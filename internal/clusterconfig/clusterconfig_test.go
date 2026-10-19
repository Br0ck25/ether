package clusterconfig

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
)

// node opens a config store in its own directory; nodes made from the same
// seed directory share its credential key, as a join leaves them.
func node(t *testing.T, keyFrom string) (*config.Store, string) {
	t.Helper()
	dir := t.TempDir()
	if keyFrom != "" {
		key, err := config.ReadCredentialKey(keyFrom)
		if err != nil {
			t.Fatal(err)
		}
		if err := config.InstallCredentialKey(dir, key); err != nil {
			t.Fatal(err)
		}
	}
	s, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close) // runs before the directory is removed
	return s, dir
}

type bus struct {
	mu   sync.Mutex
	subs map[string][]func([]byte)
}

func (b *bus) Publish(_ context.Context, topic string, p []byte) error {
	b.mu.Lock()
	fns := append([]func([]byte){}, b.subs[topic]...)
	b.mu.Unlock()
	for _, f := range fns {
		f(p)
	}
	return nil
}

func (b *bus) Subscribe(topic string, fn func([]byte)) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs == nil {
		b.subs = map[string][]func([]byte){}
	}
	b.subs[topic] = append(b.subs[topic], fn)
	return func() {}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSharedBackendVersioning(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	s := NewShared(db)
	if v, err := s.SharedVersion(ctx); err != nil || v != 0 {
		t.Fatal(v, err)
	}
	if _, found, err := s.LoadShared(ctx); err != nil || found {
		t.Fatal(found, err)
	}
	if v, err := s.SaveShared(ctx, config.SharedDoc{Doc: "{}", KeyFingerprint: "fp"}, 0); err != nil || v != 1 {
		t.Fatal(v, err)
	}
	if _, err := s.SaveShared(ctx, config.SharedDoc{Doc: "{}", KeyFingerprint: "fp"}, 0); !errors.Is(err, config.ErrSharedConflict) {
		t.Fatalf("second seed: %v", err)
	}
	if v, err := s.SaveShared(ctx, config.SharedDoc{Doc: `{"a":1}`, KeyFingerprint: "fp"}, 1); err != nil || v != 2 {
		t.Fatal(v, err)
	}
	if _, err := s.SaveShared(ctx, config.SharedDoc{Doc: "{}", KeyFingerprint: "fp"}, 1); !errors.Is(err, config.ErrSharedConflict) {
		t.Fatalf("stale write: %v", err)
	}
	d, found, err := s.LoadShared(ctx)
	if err != nil || !found || d.Version != 2 || d.Doc != `{"a":1}` || d.KeyFingerprint != "fp" {
		t.Fatal(d, found, err)
	}
}

// TestTwoNodesShareSettings runs two config stores against one database,
// as two cluster nodes would.
func TestTwoNodesShareSettings(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := store.OpenTest(t)
	b := &bus{}
	a, dirA := node(t, "")
	if err := a.Update(func(c *config.Config) error {
		c.Plex.Token = "owner-token"
		c.Server.Port = 32401
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Attach(ctx, a, db, config.SharedOptions{Notifier: b, PollInterval: -1}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT doc FROM clusterconfig_shared`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "owner-token") {
		t.Fatal("secret stored unsealed")
	}

	nb, _ := node(t, dirA)
	if err := Attach(ctx, nb, db, config.SharedOptions{Notifier: b, PollInterval: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if nb.Get().Server.MachineIdentifier != a.Get().Server.MachineIdentifier || nb.Get().Plex.Token != "owner-token" {
		t.Fatal("node B did not adopt the shared identity")
	}
	if nb.Get().Server.Port == 32401 {
		t.Fatal("node B took node A's local port")
	}

	// Notify path.
	if err := a.Update(func(c *config.Config) error { c.Server.FriendlyName = "Shared name"; return nil }); err != nil {
		t.Fatal(err)
	}
	eventually(t, "notification", func() bool { return nb.Get().Server.FriendlyName == "Shared name" })

	// Poll path (no bus).
	nc, _ := node(t, dirA)
	if err := Attach(ctx, nc, db, config.SharedOptions{PollInterval: 20 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if err := a.Update(func(c *config.Config) error { c.Catalog.Region = "CA"; return nil }); err != nil {
		t.Fatal(err)
	}
	eventually(t, "poll", func() bool { return nc.Get().Catalog.Region == "CA" })

	// Version conflict: nc is current, a falls behind after nb writes.
	if err := nc.RefreshShared(ctx); err != nil {
		t.Fatal(err)
	}
	b2 := &bus{} // detach notifications so a stays stale
	a.SetNotifier(b2)
	nb.SetNotifier(b2)
	if err := nb.Update(func(c *config.Config) error {
		c.Indexers = append(c.Indexers, config.Indexer{ID: "b", APIKey: "key-b"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stale := a.SharedVersion()
	if err := a.Update(func(c *config.Config) error { c.Catalog.Language = "nl-NL"; return nil }); err != nil {
		t.Fatal(err)
	}
	got := a.Get()
	if got.Catalog.Language != "nl-NL" || len(got.Indexers) != 1 || got.Indexers[0].ID != "b" {
		t.Fatalf("conflict merge lost a change: %q %+v", got.Catalog.Language, got.Indexers)
	}
	if a.SharedVersion() != stale+2 {
		t.Fatalf("version %d, want %d", a.SharedVersion(), stale+2)
	}
	if err := nb.RefreshShared(ctx); err != nil {
		t.Fatal(err)
	}
	if nb.Get().Catalog.Language != "nl-NL" {
		t.Fatal("node B did not converge")
	}
}

func TestKeyMismatchRefusesToStart(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	a, _ := node(t, "")
	if err := Attach(ctx, a, db, config.SharedOptions{PollInterval: -1}); err != nil {
		t.Fatal(err)
	}
	stranger, _ := node(t, "")
	err := Attach(ctx, stranger, db, config.SharedOptions{PollInterval: -1})
	if !errors.Is(err, config.ErrKeyMismatch) {
		t.Fatalf("err = %v", err)
	}
}

func TestJoinTokenLifecycle(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	key := make([]byte, 32)
	key[0] = 7
	now := time.Unix(1_800_000_000, 0)
	tok, info, err := CreateJoinToken(ctx, db, "postgres://u:p@db/ether", key, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatal(info)
	}
	s := tok.String()
	if !strings.HasPrefix(s, JoinTokenPrefix) {
		t.Fatal(s)
	}
	parsed, err := ParseJoinToken(" " + s + "\n")
	if err != nil || parsed.DatabaseURL != tok.DatabaseURL || string(parsed.Key) != string(key) || parsed.Secret != tok.Secret {
		t.Fatal(parsed, err)
	}
	var stored string
	if err := db.QueryRowContext(ctx, `SELECT secret_hash FROM clusterconfig_join_tokens WHERE id=?`, tok.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, tok.Secret) {
		t.Fatal("token secret stored in the clear")
	}

	// Wrong secret.
	bad := parsed
	bad.Secret = "nope"
	if err := ConsumeJoinToken(ctx, db, bad, "n", now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("wrong secret: %v", err)
	}
	// Concurrent consumers: exactly one wins.
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, used := 0, 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := ConsumeJoinToken(ctx, db, parsed, "node", now.Add(time.Minute))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrTokenUsed):
				used++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 || used != 3 {
		t.Fatalf("wins=%d used=%d", wins, used)
	}
	// Replay.
	if err := ConsumeJoinToken(ctx, db, parsed, "again", now.Add(2*time.Minute)); !errors.Is(err, ErrTokenUsed) {
		t.Fatalf("replay: %v", err)
	}
	// Expiry.
	tok2, _, err := CreateJoinToken(ctx, db, "postgres://db/ether", key, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ConsumeJoinToken(ctx, db, tok2, "late", now.Add(time.Hour+time.Second)); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expired: %v", err)
	}
	// Revoke.
	tok3, _, _ := CreateJoinToken(ctx, db, "postgres://db/ether", key, time.Hour, now)
	if ok, err := RevokeJoinToken(ctx, db, tok3.ID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := ConsumeJoinToken(ctx, db, tok3, "x", now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("revoked: %v", err)
	}
	list, err := ListJoinTokens(ctx, db)
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}

	for _, s := range []string{"", "ether-join-v1:!!!", "ether-join-v2:abc", JoinTokenPrefix + "e30"} {
		if _, err := ParseJoinToken(s); !errors.Is(err, ErrTokenInvalid) {
			t.Errorf("ParseJoinToken(%q) = %v", s, err)
		}
	}
}

func TestSecretsSealedAndVersioned(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	cfg, _ := node(t, "")
	s := NewSecrets(db, cfg)
	if data, v, err := s.Get(ctx, "certs/plexdirect"); err != nil || v != 0 || data != nil {
		t.Fatal(data, v, err)
	}
	if v, err := s.Put(ctx, "certs/plexdirect", []byte("PRIVATE KEY")); err != nil || v != 1 {
		t.Fatal(v, err)
	}
	if v, err := s.Put(ctx, "certs/plexdirect", []byte("PRIVATE KEY 2")); err != nil || v != 2 {
		t.Fatal(v, err)
	}
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT value FROM clusterconfig_secrets WHERE name=?`, "certs/plexdirect").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "PRIVATE") {
		t.Fatal("blob stored unsealed")
	}
	data, v, err := s.Get(ctx, "certs/plexdirect")
	if err != nil || v != 2 || string(data) != "PRIVATE KEY 2" {
		t.Fatal(string(data), v, err)
	}
	if v, _ := s.Version(ctx, "certs/plexdirect"); v != 2 {
		t.Fatal(v)
	}
	other, _ := node(t, "")
	if _, _, err := NewSecrets(db, other).Get(ctx, "certs/plexdirect"); err == nil {
		t.Fatal("blob opened with another key")
	}
}

func TestHandler(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	cfg, _ := node(t, "")

	off := Handler(HandlerOptions{DB: db, Config: cfg})
	rec := httptest.NewRecorder()
	off.ServeHTTP(rec, httptest.NewRequest("POST", "/join-tokens", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("standalone POST = %d %s", rec.Code, rec.Body)
	}

	if err := Attach(ctx, cfg, db, config.SharedOptions{PollInterval: -1}); err != nil {
		t.Fatal(err)
	}
	h := Handler(HandlerOptions{DB: db, Clustered: true, Config: cfg, DatabaseURL: func() string { return "postgres://ether:pw@db.lan/ether" }})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/join-tokens", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("before setup = %d", rec.Code)
	}
	if err := cfg.Update(func(c *config.Config) error { c.SetupComplete = true; return nil }); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/join-tokens", strings.NewReader(`{"ttlMinutes":10}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var created struct {
		Token, ID, Warning string
		ExpiresAt          time.Time
	}
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	tok, err := ParseJoinToken(created.Token)
	if err != nil || tok.DatabaseURL != "postgres://ether:pw@db.lan/ether" || config.KeyFingerprint(tok.Key) != cfg.KeyFingerprint() || created.Warning == "" {
		t.Fatal(tok, err)
	}
	if d := time.Until(created.ExpiresAt); d > 11*time.Minute || d < 8*time.Minute {
		t.Fatalf("ttl %v", d)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), created.ID) || strings.Contains(rec.Body.String(), tok.Secret) {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("DELETE", "/join-tokens/"+created.ID, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d", rec.Code)
	}
}
