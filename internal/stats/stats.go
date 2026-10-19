// Package stats records owner analytics from observed playback and provider data.
// It never reconstructs device or bandwidth information from watch counts.
package stats

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/newznab"
	"github.com/braedonsaunders/ether/internal/playback"
	"github.com/braedonsaunders/ether/internal/state"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/usenet"
)

func init() {
	store.Register("stats", 1, `
CREATE TABLE stats_plays (id TEXT PRIMARY KEY,user_id INTEGER NOT NULL,rating_key INTEGER NOT NULL,username TEXT NOT NULL,title TEXT NOT NULL,platform TEXT NOT NULL,device TEXT NOT NULL,decision TEXT NOT NULL,started INTEGER NOT NULL,updated INTEGER NOT NULL,ended INTEGER NOT NULL DEFAULT 0,bytes INTEGER NOT NULL DEFAULT 0,watched_ms INTEGER NOT NULL DEFAULT 0,duration_ms INTEGER NOT NULL DEFAULT 0,completed INTEGER NOT NULL DEFAULT 0,failovers INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',timeline INTEGER NOT NULL DEFAULT 0,timeline_state TEXT NOT NULL DEFAULT '');
CREATE INDEX stats_plays_started ON stats_plays(started DESC);
CREATE INDEX stats_plays_user ON stats_plays(user_id,started DESC);
CREATE TABLE stats_provider_days (provider TEXT NOT NULL,day TEXT NOT NULL,bytes INTEGER NOT NULL,articles INTEGER NOT NULL,misses INTEGER NOT NULL,errors INTEGER NOT NULL,PRIMARY KEY(provider,day));
CREATE TABLE stats_snapshots (kind TEXT PRIMARY KEY,payload TEXT NOT NULL,at INTEGER NOT NULL);
CREATE TABLE stats_costs (kind TEXT NOT NULL,id TEXT NOT NULL,monthly REAL NOT NULL,currency TEXT NOT NULL,PRIMARY KEY(kind,id));
`)
}

type Options struct {
	Newsletter    func(context.Context, string) error
	StopSession   func(string) error
	SessionExists func(string) bool
	Playback      core.Playback
	Providers     func() []usenet.ProviderStats
	Indexers      func(context.Context) ([]newznab.Remaining, error)
	Logger        *slog.Logger
	Now           func() time.Time
}
type Service struct {
	db       *sql.DB
	opts     Options
	mu       sync.Mutex
	previous map[string]usenet.ProviderStats
}

func New(db *sql.DB, opts Options) *Service {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	svc := &Service{db: db, opts: opts, previous: map[string]usenet.ProviderStats{}}
	if _, err := db.Exec(`UPDATE stats_plays SET ended=updated WHERE ended=0`); err != nil {
		opts.Logger.Warn("close prior playback stats", "err", err)
	}
	return svc
}

// ObservePlayback is suitable for playback.Manager.Subscribe.
func (s *Service) ObservePlayback(ev playback.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if ev.Session == nil {
		return
	}
	if err := s.Record(ctx, ev.Session, ev.Kind == "stop", ev.Error); err != nil {
		s.opts.Logger.Warn("record playback stats", "err", err)
	}
}
func (s *Service) Record(ctx context.Context, p *core.PlaySession, ended bool, problem string) error {
	if p == nil || p.ID == "" {
		return nil
	}
	now := s.opts.Now().UTC()
	start := p.StartedAt
	if start.IsZero() {
		start = now
	}
	end := int64(0)
	if ended {
		end = now.Unix()
	}
	device := p.Client.DeviceName
	if device == "" {
		device = p.Client.Device
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO stats_plays(id,user_id,rating_key,username,title,platform,device,decision,started,updated,ended,bytes,duration_ms,failovers,error) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET username=excluded.username,title=excluded.title,platform=excluded.platform,device=excluded.device,decision=excluded.decision,updated=excluded.updated,ended=MAX(stats_plays.ended,excluded.ended),bytes=MAX(stats_plays.bytes,excluded.bytes),duration_ms=MAX(stats_plays.duration_ms,excluded.duration_ms),failovers=excluded.failovers,error=CASE WHEN excluded.error='' THEN stats_plays.error ELSE excluded.error END`, p.ID, p.UserID, int64(p.Key), p.Username, p.TitleName, p.Client.Platform, device, string(p.Decision), start.Unix(), now.Unix(), end, p.BytesServed, p.Info.Duration.Milliseconds(), p.Failovers, problem)
	return err
}

// ObserveState correlates timeline data only when exactly one active session
// matches the user/title. Plex state events do not identify the client.
func (s *Service) ObserveState(ev state.Event) {
	if ev.Kind != state.EventProgress && ev.Kind != state.EventCompleted && ev.Kind != state.EventScrobble {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	now := ev.At
	if now.IsZero() {
		now = s.opts.Now()
	}
	t := now.UnixMilli()
	completed := 0
	if ev.Completed {
		completed = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE stats_plays SET watched_ms=watched_ms+CASE WHEN ?='progress' AND timeline_state='playing' AND timeline>0 AND ?>timeline AND ?-timeline<=60000 THEN ?-timeline ELSE 0 END,timeline=?,timeline_state=?,duration_ms=MAX(duration_ms,?),completed=MAX(completed,?) WHERE id=(SELECT id FROM stats_plays WHERE user_id=? AND rating_key=? AND ended=0) AND (SELECT COUNT(*) FROM stats_plays WHERE user_id=? AND rating_key=? AND ended=0)=1`, string(ev.Kind), t, t, t, t, ev.State, ev.Duration.Milliseconds(), completed, ev.UserID, int64(ev.Key), ev.UserID, int64(ev.Key))
	if err != nil {
		s.opts.Logger.Warn("record timeline stats", "err", err)
	}
}

// Run samples active bandwidth and provider counters until cancellation.
func (s *Service) Run(ctx context.Context) {
	s.Sample(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Sample(ctx)
		}
	}
}
func (s *Service) Sample(ctx context.Context) {
	if err := s.MaybeNewsletter(ctx); err != nil {
		s.opts.Logger.Warn("weekly newsletter queue failed")
	}
	if s.opts.Playback != nil {
		for _, p := range s.opts.Playback.Sessions() {
			if err := s.Record(ctx, p, false, ""); err != nil {
				s.opts.Logger.Warn("sample play stats", "err", err)
			}
		}
	}
	if s.opts.Providers != nil {
		if err := s.SnapshotProviders(ctx, s.opts.Providers()); err != nil {
			s.opts.Logger.Warn("sample provider stats", "err", err)
		}
	}
	if s.opts.Indexers != nil {
		if v, err := s.opts.Indexers(ctx); err == nil {
			s.snapshot(ctx, "indexers", v)
		}
	}
}
func (s *Service) snapshot(ctx context.Context, kind string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO stats_snapshots(kind,payload,at) VALUES(?,?,?) ON CONFLICT(kind) DO UPDATE SET payload=excluded.payload,at=excluded.at`, kind, string(b), s.opts.Now().Unix())
	return err
}
func (s *Service) SnapshotProviders(ctx context.Context, ps []usenet.ProviderStats) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := s.opts.Now().UTC().Format("2006-01-02")
	delta := func(n, o int64) int64 {
		if n < o {
			return n
		}
		return n - o
	}
	for _, p := range ps {
		id := p.ID
		if id == "" {
			id = p.Host
		}
		old := s.previous[id]
		_, err := s.db.ExecContext(ctx, `INSERT INTO stats_provider_days(provider,day,bytes,articles,misses,errors,backfill_saves,body_misses,body_takedowns) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(provider,day) DO UPDATE SET bytes=bytes+excluded.bytes,articles=articles+excluded.articles,misses=misses+excluded.misses,errors=errors+excluded.errors,backfill_saves=backfill_saves+excluded.backfill_saves,body_misses=body_misses+excluded.body_misses,body_takedowns=body_takedowns+excluded.body_takedowns`, id, day, delta(p.Bytes, old.Bytes), delta(p.Articles, old.Articles), delta(p.Misses, old.Misses), delta(p.Errors, old.Errors), delta(p.BackfillSaves, old.BackfillSaves), delta(p.BodyMisses, old.BodyMisses), delta(p.BodyTakedowns, old.BodyTakedowns))
		if err != nil {
			return err
		}
		s.previous[id] = p
	}
	return s.snapshot(ctx, "providers", ps)
}

type Play struct {
	ID         string `json:"id"`
	UserID     int64  `json:"userId"`
	Key        int64  `json:"ratingKey"`
	Username   string `json:"username"`
	Title      string `json:"title"`
	Platform   string `json:"platform"`
	Device     string `json:"device"`
	Decision   string `json:"decision"`
	Started    int64  `json:"started"`
	Updated    int64  `json:"updated"`
	Ended      int64  `json:"ended"`
	Bytes      int64  `json:"bytes"`
	WatchedMS  int64  `json:"watchedMs"`
	DurationMS int64  `json:"durationMs"`
	Completed  bool   `json:"completed"`
	Failovers  int    `json:"failovers"`
	Error      string `json:"error"`
}

func (s *Service) History(ctx context.Context, since, until time.Time, user int64, limit int) ([]Play, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,user_id,rating_key,username,title,platform,device,decision,started,updated,ended,bytes,watched_ms,duration_ms,completed,failovers,error FROM stats_plays WHERE started>=? AND started<? AND (?=0 OR user_id=?) ORDER BY started DESC LIMIT ?`, since.Unix(), until.Unix(), user, user, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Play{}
	for rows.Next() {
		var p Play
		if err := rows.Scan(&p.ID, &p.UserID, &p.Key, &p.Username, &p.Title, &p.Platform, &p.Device, &p.Decision, &p.Started, &p.Updated, &p.Ended, &p.Bytes, &p.WatchedMS, &p.DurationMS, &p.Completed, &p.Failovers, &p.Error); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type Bucket struct {
	Label     string `json:"label"`
	Plays     int64  `json:"plays"`
	WatchedMS int64  `json:"watchedMs"`
	Bytes     int64  `json:"bytes"`
}
type Summary struct {
	Plays     int64     `json:"plays"`
	WatchedMS int64     `json:"watchedMs"`
	Bytes     int64     `json:"bytes"`
	Completed int64     `json:"completed"`
	Daily     []Bucket  `json:"daily"`
	Hourly    []Bucket  `json:"hourly"`
	Platforms []Bucket  `json:"platforms"`
	Titles    []Bucket  `json:"titles"`
	Users     []Bucket  `json:"users"`
	Since     time.Time `json:"since"`
	Until     time.Time `json:"until"`
}

func (s *Service) Summary(ctx context.Context, since, until time.Time, user int64) (Summary, error) {
	out := Summary{Since: since, Until: until}
	where := ` FROM stats_plays WHERE started>=? AND started<? AND (?=0 OR user_id=?)`
	args := []any{since.Unix(), until.Unix(), user, user}
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(watched_ms),0),COALESCE(SUM(bytes),0),COALESCE(SUM(completed),0)`+where, args...).Scan(&out.Plays, &out.WatchedMS, &out.Bytes, &out.Completed)
	if err != nil {
		return out, err
	}
	groups := []struct {
		expr  string
		dst   *[]Bucket
		order string
	}{{store.UnixDay(store.DialectOf(s.db), "started"), &out.Daily, "1"}, {store.UnixHour(store.DialectOf(s.db), "started"), &out.Hourly, "1"}, {`platform`, &out.Platforms, "2 DESC"}, {`title`, &out.Titles, "2 DESC"}, {`username || ' (' || user_id || ')'`, &out.Users, "2 DESC"}}
	for _, g := range groups {
		*g.dst = []Bucket{}
		rows, err := s.db.QueryContext(ctx, `SELECT `+g.expr+`,COUNT(*),COALESCE(SUM(watched_ms),0),COALESCE(SUM(bytes),0)`+where+` GROUP BY 1 ORDER BY `+g.order+` LIMIT 366`, args...)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var b Bucket
			if err := rows.Scan(&b.Label, &b.Plays, &b.WatchedMS, &b.Bytes); err != nil {
				rows.Close()
				return out, err
			}
			*g.dst = append(*g.dst, b)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

type Cost struct {
	Kind     string  `json:"kind"`
	ID       string  `json:"id"`
	Monthly  float64 `json:"monthly"`
	Currency string  `json:"currency"`
}

func (s *Service) SetCost(ctx context.Context, c Cost) error {
	c.Currency = strings.ToUpper(c.Currency)
	validCurrency := len(c.Currency) == 3
	for _, r := range c.Currency {
		if r < 'A' || r > 'Z' {
			validCurrency = false
		}
	}
	if (c.Kind != "provider" && c.Kind != "indexer") || c.ID == "" || c.Monthly < 0 || c.Monthly > 1000000 || math.IsNaN(c.Monthly) || math.IsInf(c.Monthly, 0) || !validCurrency {
		return fmt.Errorf("kind, ID, nonnegative price and three-letter currency required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO stats_costs(kind,id,monthly,currency) VALUES(?,?,?,?) ON CONFLICT(kind,id) DO UPDATE SET monthly=excluded.monthly,currency=excluded.currency`, c.Kind, c.ID, c.Monthly, c.Currency)
	return err
}
