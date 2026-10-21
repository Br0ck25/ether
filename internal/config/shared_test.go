package config

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// memShared is an in-memory SharedBackend.
type memShared struct {
	mu       sync.Mutex
	doc      SharedDoc
	found    bool
	saves    int
	conflict func() // runs inside the next SaveShared, before the check
}

func (m *memShared) LoadShared(context.Context) (SharedDoc, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.doc, m.found, nil
}

func (m *memShared) SaveShared(_ context.Context, d SharedDoc, expect int64) (int64, error) {
	m.mu.Lock()
	hook := m.conflict
	m.conflict = nil
	m.mu.Unlock()
	if hook != nil {
		hook()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if (!m.found && expect != 0) || (m.found && m.doc.Version != expect) {
		return 0, ErrSharedConflict
	}
	d.Version = expect + 1
	m.doc, m.found = d, true
	m.saves++
	return d.Version, nil
}

func (m *memShared) SharedVersion(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.doc.Version, nil
}

// memBus is an in-process Notifier.
type memBus struct {
	mu   sync.Mutex
	subs map[string][]func([]byte)
}

func (b *memBus) Publish(_ context.Context, topic string, payload []byte) error {
	b.mu.Lock()
	fns := append([]func([]byte){}, b.subs[topic]...)
	b.mu.Unlock()
	for _, f := range fns {
		f(payload)
	}
	return nil
}

func (b *memBus) Subscribe(topic string, fn func([]byte)) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs == nil {
		b.subs = map[string][]func([]byte){}
	}
	b.subs[topic] = append(b.subs[topic], fn)
	return func() {}
}

// clusterNodes opens two data directories sharing one credential key, as
// a join would leave them.
func clusterNodes(t *testing.T) (dirA, dirB string) {
	t.Helper()
	dirA, dirB = t.TempDir(), t.TempDir()
	if _, err := Open(dirA); err != nil {
		t.Fatal(err)
	}
	key, err := ReadCredentialKey(dirA)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallCredentialKey(dirB, key); err != nil {
		t.Fatal(err)
	}
	return dirA, dirB
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSharedSeedOverlayAndPropagation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dirA, dirB := clusterNodes(t)
	backend := &memShared{}
	bus := &memBus{}

	a, err := Open(dirA)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Update(func(c *Config) error {
		c.Server.FriendlyName = "Living room"
		c.Plex.Token = "owner-secret-token"
		c.Server.Port = 32401
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	machine := a.Get().Server.MachineIdentifier
	if err := a.AttachShared(ctx, backend, SharedOptions{Notifier: bus, PollInterval: -1}); err != nil {
		t.Fatal(err)
	}
	if !a.Clustered() || backend.doc.Version != 1 {
		t.Fatalf("seed: clustered=%v version=%d", a.Clustered(), backend.doc.Version)
	}
	if strings.Contains(backend.doc.Doc, "owner-secret-token") || !strings.Contains(backend.doc.Doc, sealedPrefix) {
		t.Fatal("shared document holds an unsealed secret")
	}
	if strings.Contains(backend.doc.Doc, "32401") {
		t.Fatal("node-local port stored in the shared document")
	}
	if backend.doc.KeyFingerprint != a.KeyFingerprint() {
		t.Fatal("seed did not record the key fingerprint")
	}

	b, err := Open(dirB)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Update(func(c *Config) error { c.Server.Port = 32402; return nil }); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []Config
	b.Subscribe(func(c Config) { mu.Lock(); seen = append(seen, c); mu.Unlock() })
	if err := b.AttachShared(ctx, backend, SharedOptions{Notifier: bus, PollInterval: time.Hour}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	got := b.Get()
	if got.Server.MachineIdentifier != machine || got.Plex.Token != "owner-secret-token" || got.Server.FriendlyName != "Living room" {
		t.Fatalf("node B did not adopt shared identity: %+v", got.Server)
	}
	if got.Server.Port != 32402 {
		t.Fatalf("node B lost its local port: %d", got.Server.Port)
	}
	if backend.saves != 1 {
		t.Fatalf("attaching a second node wrote the shared document (%d saves)", backend.saves)
	}

	// A shared change on A reaches B through the bus.
	if err := a.Update(func(c *Config) error { c.Server.FriendlyName = "Den"; return nil }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B to see A's change", func() bool { return b.Get().Server.FriendlyName == "Den" })
	waitFor(t, "B's subscriber", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) > 0 && seen[len(seen)-1].Server.FriendlyName == "Den"
	})
	if b.Get().Server.Port != 32402 || a.Get().Server.Port != 32401 {
		t.Fatal("a shared update changed node-local settings")
	}

	// A local change on A is not shared.
	before := backend.saves
	if err := a.Update(func(c *Config) error { c.Cache.BudgetBytes = 123; return nil }); err != nil {
		t.Fatal(err)
	}
	if backend.saves != before {
		t.Fatal("a node-local update wrote the shared document")
	}

	// The local file keeps working settings for the next boot: reopening
	// and re-attaching gives the same view.
	a2, err := Open(dirA)
	if err != nil {
		t.Fatal(err)
	}
	if a2.Get().Cache.BudgetBytes != 123 {
		t.Fatal("local change not persisted")
	}
	if err := a2.AttachShared(ctx, backend, SharedOptions{PollInterval: -1}); err != nil {
		t.Fatal(err)
	}
	if a2.Get().Server.FriendlyName != "Den" || a2.Get().Server.Port != 32401 {
		t.Fatalf("reboot view = %+v", a2.Get().Server)
	}
}

func TestSharedPollFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dirA, dirB := clusterNodes(t)
	backend := &memShared{}
	a, _ := Open(dirA)
	if err := a.AttachShared(ctx, backend, SharedOptions{PollInterval: -1}); err != nil {
		t.Fatal(err)
	}
	b, _ := Open(dirB)
	if err := b.AttachShared(ctx, backend, SharedOptions{PollInterval: 10 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	if err := a.Update(func(c *Config) error { c.Catalog.Language = "fr-FR"; return nil }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "poll to pick up the change", func() bool { return b.Get().Catalog.Language == "fr-FR" })
}

func TestSharedConflictReappliesUpdate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dirA, dirB := clusterNodes(t)
	backend := &memShared{}
	a, _ := Open(dirA)
	b, _ := Open(dirB)
	if err := a.AttachShared(ctx, backend, SharedOptions{PollInterval: -1}); err != nil {
		t.Fatal(err)
	}
	if err := b.AttachShared(ctx, backend, SharedOptions{PollInterval: -1}); err != nil {
		t.Fatal(err)
	}
	// B changes the indexers while A, holding an older version, edits the
	// catalogue language: A's write conflicts, rebases and keeps both.
	backend.conflict = func() {
		if err := b.Update(func(c *Config) error {
			c.Indexers = append(c.Indexers, Indexer{ID: "from-b", APIKey: "kb"})
			return nil
		}); err != nil {
			t.Error(err)
		}
	}
	calls := 0
	if err := a.Update(func(c *Config) error {
		calls++
		c.Catalog.Language = "de-DE"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("update fn ran %d times, want 2 (conflict retry)", calls)
	}
	got := a.Get()
	if got.Catalog.Language != "de-DE" || len(got.Indexers) != 1 || got.Indexers[0].APIKey != "kb" {
		t.Fatalf("merged result = %q %+v", got.Catalog.Language, got.Indexers)
	}
	if backend.doc.Version != 3 {
		t.Fatalf("version = %d, want 3", backend.doc.Version)
	}
	if err := b.RefreshShared(ctx); err != nil {
		t.Fatal(err)
	}
	if b.Get().Catalog.Language != "de-DE" {
		t.Fatal("B did not converge")
	}
}

func TestSharedKeyMismatchRefused(t *testing.T) {
	ctx := context.Background()
	backend := &memShared{}
	a, _ := Open(t.TempDir())
	if err := a.AttachShared(ctx, backend, SharedOptions{PollInterval: -1}); err != nil {
		t.Fatal(err)
	}
	other, _ := Open(t.TempDir()) // its own, different key
	err := other.AttachShared(ctx, backend, SharedOptions{PollInterval: -1})
	if !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("err = %v, want ErrKeyMismatch", err)
	}
	if other.Clustered() {
		t.Fatal("mismatched node attached")
	}
	if !strings.Contains(err.Error(), "ether cluster join") {
		t.Errorf("error does not say how to fix it: %v", err)
	}
}

func TestSharedNoSeed(t *testing.T) {
	s, _ := Open(t.TempDir())
	err := s.AttachShared(context.Background(), &memShared{}, SharedOptions{NoSeed: true, PollInterval: -1})
	if !errors.Is(err, ErrSharedMissing) {
		t.Fatalf("err = %v", err)
	}
}

// TestStandaloneUnchanged pins standalone behaviour: no backend, whole
// config in config.json, callbacks fire, nothing cluster-related happens.
func TestStandaloneUnchanged(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Clustered() || s.SharedVersion() != 0 {
		t.Fatal("standalone store reports cluster mode")
	}
	if err := s.RefreshShared(context.Background()); err != nil {
		t.Fatal(err)
	}
	fired := 0
	s.Subscribe(func(Config) { fired++ })
	s.SetNotifier(&memBus{}) // harmless when not clustered
	if err := s.Update(func(c *Config) error {
		c.Indexers = []Indexer{{ID: "x", APIKey: "secret-key"}}
		c.Server.Port = 1234
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if fired != 1 {
		t.Fatalf("callbacks fired %d times", fired)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if !bytes.Contains(raw, []byte(`"port": 1234`)) || !bytes.Contains(raw, []byte(`"indexers"`)) || bytes.Contains(raw, []byte("secret-key")) {
		t.Fatalf("config.json no longer holds the whole sealed config:\n%s", raw)
	}
	again, _ := Open(dir)
	if again.Get().Indexers[0].APIKey != "secret-key" {
		t.Fatal("round trip failed")
	}
}

func TestSealRoundTrip(t *testing.T) {
	s, _ := Open(t.TempDir())
	sealed, err := s.Seal("certs/plexdirect", []byte("pem"))
	if err != nil || !strings.HasPrefix(sealed, sealedPrefix) {
		t.Fatal(sealed, err)
	}
	if got, err := s.Unseal("certs/plexdirect", sealed); err != nil || string(got) != "pem" {
		t.Fatal(string(got), err)
	}
	if _, err := s.Unseal("other", sealed); err == nil {
		t.Fatal("sealed value opened under another name")
	}
	if len(s.KeyFingerprint()) != 64 || s.KeyFingerprint() == KeyFingerprint(make([]byte, 32)) {
		t.Fatal("fingerprint")
	}
}

func TestSharedCachePolicyKeepsMountsAndLocalCacheIndependent(t *testing.T) {
	a := Config{Cache: Cache{Dir: "/mnt/nas-a", Storage: "shared", BudgetBytes: 100}}
	a.SetCachePolicy(a.Cache)
	b := Config{Cache: Cache{Dir: "/Volumes/nas", Storage: "shared", BudgetBytes: 3}}
	merged, err := MergeShared(b, SharedJSON(a))
	if err != nil {
		t.Fatal(err)
	}
	if got := merged.EffectiveCache(); got.Dir != "/Volumes/nas" || got.BudgetBytes != 100 || got.Storage != "shared" {
		t.Fatalf("bad merged policy %+v", got)
	}
	b.Cache.Storage = "local"
	merged, err = MergeShared(b, SharedJSON(a))
	if err != nil {
		t.Fatal(err)
	}
	if got := merged.EffectiveCache(); got.BudgetBytes != 3 {
		t.Fatal("shared policy changed a local cache")
	}
	if a.SharedCache.Dir != "" {
		t.Fatal("mount path leaked into shared policy")
	}
}
