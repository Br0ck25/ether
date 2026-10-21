package notify

import (
	"context"
	"crypto/ecdh"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/braedonsaunders/ether/internal/config"
)

type PushKeys struct {
	Auth   string `json:"auth"`
	P256dh string `json:"p256dh"`
}
type PushSubscription struct {
	Endpoint string   `json:"endpoint"`
	Keys     PushKeys `json:"keys"`
}

func validatePush(p *PushSubscription) error {
	if p == nil {
		return errors.New("browser subscription required; connect this device from Notifications")
	}
	u, err := url.Parse(p.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("HTTPS push endpoint required")
	}
	auth, err := base64.RawURLEncoding.DecodeString(p.Keys.Auth)
	if err != nil || len(auth) != 16 {
		return errors.New("invalid browser authentication key")
	}
	pub, err := base64.RawURLEncoding.DecodeString(p.Keys.P256dh)
	if err != nil {
		return errors.New("invalid browser encryption key")
	}
	if _, err := ecdh.P256().NewPublicKey(pub); err != nil {
		return errors.New("invalid browser encryption key")
	}
	return nil
}

// One durable VAPID identity is shared across nodes and survives restarts.
func (s *Service) pushKeys(ctx context.Context) (private, public string, err error) {
	if s.pushPrivate != "" {
		return s.pushPrivate, s.pushPublic, nil
	}
	err = s.db.QueryRowContext(ctx, `SELECT private_key,public_key FROM notify_push_keys WHERE id='vapid'`).Scan(&private, &public)
	if errors.Is(err, sql.ErrNoRows) {
		private, public, err = webpush.GenerateVAPIDKeys()
		if err != nil {
			return "", "", err
		}
		sealed, serr := config.SealWith(s.opts.Sealer, private, pushKeyContext(public)...)
		if serr != nil {
			return "", "", serr
		}
		if _, err = s.db.ExecContext(ctx, `INSERT INTO notify_push_keys(id,private_key,public_key) VALUES('vapid',?,?) ON CONFLICT(id) DO NOTHING`, sealed, public); err != nil {
			return "", "", err
		}
		err = s.db.QueryRowContext(ctx, `SELECT private_key,public_key FROM notify_push_keys WHERE id='vapid'`).Scan(&private, &public)
	}
	if err != nil {
		return "", "", err
	}
	if private, _, err = config.OpenWith(s.opts.Sealer, private, pushKeyContext(public)...); err != nil {
		return "", "", fmt.Errorf("notify: push identity: %w", err)
	}
	return private, public, nil
}
func (s *Service) sendPush(ctx context.Context, c Channel, e Event) error {
	if err := validatePush(c.Subscription); err != nil {
		return err
	}
	private, public, err := s.pushKeys(ctx)
	if err != nil {
		return errors.New("browser push identity unavailable")
	}
	payload, _ := json.Marshal(map[string]string{"title": clip(e.Title, 150), "message": clip(e.Message, 450), "url": "/ether/notifications/deliveries"})
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{Endpoint: c.Subscription.Endpoint, Keys: webpush.Keys{Auth: c.Subscription.Keys.Auth, P256dh: c.Subscription.Keys.P256dh}}, &webpush.Options{HTTPClient: s.opts.Client, Subscriber: "https://ether.local", VAPIDPrivateKey: private, VAPIDPublicKey: public, TTL: 3600, Urgency: webpush.UrgencyHigh})
	if err != nil {
		return errors.New("browser push transport failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
	if resp.StatusCode == 404 || resp.StatusCode == 410 {
		return errors.New("browser subscription expired; reconnect this device")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("browser push service rejected the notification")
	}
	return nil
}
