package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/braedonsaunders/ether/internal/config"
)

// Channel secrets are sealed inside the stored channel JSON
// (config.FieldSealer). Each is bound to the channel's id and kind; the
// token, username and password are also bound to the endpoint they are
// sent to and the push keys to their subscription endpoint, so a stored
// channel repointed elsewhere cannot use them. Endpoints are sealed too:
// a webhook URL is itself a credential.

// errSecretNeedsDestination is returned by Save when a redacted secret
// would be carried to a different endpoint or kind.
var errSecretNeedsDestination = errors.New("re-enter the channel's token and password when changing its endpoint or kind")

type channelField struct {
	name string
	p    *string
	dest func() string
}

// fields lists c's secrets in dependency order: endpoints first, since the
// other secrets are bound to their plaintext.
func channelFields(c *Channel) []channelField {
	endpoint := func() string { return c.Endpoint }
	fs := []channelField{
		{"endpoint", &c.Endpoint, nil},
		{"token", &c.Token, endpoint},
		{"username", &c.Username, endpoint},
		{"password", &c.Password, endpoint},
	}
	if c.Subscription != nil {
		sub := func() string { return c.Subscription.Endpoint }
		fs = append(fs,
			channelField{"subscription.endpoint", &c.Subscription.Endpoint, nil},
			channelField{"subscription.auth", &c.Subscription.Keys.Auth, sub},
			channelField{"subscription.p256dh", &c.Subscription.Keys.P256dh, sub})
	}
	return fs
}

func channelContext(c *Channel, f channelField) []string {
	ctx := []string{"notify_channels", f.name, c.ID, c.Kind}
	if f.dest != nil {
		ctx = append(ctx, f.dest())
	}
	return ctx
}

// sealChannel returns the stored form of c. Bound contexts use plaintext
// endpoints, so dependents are sealed before their endpoint.
func (s *Service) sealChannel(c Channel) (Channel, error) {
	if c.Subscription != nil {
		sub := *c.Subscription
		c.Subscription = &sub
	}
	fs := channelFields(&c)
	for i := len(fs) - 1; i >= 0; i-- {
		f := fs[i]
		v, err := config.SealWith(s.opts.Sealer, *f.p, channelContext(&c, f)...)
		if err != nil {
			return Channel{}, fmt.Errorf("notify: seal channel %s: %w", c.ID, err)
		}
		*f.p = v
	}
	return c, nil
}

// openChannel reverses sealChannel; legacy reports plaintext secrets.
func (s *Service) openChannel(c Channel) (Channel, bool, error) {
	if c.Subscription != nil {
		sub := *c.Subscription
		c.Subscription = &sub
	}
	legacy := false
	for _, f := range channelFields(&c) {
		v, l, err := config.OpenWith(s.opts.Sealer, *f.p, channelContext(&c, f)...)
		if err != nil {
			return Channel{}, false, fmt.Errorf("notify: channel %s: %w", c.ID, err)
		}
		*f.p, legacy = v, legacy || l
	}
	return c, legacy, nil
}

func (s *Service) encodeChannel(c Channel) (string, error) {
	sealed, err := s.sealChannel(c)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(sealed)
	return string(raw), err
}

func (s *Service) decodeChannel(raw string) (Channel, bool, error) {
	var c Channel
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Channel{}, false, err
	}
	return s.openChannel(c)
}

// keepChannelSecrets restores secrets the owner API sent back redacted,
// refusing to carry a token or password to another endpoint or kind.
func keepChannelSecrets(c *Channel, old Channel) error {
	if c.Subscription != nil && c.Subscription.Endpoint == Redacted {
		if c.Kind != old.Kind {
			return errSecretNeedsDestination
		}
		c.Subscription = old.Subscription
	}
	if c.Endpoint == Redacted {
		c.Endpoint = old.Endpoint
	}
	moved := c.Kind != old.Kind || c.Endpoint != old.Endpoint
	for _, pair := range []struct {
		p   *string
		old string
	}{{&c.Token, old.Token}, {&c.Password, old.Password}} {
		if *pair.p != Redacted {
			continue
		}
		if moved && pair.old != "" {
			return errSecretNeedsDestination
		}
		*pair.p = pair.old
	}
	return nil
}

func pushKeyContext(public string) []string {
	return []string{"notify_push_keys", "private_key", "vapid", public}
}

// MigrateSecrets seals channel secrets and the push identity stored in
// plaintext and returns how many rows it rewrote. A row is replaced only
// if it is unchanged since read, so it is safe to rerun on any node; a row
// that does not open is left untouched and reported in the error.
func (s *Service) MigrateSecrets(ctx context.Context) (int, error) {
	if s.db == nil || s.opts.Sealer == nil {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, config FROM notify_channels ORDER BY id`)
	if err != nil {
		return 0, err
	}
	type row struct{ id, raw string }
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.raw); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n, failed := 0, 0
	var firstErr error
	for _, r := range all {
		c, legacy, err := s.decodeChannel(r.raw)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !legacy {
			continue
		}
		raw, err := s.encodeChannel(c)
		if err != nil {
			return n, err
		}
		if raw == r.raw {
			continue
		}
		res, err := s.db.ExecContext(ctx, `UPDATE notify_channels SET config=? WHERE id=? AND config=?`, raw, r.id, r.raw)
		if err != nil {
			return n, err
		}
		if k, _ := res.RowsAffected(); k > 0 {
			n++
		}
	}
	var private, public string
	err = s.db.QueryRowContext(ctx, `SELECT private_key, public_key FROM notify_push_keys WHERE id='vapid'`).Scan(&private, &public)
	switch {
	case err == nil:
		plain, legacy, oerr := config.OpenWith(s.opts.Sealer, private, pushKeyContext(public)...)
		if oerr != nil {
			failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("notify: push identity: %w", oerr)
			}
			break
		}
		if !legacy {
			break
		}
		sealed, serr := config.SealWith(s.opts.Sealer, plain, pushKeyContext(public)...)
		if serr != nil {
			return n, serr
		}
		if sealed != private {
			res, uerr := s.db.ExecContext(ctx, `UPDATE notify_push_keys SET private_key=? WHERE id='vapid' AND private_key=?`, sealed, private)
			if uerr != nil {
				return n, uerr
			}
			if k, _ := res.RowsAffected(); k > 0 {
				n++
			}
		}
	case !errors.Is(err, sql.ErrNoRows):
		return n, err
	}
	if failed > 0 {
		return n, fmt.Errorf("notify: %d stored secret row(s) could not be opened and were left unchanged: %w", failed, firstErr)
	}
	return n, nil
}
