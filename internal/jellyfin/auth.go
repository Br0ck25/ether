package jellyfin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	store.Register("jellyfin", 1, `
CREATE TABLE jellyfin_credentials (
	user_id    INTEGER PRIMARY KEY,
	secret     TEXT    NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE jellyfin_tokens (
	token_hash  TEXT    PRIMARY KEY,
	user_id     INTEGER NOT NULL,
	device_id   TEXT    NOT NULL DEFAULT '',
	device_name TEXT    NOT NULL DEFAULT '',
	client      TEXT    NOT NULL DEFAULT '',
	version     TEXT    NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	last_seen   INTEGER NOT NULL
);
CREATE INDEX jellyfin_tokens_user ON jellyfin_tokens(user_id);
`)
}

// Session is one signed-in app: a viewer on a device.
type session struct {
	User     *core.User
	Token    string
	DeviceID string
	Device   string
	Client   string
	Version  string
	Remote   bool
	IP       netip.Addr
}

// product is the app's name as it appears on the dashboard.
func (s *session) product() string {
	if s.Client != "" {
		return s.Client
	}
	return "Jellyfin"
}

type ctxKey struct{}

func withSession(ctx context.Context, s *session) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

func sessionFrom(ctx context.Context) *session {
	s, _ := ctx.Value(ctxKey{}).(*session)
	return s
}

// ---------------------------------------------------------------------------
// Access codes

const (
	minCodeLen = 6
	maxCodeLen = 128
)

var errWeakCode = errors.New("an access code needs at least 6 characters")

// hashCode is argon2id with a random salt: "argon2id$salt$hash".
func hashCode(code string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	sum := argon2.IDKey([]byte(code), salt, 2, 32*1024, 2, 32)
	return "argon2id$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(sum)
}

func checkCode(code, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 3 || parts[0] != "argon2id" {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[1])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(code), salt, 2, 32*1024, 2, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

var dummyHash = hashCode("not-a-real-code")

// Store keeps access codes and sign-in tokens. Only hashes persist.
type Store struct {
	db  *sql.DB
	now func() time.Time

	mu    sync.Mutex
	cache map[string]cachedToken
}

type cachedToken struct {
	row     tokenRow
	expires time.Time
}

type tokenRow struct {
	UserID                            int64
	DeviceID, Device, Client, Version string
}

// NewStore wraps a database migrated with store.Migrate.
func NewStore(db *sql.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now, cache: map[string]cachedToken{}}
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SetAccessCode sets or replaces a viewer's code and signs out every device
// that used the old one.
func (s *Store) SetAccessCode(ctx context.Context, userID int64, code string) error {
	if len(code) < minCodeLen {
		return errWeakCode
	}
	if len(code) > maxCodeLen {
		return errors.New("that access code is too long")
	}
	now := s.now().Unix()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO jellyfin_credentials(user_id,secret,updated_at) VALUES(?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET secret=excluded.secret, updated_at=excluded.updated_at`, userID, hashCode(code), now); err != nil {
		return err
	}
	return s.RevokeUser(ctx, userID)
}

// ClearAccessCode removes a viewer's code and signs out their devices.
func (s *Store) ClearAccessCode(ctx context.Context, userID int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM jellyfin_credentials WHERE user_id=?`, userID); err != nil {
		return err
	}
	return s.RevokeUser(ctx, userID)
}

// HasAccessCode reports which of the viewers can sign in.
func (s *Store) HasAccessCode(ctx context.Context, userID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jellyfin_credentials WHERE user_id=?`, userID).Scan(&n)
	return n > 0, err
}

// verify checks a viewer's code. A viewer with no code never verifies.
func (s *Store) verify(ctx context.Context, userID int64, code string) bool {
	var stored string
	if err := s.db.QueryRowContext(ctx, `SELECT secret FROM jellyfin_credentials WHERE user_id=?`, userID).Scan(&stored); err != nil {
		// Spend the hashing time anyway so absence is not observable.
		checkCode(code, dummyHash)
		return false
	}
	return checkCode(code, stored)
}

// issue creates a token for a viewer's device.
func (s *Store) issue(ctx context.Context, userID int64, sess *session) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	now := s.now().Unix()
	// One token per device: signing in again replaces the old one.
	if sess.DeviceID != "" {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM jellyfin_tokens WHERE user_id=? AND device_id=?`, userID, sess.DeviceID); err != nil {
			return "", err
		}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO jellyfin_tokens(token_hash,user_id,device_id,device_name,client,version,created_at,last_seen)
		VALUES(?,?,?,?,?,?,?,?)`, tokenHash(token), userID, sess.DeviceID, sess.Device, sess.Client, sess.Version, now, now)
	return token, err
}

const tokenCacheTTL = time.Minute

func (s *Store) lookup(ctx context.Context, token string) (tokenRow, bool) {
	h := tokenHash(token)
	s.mu.Lock()
	if c, ok := s.cache[h]; ok && s.now().Before(c.expires) {
		s.mu.Unlock()
		return c.row, true
	}
	s.mu.Unlock()
	var row tokenRow
	err := s.db.QueryRowContext(ctx, `SELECT user_id,device_id,device_name,client,version FROM jellyfin_tokens WHERE token_hash=?`, h).
		Scan(&row.UserID, &row.DeviceID, &row.Device, &row.Client, &row.Version)
	if err != nil {
		return tokenRow{}, false
	}
	s.mu.Lock()
	if len(s.cache) > 4096 {
		s.cache = map[string]cachedToken{}
	}
	s.cache[h] = cachedToken{row: row, expires: s.now().Add(tokenCacheTTL)}
	s.mu.Unlock()
	_, _ = s.db.ExecContext(ctx, `UPDATE jellyfin_tokens SET last_seen=? WHERE token_hash=?`, s.now().Unix(), h)
	return row, true
}

// Revoke signs out one token.
func (s *Store) Revoke(ctx context.Context, token string) error {
	h := tokenHash(token)
	s.mu.Lock()
	delete(s.cache, h)
	s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM jellyfin_tokens WHERE token_hash=?`, h)
	return err
}

// RevokeUser signs a viewer out everywhere.
func (s *Store) RevokeUser(ctx context.Context, userID int64) error {
	s.mu.Lock()
	for h, c := range s.cache {
		if c.row.UserID == userID {
			delete(s.cache, h)
		}
	}
	s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM jellyfin_tokens WHERE user_id=?`, userID)
	return err
}

// ---------------------------------------------------------------------------
// Sign-in throttle

// throttle slows password guessing: five failures for one user name from
// one address lock that pair for ten minutes.
type throttle struct {
	mu   sync.Mutex
	now  func() time.Time
	fail map[string]*failure
}

type failure struct {
	n     int
	until time.Time
	last  time.Time
}

const (
	// A user name from one address locks after maxFailures; an address that
	// has failed maxIPFailures times across any names locks too, so cycling
	// through names does not get around the first limit.
	maxFailures   = 5
	maxIPFailures = 20
	lockout       = 10 * time.Minute
)

func newThrottle(now func() time.Time) *throttle {
	return &throttle{now: now, fail: map[string]*failure{}}
}

func (t *throttle) blocked(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.fail[key]
	return f != nil && t.now().Before(f.until)
}

func (t *throttle) record(key string, ok bool, limit int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ok {
		delete(t.fail, key)
		return
	}
	now := t.now()
	if len(t.fail) > 4096 {
		for k, f := range t.fail {
			if now.Sub(f.last) > lockout {
				delete(t.fail, k)
			}
		}
	}
	f := t.fail[key]
	if f == nil || now.Sub(f.last) > lockout {
		f = &failure{}
		t.fail[key] = f
	}
	f.n++
	f.last = now
	if f.n >= limit {
		f.until = now.Add(lockout)
		f.n = 0
	}
}

// ---------------------------------------------------------------------------
// Request authentication

var authPair = regexp.MustCompile(`(\w+)="([^"]*)"`)

// authInfo is what an app says about itself and its token. Apps send it in
// "Authorization: MediaBrowser ..." (Jellyfin), "X-Emby-Authorization" (both),
// "X-Emby-Token" / "X-MediaBrowser-Token", or the api_key query parameter.
type authInfo struct {
	Token, DeviceID, Device, Client, Version string
}

func parseAuth(r *http.Request, p params) authInfo {
	var a authInfo
	for _, h := range []string{"X-Emby-Authorization", "Authorization", "X-MediaBrowser-Authorization"} {
		v := r.Header.Get(h)
		if v == "" {
			continue
		}
		if i := strings.IndexByte(v, ' '); i > 0 {
			scheme := strings.ToLower(v[:i])
			if scheme != "mediabrowser" && scheme != "emby" {
				continue
			}
			v = v[i+1:]
		}
		for _, m := range authPair.FindAllStringSubmatch(v, -1) {
			val := strings.TrimSpace(m[2])
			switch strings.ToLower(m[1]) {
			case "token":
				a.Token = firstNonEmpty(a.Token, val)
			case "deviceid":
				a.DeviceID = firstNonEmpty(a.DeviceID, val)
			case "device":
				a.Device = firstNonEmpty(a.Device, val)
			case "client":
				a.Client = firstNonEmpty(a.Client, val)
			case "version":
				a.Version = firstNonEmpty(a.Version, val)
			}
		}
	}
	a.Token = firstNonEmpty(a.Token, r.Header.Get("X-Emby-Token"), r.Header.Get("X-MediaBrowser-Token"),
		p.get("api_key"), p.get("apikey"), p.get("x-emby-token"))
	a.DeviceID = firstNonEmpty(a.DeviceID, p.get("deviceid"), r.Header.Get("X-Emby-Device-Id"))
	a.Device = firstNonEmpty(a.Device, p.get("device"), r.Header.Get("X-Emby-Device-Name"))
	a.Client = firstNonEmpty(a.Client, p.get("client"), r.Header.Get("X-Emby-Client"))
	a.Version = firstNonEmpty(a.Version, p.get("version"))
	return a
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func clientIP(r *http.Request) netip.Addr {
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}
	}
	ip = ip.Unmap()
	if ip.IsLoopback() {
		// A local reverse proxy: trust the nearest non-local hop it reports.
		hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			fip, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				return netip.Addr{}
			}
			if fip = fip.Unmap(); !isLocal(fip) {
				return fip
			}
		}
	}
	return ip
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func isLocal(ip netip.Addr) bool {
	return ip.IsValid() && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip))
}

func addrOf(u *net.UDPAddr) netip.Addr {
	a, _ := netip.AddrFromSlice(u.IP)
	return a.Unmap()
}
