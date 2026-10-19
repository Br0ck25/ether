// Package auth implements core.Auth: it maps the X-Plex-Token a Plex app
// presents to an Ether user, and keeps the users table with per-user
// settings (stream limit, content rating cap, languages, sections).
//
// A token is recognised, in order, as
//
//  1. the server's own token (the owner),
//  2. a server access token synced from plex.tv for the owner or a friend,
//  3. any other plex.tv token, validated against plex.tv (GET /api/v2/user)
//     and checked for access to this server (owner id, synced friend, or
//     the server in that token's resources list).
//
// Results of (3) are cached (positive and negative) so plex.tv is asked at
// most once per token per TTL; during a plex.tv outage a previously valid
// token keeps working for StaleTTL.
package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plextv"
	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	store.Register("auth", 1, `
CREATE TABLE auth_users (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	plex_id            INTEGER NOT NULL UNIQUE,
	uuid               TEXT    NOT NULL DEFAULT '',
	username           TEXT    NOT NULL DEFAULT '',
	title              TEXT    NOT NULL DEFAULT '',
	email              TEXT    NOT NULL DEFAULT '',
	thumb              TEXT    NOT NULL DEFAULT '',
	is_owner           INTEGER NOT NULL DEFAULT 0,
	is_home            INTEGER NOT NULL DEFAULT 0,
	restricted         INTEGER NOT NULL DEFAULT 0,
	active             INTEGER NOT NULL DEFAULT 1,
	origin             TEXT    NOT NULL DEFAULT 'friend',
	plex_sections      TEXT    NOT NULL DEFAULT '[]',
	stream_limit       INTEGER NOT NULL DEFAULT 0,
	max_content_rating TEXT    NOT NULL DEFAULT '',
	subtitle_languages TEXT    NOT NULL DEFAULT '[]',
	audio_languages    TEXT    NOT NULL DEFAULT '[]',
	allowed_sections   TEXT    NOT NULL DEFAULT '[]',
	created_at         INTEGER NOT NULL,
	updated_at         INTEGER NOT NULL,
	last_seen_at       INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE auth_tokens (
	token_hash TEXT    PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
	source     TEXT    NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX auth_tokens_user ON auth_tokens(user_id);
`)
}

// User origins.
const (
	originOwner     = "owner"
	originFriend    = "friend"    // from the plex.tv friend sync
	originValidated = "validated" // first seen through plex.tv validation
)

// Token sources in auth_tokens.
const (
	sourceServer = "server" // the server token (owner)
	sourceFriend = "friend" // synced access tokens (owner's or a friend's)
)

// Validator checks tokens Ether does not already know against plex.tv.
type Validator interface {
	// Account returns the account behind token. An error matching
	// core.ErrUnauthorized means the token is invalid; other errors mean
	// plex.tv could not answer.
	Account(ctx context.Context, token string) (*plextv.Account, error)
	// HasServer reports whether token's resources list includes this
	// server (i.e. the account was granted access).
	HasServer(ctx context.Context, token string) (bool, error)
}

// PlexTVValidator is the production Validator.
type PlexTVValidator struct {
	Client    *plextv.Client
	MachineID string
}

// Account implements Validator.
func (v PlexTVValidator) Account(ctx context.Context, token string) (*plextv.Account, error) {
	return v.Client.User(ctx, token)
}

// HasServer implements Validator.
func (v PlexTVValidator) HasServer(ctx context.Context, token string) (bool, error) {
	_, err := v.Client.FindServer(ctx, token, v.MachineID)
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Options configures the Service.
type Options struct {
	// Validator checks unknown tokens; nil means only server and synced
	// tokens are accepted.
	Validator Validator
	// CacheTTL is how long a plex.tv-validated token is trusted (15m).
	CacheTTL time.Duration
	// NegativeTTL is how long a rejected token is remembered (2m).
	NegativeTTL time.Duration
	// StaleTTL lets a validated token keep working this long past CacheTTL
	// while plex.tv is unreachable (24h).
	StaleTTL time.Duration
	// MaxCacheEntries bounds the token caches (10000).
	MaxCacheEntries int
	Logger          *slog.Logger
	Now             func() time.Time
}

type cacheEntry struct {
	userID  int64
	expires time.Time
}

type call struct {
	done chan struct{}
	user *core.User
	err  error
}

// Service implements core.Auth and plextv.UserSync.
type Service struct {
	db  *sql.DB
	o   Options
	log *slog.Logger

	mu          sync.RWMutex
	users       map[int64]*row  // by local id
	byPlex      map[int64]int64 // plex id -> local id
	tokens      map[string]int64
	pos         map[string]cacheEntry
	neg         map[string]time.Time
	inflight    map[string]*call
	ownerPlexID int64

	seenMu sync.Mutex
	seen   map[int64]time.Time // pending last_seen_at writes
}

type row struct {
	core.User
	Active       bool
	Origin       string
	Home         bool
	Restricted   bool
	PlexSections []int
	LastSeen     time.Time
}

var (
	_ core.Auth       = (*Service)(nil)
	_ plextv.UserSync = (*Service)(nil)
)

// New loads users and known tokens from db (migrated with store.Migrate).
func New(ctx context.Context, db *sql.DB, o Options) (*Service, error) {
	if db == nil {
		return nil, errors.New("auth: db required")
	}
	if o.CacheTTL <= 0 {
		o.CacheTTL = 15 * time.Minute
	}
	if o.NegativeTTL <= 0 {
		o.NegativeTTL = 2 * time.Minute
	}
	if o.StaleTTL <= 0 {
		o.StaleTTL = 24 * time.Hour
	}
	if o.MaxCacheEntries <= 0 {
		o.MaxCacheEntries = 10000
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Service{db: db, o: o, log: o.Logger.With("component", "auth"),
		pos: map[string]cacheEntry{}, neg: map[string]time.Time{}, inflight: map[string]*call{},
		seen: map[int64]time.Time{}}
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// HashToken is how tokens are stored: never in plain text.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

const userCols = `id, plex_id, uuid, username, title, email, thumb, is_owner, is_home, restricted, active, origin,
	plex_sections, stream_limit, max_content_rating, subtitle_languages, audio_languages, allowed_sections, last_seen_at`

func scanRow(sc interface{ Scan(...any) error }) (*row, error) {
	var r row
	var isOwner, home, restricted, active int
	var plexSecs, subs, auds, secs string
	var lastSeen int64
	if err := sc.Scan(&r.ID, &r.PlexID, &r.UUID, &r.Username, &r.Title, &r.Email, &r.Thumb, &isOwner, &home,
		&restricted, &active, &r.Origin, &plexSecs, &r.StreamLimit, &r.MaxContentRating, &subs, &auds, &secs, &lastSeen); err != nil {
		return nil, err
	}
	r.IsOwner, r.Home, r.Restricted, r.Active = isOwner == 1, home == 1, restricted == 1, active == 1
	_ = json.Unmarshal([]byte(plexSecs), &r.PlexSections)
	_ = json.Unmarshal([]byte(subs), &r.SubtitleLanguages)
	_ = json.Unmarshal([]byte(auds), &r.AudioLanguages)
	_ = json.Unmarshal([]byte(secs), &r.AllowedSections)
	if lastSeen > 0 {
		r.LastSeen = time.Unix(lastSeen, 0)
	}
	return &r, nil
}

// reload rebuilds the in-memory users and token maps from the database.
func (s *Service) reload(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM auth_users`)
	if err != nil {
		return fmt.Errorf("auth: load users: %w", err)
	}
	users := map[int64]*row{}
	byPlex := map[int64]int64{}
	var owner int64
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			rows.Close()
			return err
		}
		users[r.ID] = r
		byPlex[r.PlexID] = r.ID
		if r.IsOwner {
			owner = r.PlexID
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	trows, err := s.db.QueryContext(ctx, `SELECT token_hash, user_id FROM auth_tokens`)
	if err != nil {
		return fmt.Errorf("auth: load tokens: %w", err)
	}
	tokens := map[string]int64{}
	for trows.Next() {
		var h string
		var id int64
		if err := trows.Scan(&h, &id); err != nil {
			trows.Close()
			return err
		}
		tokens[h] = id
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.users, s.byPlex, s.tokens, s.ownerPlexID = users, byPlex, tokens, owner
	// Cached validations may point at users whose state changed.
	for h, e := range s.pos {
		if u := users[e.userID]; u == nil || !u.Active {
			delete(s.pos, h)
		}
	}
	s.mu.Unlock()
	return nil
}

// effective returns the core.User view of a row.
func (r *row) effective() core.User {
	u := r.User
	u.SubtitleLanguages = append([]string{}, r.SubtitleLanguages...)
	u.AudioLanguages = append([]string{}, r.AudioLanguages...)
	u.AllowedSections = append([]int{}, r.AllowedSections...)
	if len(u.AllowedSections) == 0 && len(r.PlexSections) > 0 && !r.IsOwner {
		// No Ether-side restriction: honour the sections the owner shared
		// on plex.tv.
		u.AllowedSections = append([]int{}, r.PlexSections...)
	}
	return u
}

// Authenticate implements core.Auth.
func (s *Service) Authenticate(ctx context.Context, token string) (*core.User, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, core.ErrUnauthorized
	}
	h := HashToken(token)
	now := s.o.Now()

	s.mu.RLock()
	if id, ok := s.tokens[h]; ok {
		u := s.activeUser(id)
		s.mu.RUnlock()
		if u != nil {
			s.touch(u.ID, now)
			return u, nil
		}
		return nil, core.ErrUnauthorized
	}
	if e, ok := s.pos[h]; ok && now.Before(e.expires) {
		u := s.activeUser(e.userID)
		s.mu.RUnlock()
		if u != nil {
			s.touch(u.ID, now)
			return u, nil
		}
		return nil, core.ErrUnauthorized
	}
	if exp, ok := s.neg[h]; ok && now.Before(exp) {
		s.mu.RUnlock()
		return nil, core.ErrUnauthorized
	}
	s.mu.RUnlock()

	if s.o.Validator == nil {
		return nil, core.ErrUnauthorized
	}
	return s.validate(ctx, token, h)
}

// activeUser returns a copy of an active user; callers hold s.mu.
func (s *Service) activeUser(id int64) *core.User {
	r := s.users[id]
	if r == nil || !r.Active {
		return nil
	}
	u := r.effective()
	return &u
}

// validate asks plex.tv, collapsing concurrent requests for one token.
func (s *Service) validate(ctx context.Context, token, h string) (*core.User, error) {
	s.mu.Lock()
	if c, ok := s.inflight[h]; ok {
		s.mu.Unlock()
		select {
		case <-c.done:
			return c.user, c.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c := &call{done: make(chan struct{})}
	s.inflight[h] = c
	s.mu.Unlock()

	// Detach from the caller's cancellation so one impatient client does
	// not fail the others waiting on the same token.
	vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	c.user, c.err = s.validateOnce(vctx, token, h)
	cancel()

	s.mu.Lock()
	delete(s.inflight, h)
	s.mu.Unlock()
	close(c.done)
	return c.user, c.err
}

func (s *Service) validateOnce(ctx context.Context, token, h string) (*core.User, error) {
	acct, err := s.o.Validator.Account(ctx, token)
	if err != nil {
		if errors.Is(err, core.ErrUnauthorized) {
			s.reject(h)
			return nil, core.ErrUnauthorized
		}
		return s.stale(h, err)
	}
	s.mu.RLock()
	owner := s.ownerPlexID
	localID, known := s.byPlex[acct.ID]
	var knownActive bool
	if known {
		knownActive = s.users[localID] != nil && s.users[localID].Active
	}
	s.mu.RUnlock()

	switch {
	case owner != 0 && acct.ID == owner:
		// The owner signed in on another device.
	case knownActive:
		// A friend from the last sync, using a personal token.
	default:
		ok, err := s.o.Validator.HasServer(ctx, token)
		if err != nil {
			if errors.Is(err, core.ErrUnauthorized) {
				s.reject(h)
				return nil, core.ErrUnauthorized
			}
			return s.stale(h, err)
		}
		if !ok {
			s.log.Info("token without access to this server", "plexId", acct.ID, "username", acct.Username)
			s.reject(h)
			return nil, core.ErrUnauthorized
		}
		id, err := s.upsertValidated(ctx, acct)
		if err != nil {
			return nil, err
		}
		localID = id
	}
	if owner != 0 && acct.ID == owner {
		s.mu.RLock()
		localID = s.byPlex[owner]
		s.mu.RUnlock()
	}
	s.mu.Lock()
	s.evictLocked()
	s.pos[h] = cacheEntry{userID: localID, expires: s.o.Now().Add(s.o.CacheTTL)}
	delete(s.neg, h)
	u := s.activeUser(localID)
	s.mu.Unlock()
	if u == nil {
		return nil, core.ErrUnauthorized
	}
	s.touch(u.ID, s.o.Now())
	return u, nil
}

func (s *Service) reject(h string) {
	s.mu.Lock()
	s.evictLocked()
	s.neg[h] = s.o.Now().Add(s.o.NegativeTTL)
	delete(s.pos, h)
	s.mu.Unlock()
}

// stale serves an expired positive entry while plex.tv is unreachable.
func (s *Service) stale(h string, cause error) (*core.User, error) {
	now := s.o.Now()
	s.mu.RLock()
	e, ok := s.pos[h]
	var u *core.User
	if ok && now.Before(e.expires.Add(s.o.StaleTTL)) {
		u = s.activeUser(e.userID)
	}
	s.mu.RUnlock()
	if u != nil {
		s.log.Warn("plex.tv unreachable; using cached token validation", "err", cause)
		return u, nil
	}
	return nil, fmt.Errorf("auth: validate token with plex.tv: %w", cause)
}

// evictLocked bounds the caches; callers hold s.mu for writing.
func (s *Service) evictLocked() {
	if len(s.pos)+len(s.neg) < s.o.MaxCacheEntries {
		return
	}
	now := s.o.Now()
	for h, e := range s.pos {
		if now.After(e.expires.Add(s.o.StaleTTL)) {
			delete(s.pos, h)
		}
	}
	for h, exp := range s.neg {
		if now.After(exp) {
			delete(s.neg, h)
		}
	}
	// Still full: drop arbitrary entries (map order is random).
	for h := range s.neg {
		if len(s.pos)+len(s.neg) < s.o.MaxCacheEntries*3/4 {
			break
		}
		delete(s.neg, h)
	}
	for h := range s.pos {
		if len(s.pos)+len(s.neg) < s.o.MaxCacheEntries*3/4 {
			break
		}
		delete(s.pos, h)
	}
}

// Invalidate forgets cached validations of token.
func (s *Service) Invalidate(token string) {
	h := HashToken(token)
	s.mu.Lock()
	delete(s.pos, h)
	delete(s.neg, h)
	s.mu.Unlock()
}

// touch records activity; written to the database by FlushSeen.
func (s *Service) touch(id int64, at time.Time) {
	s.seenMu.Lock()
	s.seen[id] = at
	s.seenMu.Unlock()
}

// FlushSeen persists last-seen times (call periodically and at shutdown).
func (s *Service) FlushSeen(ctx context.Context) error {
	s.seenMu.Lock()
	pending := s.seen
	s.seen = map[int64]time.Time{}
	s.seenMu.Unlock()
	for id, at := range pending {
		if _, err := s.db.ExecContext(ctx, `UPDATE auth_users SET last_seen_at=? WHERE id=?`, at.Unix(), id); err != nil {
			return err
		}
		s.mu.Lock()
		if r := s.users[id]; r != nil {
			r.LastSeen = at
		}
		s.mu.Unlock()
	}
	return nil
}

func (s *Service) upsertValidated(ctx context.Context, a *plextv.Account) (int64, error) {
	now := s.o.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO auth_users (plex_id, uuid, username, title, email, thumb, is_home, restricted, active, origin, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
ON CONFLICT(plex_id) DO UPDATE SET uuid=excluded.uuid, username=excluded.username, title=excluded.title,
	email=excluded.email, thumb=excluded.thumb, active=1, updated_at=excluded.updated_at`,
		a.ID, a.UUID, a.Username, a.DisplayName(), a.Email, a.Thumb, b2i(a.Home), b2i(a.Restricted), originValidated, now, now)
	if err != nil {
		return 0, fmt.Errorf("auth: save user: %w", err)
	}
	if err := s.reload(ctx); err != nil {
		return 0, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byPlex[a.ID], nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// BootstrapOwner creates the owner from config without network access (on
// startup, before plex.tv is reachable) and registers the server token.
func (s *Service) BootstrapOwner(ctx context.Context, p config.Plex) error {
	if p.OwnerPlexID == 0 {
		return nil
	}
	s.mu.RLock()
	id, ok := s.byPlex[p.OwnerPlexID]
	var r *row
	if ok {
		r = s.users[id]
	}
	s.mu.RUnlock()
	acct := plextv.Account{ID: p.OwnerPlexID, Username: p.OwnerUsername, Title: p.OwnerUsername}
	if r != nil {
		// Keep the richer profile already stored.
		acct.UUID, acct.Email, acct.Thumb, acct.Title = r.UUID, r.Email, r.Thumb, r.Title
		if acct.Username == "" {
			acct.Username = r.Username
		}
	}
	return s.SetOwner(ctx, acct, p.Token)
}

// SetOwner implements plextv.UserSync: it makes a the owner and registers
// serverToken as the owner's token.
func (s *Service) SetOwner(ctx context.Context, a plextv.Account, serverToken string) error {
	if a.ID == 0 {
		return errors.New("auth: owner without plex id")
	}
	now := s.o.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE auth_users SET is_owner=0, origin=CASE WHEN origin='owner' THEN 'friend' ELSE origin END,
		active=CASE WHEN is_owner=1 THEN 0 ELSE active END WHERE is_owner=1 AND plex_id<>?`, a.ID); err != nil {
		return err
	}
	title := a.DisplayName()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO auth_users (plex_id, uuid, username, title, email, thumb, is_owner, is_home, active, origin, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, 1, ?, 1, 'owner', ?, ?)
ON CONFLICT(plex_id) DO UPDATE SET
	uuid=CASE WHEN excluded.uuid<>'' THEN excluded.uuid ELSE uuid END,
	username=CASE WHEN excluded.username<>'' THEN excluded.username ELSE username END,
	title=CASE WHEN excluded.title<>'' THEN excluded.title ELSE title END,
	email=CASE WHEN excluded.email<>'' THEN excluded.email ELSE email END,
	thumb=CASE WHEN excluded.thumb<>'' THEN excluded.thumb ELSE thumb END,
	is_owner=1, active=1, origin='owner', updated_at=excluded.updated_at`,
		a.ID, a.UUID, a.Username, title, a.Email, a.Thumb, b2i(a.Home), now, now); err != nil {
		return fmt.Errorf("auth: save owner: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_tokens WHERE source=?`, sourceServer); err != nil {
		return err
	}
	if serverToken != "" {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO auth_tokens (token_hash, user_id, source, created_at)
			SELECT ?, id, ?, ? FROM auth_users WHERE plex_id=?`, HashToken(serverToken), sourceServer, now, a.ID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.mu.Lock()
	s.pos = map[string]cacheEntry{}
	s.neg = map[string]time.Time{}
	s.mu.Unlock()
	return s.reload(ctx)
}

// SyncFriends implements plextv.UserSync: it replaces the synced access
// tokens, upserts friends' profiles (never their Ether settings) and
// deactivates friends who lost access. Users first seen through token
// validation are left to the cache TTL to re-check.
func (s *Service) SyncFriends(ctx context.Context, friends []plextv.Friend) error {
	now := s.o.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ownerLocal int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM auth_users WHERE is_owner=1`).Scan(&ownerLocal); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_tokens WHERE source=?`, sourceFriend); err != nil {
		return err
	}
	keep := map[int64]bool{}
	for _, f := range friends {
		if f.Token == "" {
			continue
		}
		var uid int64
		switch {
		case f.Owned:
			if ownerLocal == 0 {
				continue
			}
			uid = ownerLocal
		case f.PlexID == 0:
			continue
		default:
			secs, _ := json.Marshal(sectionIDs(f.Sections, f.AllLibs))
			if err := tx.QueryRowContext(ctx, `
INSERT INTO auth_users (plex_id, uuid, username, title, email, thumb, is_home, restricted, active, origin, plex_sections, created_at, updated_at)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, 'friend', ?10, ?11, ?11)
ON CONFLICT(plex_id) DO UPDATE SET
	uuid=CASE WHEN ?2<>'' THEN ?2 ELSE uuid END,
	username=CASE WHEN ?3<>'' THEN ?3 ELSE username END,
	title=CASE WHEN ?12<>'' THEN ?12 ELSE title END,
	email=CASE WHEN ?5<>'' THEN ?5 ELSE email END,
	thumb=CASE WHEN ?6<>'' THEN ?6 ELSE thumb END,
	is_home=?7, restricted=?8,
	active=CASE WHEN is_owner=1 THEN 1 ELSE ?9 END,
	origin=CASE WHEN origin='owner' THEN origin ELSE 'friend' END,
	plex_sections=?10, updated_at=?11
RETURNING id`,
				f.PlexID, f.UUID, f.Username, f.DisplayName(), f.Email, f.Thumb, b2i(f.Home), b2i(f.Restricted),
				b2i(!f.Pending), string(secs), now, f.Title).Scan(&uid); err != nil {
				return fmt.Errorf("auth: save friend %d: %w", f.PlexID, err)
			}
			if f.Pending {
				continue // invited but not accepted: no access yet
			}
			keep[uid] = true
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO auth_tokens (token_hash, user_id, source, created_at) VALUES (?, ?, ?, ?)`,
			HashToken(f.Token), uid, sourceFriend, now); err != nil {
			return err
		}
	}
	// Friends no longer in the list lost access.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM auth_users WHERE origin='friend' AND is_owner=0 AND active=1`)
	if err != nil {
		return err
	}
	var gone []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !keep[id] {
			gone = append(gone, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range gone {
		if _, err := tx.ExecContext(ctx, `UPDATE auth_users SET active=0, updated_at=? WHERE id=?`, now, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if len(gone) > 0 {
		s.log.Info("friends lost access", "count", len(gone))
	}
	return s.reload(ctx)
}

// sectionIDs converts plex.tv section keys ("1", "/library/sections/1") to
// ints; nil when everything is shared.
func sectionIDs(keys []string, all bool) []int {
	if all {
		return []int{}
	}
	out := []int{}
	for _, k := range keys {
		k = strings.TrimRight(k, "/")
		if i := strings.LastIndex(k, "/"); i >= 0 {
			k = k[i+1:]
		}
		if n, err := strconv.Atoi(k); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// Users implements core.Auth: active users, owner first, then by name.
func (s *Service) Users(ctx context.Context) ([]core.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]core.User, 0, len(s.users))
	for _, r := range s.users {
		if r.Active {
			out = append(out, r.effective())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsOwner != out[j].IsOwner {
			return out[i].IsOwner
		}
		ti, tj := strings.ToLower(out[i].Title), strings.ToLower(out[j].Title)
		if ti != tj {
			return ti < tj
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// UserInfo is a user plus bookkeeping for the admin UI.
type UserInfo struct {
	core.User
	Active       bool      `json:"active"`
	Origin       string    `json:"origin"`
	Home         bool      `json:"home"`
	Restricted   bool      `json:"restricted"`
	PlexSections []int     `json:"plexSections"`
	LastSeen     time.Time `json:"lastSeen,omitempty"`
}

// AllUsers lists every known user including those who lost access.
func (s *Service) AllUsers(ctx context.Context) ([]UserInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]UserInfo, 0, len(s.users))
	for _, r := range s.users {
		out = append(out, UserInfo{User: r.effective(), Active: r.Active, Origin: r.Origin, Home: r.Home,
			Restricted: r.Restricted, PlexSections: append([]int{}, r.PlexSections...), LastSeen: r.LastSeen})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// User returns one user by local id (active or not).
func (s *Service) User(ctx context.Context, id int64) (*core.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.users[id]
	if r == nil {
		return nil, core.ErrNotFound
	}
	u := r.effective()
	return &u, nil
}

// Owner implements core.Auth.
func (s *Service) Owner(ctx context.Context) (*core.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.ownerPlexID == 0 {
		return nil, core.ErrNotFound
	}
	r := s.users[s.byPlex[s.ownerPlexID]]
	if r == nil {
		return nil, core.ErrNotFound
	}
	u := r.effective()
	return &u, nil
}

// UpdateUser implements core.Auth. It changes only per-user settings:
// stream limit, content rating cap, languages and allowed sections.
// Identity fields come from plex.tv and are ignored.
func (s *Service) UpdateUser(ctx context.Context, u core.User) error {
	if u.StreamLimit < 0 {
		return fmt.Errorf("auth: stream limit must be >= 0")
	}
	norm := func(xs []string) []string {
		out := []string{}
		seen := map[string]bool{}
		for _, x := range xs {
			x = strings.ToLower(strings.TrimSpace(x))
			if x != "" && !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
		return out
	}
	subs, _ := json.Marshal(norm(u.SubtitleLanguages))
	auds, _ := json.Marshal(norm(u.AudioLanguages))
	secs := u.AllowedSections
	if secs == nil {
		secs = []int{}
	}
	secJSON, _ := json.Marshal(secs)
	res, err := s.db.ExecContext(ctx, `UPDATE auth_users SET stream_limit=?, max_content_rating=?, subtitle_languages=?,
		audio_languages=?, allowed_sections=?, updated_at=? WHERE id=?`,
		u.StreamLimit, strings.TrimSpace(u.MaxContentRating), string(subs), string(auds), string(secJSON), s.o.Now().Unix(), u.ID)
	if err != nil {
		return fmt.Errorf("auth: update user: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrNotFound
	}
	return s.reload(ctx)
}
