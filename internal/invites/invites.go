// Package invites implements expiring invitations with account-bound redemption.
package invites

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/safety"
	"github.com/braedonsaunders/ether/internal/store"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

func init() {
	store.Register("invites", 1, `CREATE TABLE invites_links(id INTEGER PRIMARY KEY, token_hash TEXT UNIQUE NOT NULL,label TEXT NOT NULL,expires INTEGER NOT NULL,max_uses INTEGER NOT NULL,uses INTEGER NOT NULL DEFAULT 0,policy TEXT NOT NULL,sections TEXT NOT NULL,revoked INTEGER NOT NULL DEFAULT 0); CREATE TABLE invites_claims(invite_id INTEGER NOT NULL,plex_id INTEGER NOT NULL,state TEXT NOT NULL,PRIMARY KEY(invite_id,plex_id));`)
	// Pending plex.tv PIN sign-ins live in the database so a sign-in
	// started on one cluster node can be polled on another. The handle the
	// browser holds is stored hashed.
	store.Register("invites", 2, `CREATE TABLE invites_pins(handle_hash TEXT PRIMARY KEY, invite_id INTEGER NOT NULL, pin_id TEXT NOT NULL, expires INTEGER NOT NULL, plex_id INTEGER NOT NULL DEFAULT 0, email TEXT NOT NULL DEFAULT '');
CREATE INDEX invites_pins_expires ON invites_pins(expires);`)
}

type Invite struct {
	ID          int64         `json:"id"`
	Label       string        `json:"label"`
	Expires     time.Time     `json:"expires"`
	MaxUses     int           `json:"maxUses"`
	Uses        int           `json:"uses"`
	Policy      safety.Policy `json:"policy"`
	Sections    []int         `json:"sections"`
	Revoked     bool          `json:"revoked"`
	StreamLimit int           `json:"streamLimit"`
}
type Account struct {
	PlexID int64
	Email  string
}
type Deps struct {
	Start func(context.Context) (id, code, authURL string, err error)
	Poll  func(context.Context, string) (*Account, error)
	Share func(context.Context, Account, Invite) error
	Apply func(context.Context, Account, Invite) error
	Now   func() time.Time
}
type pending struct {
	inviteID int64
	pinID    string
	expires  time.Time
	account  *Account
}

// pinTTL is how long a started sign-in may be polled.
const pinTTL = 15 * time.Minute

type Service struct {
	db       *sql.DB
	d        Deps
	mu       sync.Mutex
	redeemMu sync.Mutex // in-process only; the claim key and conditional use count keep redemption correct across nodes
	rate     map[string]bucket
}
type bucket struct {
	count int
	until time.Time
}

func New(db *sql.DB, d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{db: db, d: d, rate: map[string]bucket{}}
}
func secret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func hash(v string) string { x := sha256.Sum256([]byte(v)); return hex.EncodeToString(x[:]) }
func (s *Service) Create(ctx context.Context, in Invite) (Invite, string, error) {
	if in.MaxUses < 1 || in.MaxUses > 1000 || !in.Expires.After(s.d.Now()) || in.Expires.After(s.d.Now().Add(90*24*time.Hour)) || len(in.Sections) == 0 {
		return in, "", fmt.Errorf("expiry (up to 90 days), uses (1..1000), and sections are required")
	}
	for _, v := range in.Sections {
		if v < 1 || v > 2 {
			return in, "", fmt.Errorf("invalid library section")
		}
	}
	// Redemption stages the policy (with the stream limit) through
	// safety.Put, which rejects invalid values: an invite carrying one could
	// never be redeemed, failing every viewer at the last step.
	staged := in.Policy
	staged.StreamLimit = in.StreamLimit
	if err := staged.Validate(); err != nil {
		return in, "", fmt.Errorf("invalid policy: %w", err)
	}
	token, err := secret()
	if err != nil {
		return in, "", err
	}
	policy, _ := json.Marshal(struct {
		Policy      safety.Policy `json:"policy"`
		StreamLimit int           `json:"streamLimit"`
	}{in.Policy, in.StreamLimit})
	secs, _ := json.Marshal(in.Sections)
	err = s.db.QueryRowContext(ctx, `INSERT INTO invites_links(token_hash,label,expires,max_uses,policy,sections) VALUES(?,?,?,?,?,?) RETURNING id`,
		hash(token), in.Label, in.Expires.Unix(), in.MaxUses, string(policy), string(secs)).Scan(&in.ID)
	return in, token, err
}
func scan(row interface{ Scan(...any) error }) (Invite, error) {
	var in Invite
	var exp int64
	var p, secs string
	err := row.Scan(&in.ID, &in.Label, &exp, &in.MaxUses, &in.Uses, &p, &secs, &in.Revoked)
	if err != nil {
		return in, err
	}
	in.Expires = time.Unix(exp, 0)
	var x struct {
		Policy      safety.Policy `json:"policy"`
		StreamLimit int           `json:"streamLimit"`
	}
	if err = json.Unmarshal([]byte(p), &x); err != nil {
		return in, err
	}
	in.Policy = x.Policy
	in.StreamLimit = x.StreamLimit
	err = json.Unmarshal([]byte(secs), &in.Sections)
	return in, err
}

const columns = `id,label,expires,max_uses,uses,policy,sections,revoked`

func (s *Service) lookup(ctx context.Context, token string) (Invite, error) {
	in, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM invites_links WHERE token_hash=?`, hash(token)))
	if err != nil || in.Revoked || !in.Expires.After(s.d.Now()) {
		return in, core.ErrNotFound
	}
	return in, nil
}
func (s *Service) Redeem(ctx context.Context, in Invite, a Account) error {
	s.redeemMu.Lock()
	defer s.redeemMu.Unlock()
	if a.PlexID <= 0 || a.Email == "" {
		return core.ErrUnauthorized
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Claim first: the claim's primary key decides between concurrent
	// redemptions by the same account on different nodes, and the
	// conditional use increment below decides between different accounts.
	res, err := tx.ExecContext(ctx, `INSERT INTO invites_claims(invite_id,plex_id,state) VALUES(?,?,'pending') ON CONFLICT(invite_id,plex_id) DO NOTHING`, in.ID, a.PlexID)
	if err != nil {
		tx.Rollback()
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		tx.Rollback()
		if err != nil {
			return err
		}
		var state string
		if err := s.db.QueryRowContext(ctx, `SELECT state FROM invites_claims WHERE invite_id=? AND plex_id=?`, in.ID, a.PlexID).Scan(&state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("invitation is being processed")
			}
			return err
		}
		if state == "complete" {
			return nil
		}
		if state == "shared" {
			return s.apply(ctx, in, a)
		}
		return fmt.Errorf("invitation is being processed")
	}
	res, err = tx.ExecContext(ctx, `UPDATE invites_links SET uses=uses+1 WHERE id=? AND revoked=0 AND expires>? AND uses<max_uses`, in.ID, s.d.Now().Unix())
	if err != nil {
		tx.Rollback()
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		tx.Rollback()
		return core.ErrNotFound
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// Persist reservation before sharing. A process crash leaves it pending for
	// owner review rather than giving an extra person access without consuming use.
	if s.d.Share == nil {
		s.release(ctx, in.ID, a.PlexID)
		return fmt.Errorf("Plex sharing unavailable")
	}
	if err = s.d.Share(ctx, a, in); err != nil {
		s.release(ctx, in.ID, a.PlexID)
		return err
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE invites_claims SET state='shared' WHERE invite_id=? AND plex_id=?`, in.ID, a.PlexID); err != nil {
		return err
	}
	return s.apply(ctx, in, a)
}
func (s *Service) apply(ctx context.Context, in Invite, a Account) error {
	if s.d.Apply != nil {
		if err := s.d.Apply(ctx, a, in); err != nil {
			return fmt.Errorf("shared; policy application needs retry: %w", err)
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE invites_claims SET state='complete' WHERE invite_id=? AND plex_id=?`, in.ID, a.PlexID)
	return err
}
func (s *Service) release(ctx context.Context, id, user int64) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM invites_claims WHERE invite_id=? AND plex_id=? AND state='pending'`, id, user)
	if err != nil {
		return
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return // already released elsewhere: do not return the use twice
	}
	if _, err = tx.ExecContext(ctx, `UPDATE invites_links SET uses=CASE WHEN uses>0 THEN uses-1 ELSE 0 END WHERE id=?`, id); err == nil {
		_ = tx.Commit()
	}
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Service) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.db.QueryContext(r.Context(), `SELECT `+columns+` FROM invites_links ORDER BY id DESC`)
		if err != nil {
			http.Error(w, "could not load invites", 500)
			return
		}
		defer rows.Close()
		out := []Invite{}
		for rows.Next() {
			in, err := scan(rows)
			if err != nil {
				http.Error(w, "could not decode invite", 500)
				return
			}
			out = append(out, in)
		}
		reply(w, 200, out)
	})
	m.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		var in Invite
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in) != nil {
			http.Error(w, "invalid invite", 400)
			return
		}
		in, token, err := s.Create(r.Context(), in)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		reply(w, 201, map[string]any{"invite": in, "path": "/invite/" + token})
	})
	m.HandleFunc("DELETE /{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		_, err := s.db.ExecContext(r.Context(), `UPDATE invites_links SET revoked=1 WHERE id=?`, id)
		if err != nil {
			http.Error(w, "could not revoke", 500)
			return
		}
		reply(w, 200, map[string]bool{"revoked": true})
	})
	return m
}
func (s *Service) limited(peer string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.d.Now()
	for k, v := range s.rate {
		if now.After(v.until) {
			delete(s.rate, k)
		}
	}
	b := s.rate[peer]
	if b.until.IsZero() {
		b.until = now.Add(time.Minute)
	}
	b.count++
	s.rate[peer] = b
	return b.count > 30 || len(s.rate) > 4096
}
func (s *Service) PublicHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /{token}", s.landing)
	m.HandleFunc("POST /{token}/pin", s.start)
	m.HandleFunc("POST /{token}/poll", s.poll)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		peer, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			peer = r.RemoteAddr
		}
		if s.limited(peer) {
			http.Error(w, "try again shortly", 429)
			return
		}
		m.ServeHTTP(w, r)
	})
}
func (s *Service) start(w http.ResponseWriter, r *http.Request) {
	in, err := s.lookup(r.Context(), r.PathValue("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if in.Uses >= in.MaxUses {
		http.NotFound(w, r)
		return
	}
	if s.d.Start == nil {
		http.Error(w, "sign in unavailable", 503)
		return
	}
	id, code, url, err := s.d.Start(r.Context())
	if err != nil {
		http.Error(w, "could not contact Plex", 502)
		return
	}
	h, err := secret()
	if err != nil {
		http.Error(w, "could not start sign in", 500)
		return
	}
	if err := s.savePending(r.Context(), h, pending{inviteID: in.ID, pinID: id, expires: s.d.Now().Add(pinTTL)}); err != nil {
		http.Error(w, "could not start sign in", 500)
		return
	}
	reply(w, 200, map[string]string{"handle": h, "code": code, "authUrl": url})
}
func (s *Service) poll(w http.ResponseWriter, r *http.Request) {
	in, err := s.lookup(r.Context(), r.PathValue("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Handle string `json:"handle"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
		http.Error(w, "invalid sign in", 400)
		return
	}
	p, ok, err := s.loadPending(r.Context(), body.Handle)
	if err != nil {
		http.Error(w, "could not check sign in", 500)
		return
	}
	if !ok || p.inviteID != in.ID || s.d.Now().After(p.expires) {
		http.Error(w, "sign in expired", 404)
		return
	}
	a := p.account
	if a == nil {
		a, err = s.d.Poll(r.Context(), p.pinID)
	}
	if err != nil {
		http.Error(w, "could not check Plex sign in", 502)
		return
	}
	if a == nil {
		reply(w, 200, map[string]bool{"done": false})
		return
	}
	if p.account == nil {
		// plex.tv answers a PIN once: keep the account for retries on any node.
		if err := s.saveAccount(r.Context(), body.Handle, *a); err != nil {
			http.Error(w, "could not save sign in", 500)
			return
		}
	}
	if err = s.Redeem(r.Context(), in, *a); err != nil {
		switch {
		case errors.Is(err, core.ErrNotFound):
			// The last use went to someone else (or the invite was revoked
			// or expired) while this viewer signed in. The landing page
			// retries 409s forever, so answer with a final status.
			_, _ = s.db.ExecContext(r.Context(), `DELETE FROM invites_pins WHERE handle_hash=?`, hash(body.Handle))
			http.Error(w, "This invitation is no longer available. Ask the owner for a new one.", http.StatusGone)
		case errors.Is(err, core.ErrUnauthorized):
			http.Error(w, "This Plex account cannot accept the invitation.", http.StatusForbidden)
		default:
			http.Error(w, err.Error(), 409)
		}
		return
	}
	_, _ = s.db.ExecContext(r.Context(), `DELETE FROM invites_pins WHERE handle_hash=?`, hash(body.Handle))
	reply(w, 200, map[string]bool{"done": true})
}

// savePending records a started sign-in, dropping expired ones.
func (s *Service) savePending(ctx context.Context, handle string, p pending) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM invites_pins WHERE expires<?`, s.d.Now().Unix()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO invites_pins(handle_hash,invite_id,pin_id,expires) VALUES(?,?,?,?)`,
		hash(handle), p.inviteID, p.pinID, p.expires.Unix())
	return err
}

// loadPending reads a started sign-in by its handle.
func (s *Service) loadPending(ctx context.Context, handle string) (pending, bool, error) {
	var p pending
	var exp, plexID int64
	var email string
	err := s.db.QueryRowContext(ctx, `SELECT invite_id,pin_id,expires,plex_id,email FROM invites_pins WHERE handle_hash=?`, hash(handle)).
		Scan(&p.inviteID, &p.pinID, &exp, &plexID, &email)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	p.expires = time.Unix(exp, 0)
	if plexID > 0 {
		p.account = &Account{PlexID: plexID, Email: email}
	}
	return p, true, nil
}

// saveAccount records the plex.tv account a sign-in resolved to.
func (s *Service) saveAccount(ctx context.Context, handle string, a Account) error {
	_, err := s.db.ExecContext(ctx, `UPDATE invites_pins SET plex_id=?, email=? WHERE handle_hash=?`, a.PlexID, a.Email, hash(handle))
	return err
}
