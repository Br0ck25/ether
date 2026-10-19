package personalize

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// Tables (all times are unix milliseconds; day is YYYYMMDD in server local
// time):
//
//	personalize_row_impressions   one row per user, day and hub: how often
//	                              the row was served that day and where.
//	personalize_item_impressions  one row per user, day, item and hub.
//	personalize_plays             plays observed from watch-state events,
//	                              attributed to the row they came from.
//	personalize_overrides         owner corrections to a viewer's taste.
//	personalize_layout            consumed legacy rules (migration only).
//
// Impressions are compacted on write: repeated serves of the same row on
// the same day bump a counter instead of adding rows, so a client that
// polls /hubs every minute costs nothing extra. Retention removes item
// impressions after Options.Retention (60 days) and row impressions and
// plays after 400 days.
func init() {
	store.Register("personalize", 1, `
CREATE TABLE personalize_row_impressions (
  user_id INTEGER NOT NULL,
  day INTEGER NOT NULL,
  hub_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  serves INTEGER NOT NULL DEFAULT 0,
  best_position INTEGER NOT NULL DEFAULT 0,
  first_at INTEGER NOT NULL,
  last_at INTEGER NOT NULL,
  device TEXT NOT NULL DEFAULT '',
  daypart TEXT NOT NULL DEFAULT '',
  weekend INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, day, hub_id)
);
CREATE INDEX personalize_row_impr_day ON personalize_row_impressions(day);
CREATE TABLE personalize_item_impressions (
  user_id INTEGER NOT NULL,
  day INTEGER NOT NULL,
  item_key INTEGER NOT NULL,
  hub_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  shows INTEGER NOT NULL DEFAULT 0,
  best_rank INTEGER NOT NULL DEFAULT 0,
  first_at INTEGER NOT NULL,
  last_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, day, item_key, hub_id)
);
CREATE INDEX personalize_item_impr_lookup ON personalize_item_impressions(user_id, item_key, last_at);
CREATE INDEX personalize_item_impr_day ON personalize_item_impressions(day);
CREATE TABLE personalize_plays (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  item_key INTEGER NOT NULL,
  taste_key INTEGER NOT NULL,
  hub_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  start_fraction REAL NOT NULL DEFAULT 0,
  max_fraction REAL NOT NULL DEFAULT 0,
  completed INTEGER NOT NULL DEFAULT 0,
  abandoned INTEGER NOT NULL DEFAULT 0,
  device TEXT NOT NULL DEFAULT '',
  daypart TEXT NOT NULL DEFAULT '',
  weekend INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX personalize_plays_user ON personalize_plays(user_id, started_at);
CREATE INDEX personalize_plays_started ON personalize_plays(started_at);
CREATE TABLE personalize_overrides (
  user_id INTEGER NOT NULL,
  kind TEXT NOT NULL,
  value TEXT NOT NULL,
  weight REAL NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, kind, value)
);
CREATE TABLE personalize_layout (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  rules TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);`)
}

type sqlStore struct {
	db  *sql.DB
	loc *time.Location
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

// dayOf is t's local calendar day as YYYYMMDD.
func dayOf(t time.Time, loc *time.Location) int {
	t = t.In(loc)
	return t.Year()*10000 + int(t.Month())*100 + t.Day()
}

// dayTime is the local midnight starting day d.
func dayTime(d int, loc *time.Location) time.Time {
	return time.Date(d/10000, time.Month(d/100%100), d%100, 0, 0, 0, 0, loc)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- impressions -----------------------------------------------------------

type rowAgg struct {
	userID       int64
	day          int
	hubID, kind  string
	title        string
	serves       int
	bestPosition int
	first, last  int64
	hc           core.HubContext
}

type itemAgg struct {
	userID      int64
	day         int
	key         core.RatingKey
	hubID, kind string
	shows       int
	bestRank    int
	first, last int64
}

// writeImpressions upserts pre-aggregated impressions in one transaction.
func (s *sqlStore) writeImpressions(ctx context.Context, rows []rowAgg, items []itemAgg) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rowStmt, err := tx.PrepareContext(ctx, `INSERT INTO personalize_row_impressions
		(user_id, day, hub_id, kind, title, serves, best_position, first_at, last_at, device, daypart, weekend)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(user_id, day, hub_id) DO UPDATE SET
		  serves = personalize_row_impressions.serves + excluded.serves,
		  best_position = CASE WHEN excluded.best_position < personalize_row_impressions.best_position
		    THEN excluded.best_position ELSE personalize_row_impressions.best_position END,
		  first_at = CASE WHEN excluded.first_at < personalize_row_impressions.first_at
		    THEN excluded.first_at ELSE personalize_row_impressions.first_at END,
		  last_at = CASE WHEN excluded.last_at > personalize_row_impressions.last_at
		    THEN excluded.last_at ELSE personalize_row_impressions.last_at END,
		  kind = excluded.kind, title = excluded.title,
		  device = excluded.device, daypart = excluded.daypart, weekend = excluded.weekend`)
	if err != nil {
		return err
	}
	defer rowStmt.Close()
	for _, r := range rows {
		if _, err := rowStmt.ExecContext(ctx, r.userID, r.day, r.hubID, r.kind, r.title, r.serves, r.bestPosition,
			r.first, r.last, r.hc.Device, r.hc.Daypart, b2i(r.hc.Weekend)); err != nil {
			return err
		}
	}
	itemStmt, err := tx.PrepareContext(ctx, `INSERT INTO personalize_item_impressions
		(user_id, day, item_key, hub_id, kind, shows, best_rank, first_at, last_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(user_id, day, item_key, hub_id) DO UPDATE SET
		  shows = personalize_item_impressions.shows + excluded.shows,
		  best_rank = CASE WHEN excluded.best_rank < personalize_item_impressions.best_rank
		    THEN excluded.best_rank ELSE personalize_item_impressions.best_rank END,
		  first_at = CASE WHEN excluded.first_at < personalize_item_impressions.first_at
		    THEN excluded.first_at ELSE personalize_item_impressions.first_at END,
		  last_at = CASE WHEN excluded.last_at > personalize_item_impressions.last_at
		    THEN excluded.last_at ELSE personalize_item_impressions.last_at END`)
	if err != nil {
		return err
	}
	defer itemStmt.Close()
	for _, it := range items {
		if _, err := itemStmt.ExecContext(ctx, it.userID, it.day, int64(it.key), it.hubID, it.kind, it.shows, it.bestRank, it.first, it.last); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// attribution is the row a play came from.
type attribution struct {
	hubID, kind string
	at          time.Time
}

// attribute finds the most recent impression of any of keys for a user in
// [from, to]: the row the viewer most plausibly started the play from.
func (s *sqlStore) attribute(ctx context.Context, userID int64, keys []core.RatingKey, from, to time.Time) (attribution, bool, error) {
	if len(keys) == 0 {
		return attribution{}, false, nil
	}
	q := `SELECT hub_id, kind, last_at FROM personalize_item_impressions
		WHERE user_id = ? AND last_at >= ? AND last_at <= ? AND item_key IN (` + placeholders(len(keys)) + `)
		ORDER BY last_at DESC, best_rank ASC LIMIT 1`
	args := []any{userID, ms(from), ms(to)}
	for _, k := range keys {
		args = append(args, int64(k))
	}
	var a attribution
	var at int64
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&a.hubID, &a.kind, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return attribution{}, false, nil
	}
	if err != nil {
		return attribution{}, false, err
	}
	a.at = fromMS(at)
	return a, true, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

// shownDays returns, per item key, the number of distinct days the item was
// shown to the user since `since` in rows other than Continue Watching and
// Up Next (those list titles the viewer is already watching).
func (s *sqlStore) shownDays(ctx context.Context, userID int64, since time.Time) (map[core.RatingKey]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT item_key, COUNT(DISTINCT day) FROM personalize_item_impressions
		WHERE user_id = ? AND last_at >= ? AND kind NOT IN (?, ?)
		GROUP BY item_key`, userID, ms(since), KindContinue, KindUpNext)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[core.RatingKey]int{}
	for rows.Next() {
		var k int64
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[core.RatingKey(k)] = n
	}
	return out, rows.Err()
}

// lastSeen returns each user's latest impression time.
func (s *sqlStore) lastSeen(ctx context.Context) (map[int64]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, MAX(last_at) FROM personalize_row_impressions GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var id, at int64
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = fromMS(at)
	}
	return out, rows.Err()
}

// rowStat is per-row-kind engagement.
type rowStat struct {
	kind, hubID, title string
	shows              float64 // row-days shown
	plays              float64 // plays attributed
	reward             float64 // sum of play rewards (completion-weighted)
}

// kindShows counts row-days per (user, kind) since `since`.
func (s *sqlStore) kindShows(ctx context.Context, since time.Time) (map[int64]map[string]float64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, kind, COUNT(*) FROM personalize_row_impressions
		WHERE last_at >= ? GROUP BY user_id, kind`, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[string]float64{}
	for rows.Next() {
		var uid int64
		var kind string
		var n float64
		if err := rows.Scan(&uid, &kind, &n); err != nil {
			return nil, err
		}
		if out[uid] == nil {
			out[uid] = map[string]float64{}
		}
		out[uid][kind] = n
	}
	return out, rows.Err()
}

// ---- plays -----------------------------------------------------------------

type playRecord struct {
	id            int64
	userID        int64
	key, tasteKey core.RatingKey
	hubID, kind   string
	started       time.Time
	updated       time.Time
	duration      time.Duration
	startFraction float64
	maxFraction   float64
	completed     bool
	abandoned     bool
	device        string
	daypart       string
	weekend       bool
}

// watched is how much of the title this play covered.
func (p playRecord) watched() time.Duration {
	f := p.maxFraction - p.startFraction
	if p.completed && f < 0.5 {
		// Resumed near the end and finished: count what remained.
		f = 1 - p.startFraction
	}
	if f < 0 {
		f = 0
	}
	return time.Duration(float64(p.duration) * f)
}

// sessions merges plays of the same title by the same viewer on the same
// local day (a film paused overnight, resumed after a break) into one,
// keeping the first attribution and the best outcome.
func sessions(plays []playRecord, loc *time.Location) []playRecord {
	type k struct {
		user int64
		key  core.RatingKey
		day  int
	}
	idx := map[k]int{}
	out := make([]playRecord, 0, len(plays))
	for _, p := range plays {
		key := k{p.userID, p.key, dayOf(p.started, loc)}
		i, ok := idx[key]
		if !ok {
			idx[key] = len(out)
			out = append(out, p)
			continue
		}
		m := &out[i]
		if m.kind == "" {
			m.hubID, m.kind = p.hubID, p.kind
		}
		m.completed = m.completed || p.completed
		m.maxFraction = math.Max(m.maxFraction, p.maxFraction)
		m.abandoned = m.abandoned && p.abandoned && !m.completed
		if p.updated.After(m.updated) {
			m.updated = p.updated
		}
	}
	return out
}

// reward is the bandit reward of a play: an abandoned start is barely a
// success, a finished title is a full one.
func (p playRecord) reward() float64 {
	switch {
	case p.abandoned:
		return 0.1
	case p.completed:
		return 1
	case p.maxFraction >= 0.5:
		return 0.85
	}
	return 0.6
}

func (s *sqlStore) insertPlay(ctx context.Context, p *playRecord) error {
	return s.db.QueryRowContext(ctx, `INSERT INTO personalize_plays
		(user_id, item_key, taste_key, hub_id, kind, started_at, updated_at, duration_ms, start_fraction, max_fraction, completed, abandoned, device, daypart, weekend)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`,
		p.userID, int64(p.key), int64(p.tasteKey), p.hubID, p.kind, ms(p.started), ms(p.updated), p.duration.Milliseconds(),
		p.startFraction, p.maxFraction, b2i(p.completed), b2i(p.abandoned), p.device, p.daypart, b2i(p.weekend)).Scan(&p.id)
}

func (s *sqlStore) updatePlay(ctx context.Context, p *playRecord) error {
	_, err := s.db.ExecContext(ctx, `UPDATE personalize_plays SET updated_at = ?, duration_ms = ?, max_fraction = ?,
		completed = ?, abandoned = ? WHERE id = ?`,
		ms(p.updated), p.duration.Milliseconds(), p.maxFraction, b2i(p.completed), b2i(p.abandoned), p.id)
	return err
}

// plays lists plays started at or after since, oldest first. userID 0
// means every user.
func (s *sqlStore) plays(ctx context.Context, userID int64, since time.Time) ([]playRecord, error) {
	q := `SELECT id, user_id, item_key, taste_key, hub_id, kind, started_at, updated_at, duration_ms, start_fraction,
		max_fraction, completed, abandoned, device, daypart, weekend FROM personalize_plays WHERE started_at >= ?`
	args := []any{ms(since)}
	if userID != 0 {
		q += ` AND user_id = ?`
		args = append(args, userID)
	}
	q += ` ORDER BY started_at, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []playRecord
	for rows.Next() {
		var p playRecord
		var key, tk, started, updated, dur int64
		var completed, abandoned, weekend int
		if err := rows.Scan(&p.id, &p.userID, &key, &tk, &p.hubID, &p.kind, &started, &updated, &dur, &p.startFraction,
			&p.maxFraction, &completed, &abandoned, &p.device, &p.daypart, &weekend); err != nil {
			return nil, err
		}
		p.key, p.tasteKey = core.RatingKey(key), core.RatingKey(tk)
		p.started, p.updated = fromMS(started), fromMS(updated)
		p.duration = time.Duration(dur) * time.Millisecond
		p.completed, p.abandoned, p.weekend = completed != 0, abandoned != 0, weekend != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- row insights ------------------------------------------------------------

// rowShowsSince returns row-days per kind since `since`, with the latest
// title seen for the kind.
func (s *sqlStore) rowShowsSince(ctx context.Context, since time.Time) (map[string]*rowStat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, hub_id, title, last_at FROM personalize_row_impressions WHERE last_at >= ?
		ORDER BY last_at`, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*rowStat{}
	for rows.Next() {
		var kind, hubID, title string
		var at int64
		if err := rows.Scan(&kind, &hubID, &title, &at); err != nil {
			return nil, err
		}
		st := out[kind]
		if st == nil {
			st = &rowStat{kind: kind}
			out[kind] = st
		}
		st.shows++
		st.hubID, st.title = hubID, title
	}
	return out, rows.Err()
}

// ---- overrides ---------------------------------------------------------------

func (s *sqlStore) overrides(ctx context.Context, userID int64) ([]core.TasteOverride, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, value, weight FROM personalize_overrides WHERE user_id = ?
		ORDER BY kind, value`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.TasteOverride{}
	for rows.Next() {
		var o core.TasteOverride
		if err := rows.Scan(&o.Kind, &o.Value, &o.Weight); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// setOverrides replaces a user's overrides.
func (s *sqlStore) setOverrides(ctx context.Context, userID int64, os []core.TasteOverride, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM personalize_overrides WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, o := range os {
		if _, err := tx.ExecContext(ctx, `INSERT INTO personalize_overrides (user_id, kind, value, weight, updated_at)
			VALUES (?,?,?,?,?) ON CONFLICT(user_id, kind, value) DO UPDATE SET weight = excluded.weight, updated_at = excluded.updated_at`,
			userID, o.Kind, o.Value, o.Weight, ms(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- layout --------------------------------------------------------------------

func (s *sqlStore) layout(ctx context.Context) ([]core.LayoutRule, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT rules FROM personalize_layout WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return []core.LayoutRule{}, nil
	}
	if err != nil {
		return nil, err
	}
	var rules []core.LayoutRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, err
	}
	if rules == nil {
		rules = []core.LayoutRule{}
	}
	return rules, nil
}

// ---- retention -------------------------------------------------------------------

// purge applies retention: item impressions older than itemDays days, row
// impressions and plays older than 400 days. It returns the rows removed.
func (s *sqlStore) purge(ctx context.Context, now time.Time, itemRetention time.Duration) (int64, error) {
	var total int64
	cutItems := dayOf(now.Add(-itemRetention), s.loc)
	cutRows := dayOf(now.AddDate(0, 0, -400), s.loc)
	for _, st := range []struct {
		q   string
		arg any
	}{
		{`DELETE FROM personalize_item_impressions WHERE day < ?`, cutItems},
		{`DELETE FROM personalize_row_impressions WHERE day < ?`, cutRows},
		{`DELETE FROM personalize_plays WHERE started_at < ?`, ms(now.AddDate(0, 0, -400))},
	} {
		res, err := s.db.ExecContext(ctx, st.q, st.arg)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
