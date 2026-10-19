package stats

import (
	"bytes"
	"context"
	"fmt"
	"github.com/braedonsaunders/ether/internal/store"
	"time"
)

func init() {
	store.Register("stats", 5, `CREATE TABLE stats_newsletter_settings(id INTEGER PRIMARY KEY CHECK(id=1),enabled INTEGER NOT NULL DEFAULT 0,weekday INTEGER NOT NULL DEFAULT 1,hour INTEGER NOT NULL DEFAULT 9,user_id INTEGER NOT NULL DEFAULT 0,next_due INTEGER NOT NULL DEFAULT 0);INSERT INTO stats_newsletter_settings(id) VALUES(1);CREATE TABLE stats_newsletter_ledger(due INTEGER PRIMARY KEY,attempted INTEGER NOT NULL,status TEXT NOT NULL);`)
}

type NewsletterSettings struct {
	Enabled     bool   `json:"enabled"`
	Weekday     int    `json:"weekday"`
	Hour        int    `json:"hour"`
	UserID      int64  `json:"userId"`
	NextDue     int64  `json:"nextDue"`
	LastAttempt int64  `json:"lastAttempt"`
	LastStatus  string `json:"lastStatus"`
}

func (s *Service) NewsletterSettings(ctx context.Context) (NewsletterSettings, error) {
	var v NewsletterSettings
	err := s.db.QueryRowContext(ctx, `SELECT enabled,weekday,hour,user_id,next_due FROM stats_newsletter_settings WHERE id=1`).Scan(&v.Enabled, &v.Weekday, &v.Hour, &v.UserID, &v.NextDue)
	if err != nil {
		return v, err
	}
	_ = s.db.QueryRowContext(ctx, `SELECT attempted,status FROM stats_newsletter_ledger ORDER BY due DESC LIMIT 1`).Scan(&v.LastAttempt, &v.LastStatus)
	return v, nil
}
func (s *Service) SaveNewsletter(ctx context.Context, v NewsletterSettings) error {
	if v.Weekday < 0 || v.Weekday > 6 || v.Hour < 0 || v.Hour > 23 || v.UserID < 0 {
		return fmt.Errorf("weekday 0–6, UTC hour 0–23, and nonnegative viewer ID required")
	}
	if v.Enabled && s.opts.Newsletter == nil {
		return fmt.Errorf("newsletter delivery unavailable")
	}
	now := s.opts.Now().UTC()
	due := time.Date(now.Year(), now.Month(), now.Day(), v.Hour, 0, 0, 0, time.UTC).AddDate(0, 0, (v.Weekday-int(now.Weekday())+7)%7)
	if !due.After(now) {
		due = due.AddDate(0, 0, 7)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE stats_newsletter_settings SET enabled=?,weekday=?,hour=?,user_id=?,next_due=? WHERE id=1`, v.Enabled, v.Weekday, v.Hour, v.UserID, due.Unix())
	return err
}
func (s *Service) RenderNewsletter(ctx context.Context, userID int64) (string, error) {
	until := s.opts.Now().UTC()
	v, err := s.Summary(ctx, until.AddDate(0, 0, -7), until, userID)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	err = newsletterTemplate.Execute(&b, struct {
		Summary
		Hours string
	}{v, fmt.Sprintf("%.1f", float64(v.WatchedMS)/3600000)})
	return b.String(), err
}

// MaybeNewsletter claims each weekly slot in a durable ledger before enqueueing.
// Queue errors remain visible and are not retried automatically, preventing
// duplicates after an ambiguous process interruption.
func (s *Service) MaybeNewsletter(ctx context.Context) error {
	v, err := s.NewsletterSettings(ctx)
	if err != nil || !v.Enabled || s.opts.Newsletter == nil {
		return err
	}
	now := s.opts.Now().UTC()
	if v.NextDue <= 0 || v.NextDue > now.Unix() {
		return nil
	}
	claim, err := s.db.ExecContext(ctx, `INSERT INTO stats_newsletter_ledger(due,attempted,status) VALUES(?,?,'sending') ON CONFLICT(due) DO NOTHING`, v.NextDue, now.Unix())
	if err != nil {
		return err
	}
	n, err := claim.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		status := "queued"
		html, renderErr := s.RenderNewsletter(ctx, v.UserID)
		if renderErr != nil {
			err = renderErr
		} else {
			err = s.opts.Newsletter(ctx, html)
		}
		if err != nil {
			status = "failed"
		}
		if _, saveErr := s.db.ExecContext(ctx, `UPDATE stats_newsletter_ledger SET status=? WHERE due=?`, status, v.NextDue); saveErr != nil {
			return saveErr
		}
	}
	next := time.Unix(v.NextDue, 0).UTC()
	for !next.After(now) {
		next = next.AddDate(0, 0, 7)
	}
	_, saveErr := s.db.ExecContext(ctx, `UPDATE stats_newsletter_settings SET next_due=? WHERE id=1 AND next_due=?`, next.Unix(), v.NextDue)
	if saveErr != nil {
		return saveErr
	}
	return err
}
