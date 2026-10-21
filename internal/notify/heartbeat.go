package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Heartbeat struct {
	Enabled  bool   `json:"enabled"`
	Endpoint string `json:"endpoint"`
}

func (s *Service) HeartbeatSettings(ctx context.Context, redact bool) (Heartbeat, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT config FROM notify_settings WHERE id='heartbeat'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Heartbeat{}, nil
	}
	if err != nil {
		return Heartbeat{}, err
	}
	var h Heartbeat
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return h, err
	}
	if redact && h.Endpoint != "" {
		h.Endpoint = Redacted
	}
	return h, nil
}
func (s *Service) SaveHeartbeat(ctx context.Context, h Heartbeat) error {
	if h.Endpoint == Redacted {
		old, err := s.HeartbeatSettings(ctx, false)
		if err != nil {
			return err
		}
		h.Endpoint = old.Endpoint
	}
	if h.Endpoint != "" {
		u, err := url.Parse(h.Endpoint)
		if err != nil || u.Host == "" || u.Scheme != "https" || u.User != nil || strings.ContainsAny(h.Endpoint, "\r\n") {
			return errors.New("HTTPS heartbeat URL required")
		}
	}
	if h.Enabled && h.Endpoint == "" {
		return errors.New("heartbeat URL required")
	}
	raw, _ := json.Marshal(h)
	_, err := s.db.ExecContext(ctx, `INSERT INTO notify_settings(id,config) VALUES('heartbeat',?) ON CONFLICT(id) DO UPDATE SET config=excluded.config`, string(raw))
	return err
}

// Heartbeat is called only after a successful local HTTP probe. The remote
// monitor must alert when these one-minute pings stop (allow a three-minute grace).
func (s *Service) Heartbeat(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	h, err := s.HeartbeatSettings(ctx, false)
	if err != nil || !h.Enabled {
		return err
	}
	return s.pingHeartbeat(ctx, h)
}
func (s *Service) pingHeartbeat(ctx context.Context, h Heartbeat) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.Endpoint, nil)
	if err != nil {
		return errors.New("invalid heartbeat URL")
	}
	resp, err := s.opts.Client.Do(req)
	if err != nil {
		return errors.New("heartbeat transport failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("heartbeat endpoint rejected the ping")
	}
	return nil
}
