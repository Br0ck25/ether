package debrid

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/clusterbus"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// sharedDB is one database two nodes share: a SQLite file (several
// connections, like two processes) or, with ETHER_TEST_POSTGRES, a fresh
// PostgreSQL schema.
func sharedDB(t *testing.T) *sql.DB {
	t.Helper()
	if os.Getenv(store.TestPostgresEnv) != "" {
		return store.OpenTest(t)
	}
	db, err := store.OpenPath(context.Background(), filepath.Join(t.TempDir(), "ether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// testClock is a settable clock.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *testClock { return &testClock{t: time.Now()} }

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// clusterNodeSvc is one node's debrid Service over a provider shared by
// every node (one debrid account).
func clusterNodeSvc(t *testing.T, db *sql.DB, node clusterbus.Node, prov *fakeProvider, now func() time.Time) *Service {
	t.Helper()
	cfg := config.Debrid{CachedOnly: true, RemoveUncached: true,
		Providers: []config.DebridProvider{{ID: "rd", Name: "rd", Kind: config.DebridRealDebrid, APIKey: "k", Enabled: true}}}
	s := NewService(cfg, Options{DB: db, Now: now, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TorBoxSearchURL: "http://127.0.0.1:1",
		NewProvider: func(config.DebridProvider, ProviderOptions) (Provider, error) { return prov, nil }})
	if node != nil {
		s.SetCluster(node)
		t.Cleanup(func() { s.SetCluster(nil) })
	}
	return s
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLimiterResizesGracefully(t *testing.T) {
	l := newLimiter(4)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if err := l.acquire(ctx); err != nil {
			t.Fatal(err)
		}
	}
	l.resize(2) // four in flight keep running
	var got atomic.Int32
	go func() {
		if l.acquire(ctx) == nil {
			got.Add(1)
		}
	}()
	l.release()
	l.release() // two in flight: at the new limit
	time.Sleep(20 * time.Millisecond)
	if got.Load() != 0 {
		t.Fatal("acquired above the new limit")
	}
	l.release() // one in flight: a slot is free
	waitFor(t, "waiter admitted", func() bool { return got.Load() == 1 })
	short, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	_ = l.acquire(short) // 2 of 2
	if err := l.acquire(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("over limit: %v", err)
	}
}

func TestConcurrencyShareInStats(t *testing.T) {
	h := newHarness(t, 1<<20, "realdebrid")
	if st := h.svc.Stats()[0]; st.Slots != providerParallel || st.AccountSlots != providerParallel {
		t.Fatalf("standalone slots %+v", st)
	}
	h.svc.SetConcurrencyShare(func(c int) int { return c / 2 })
	if st := h.svc.Stats()[0]; st.Slots != providerParallel/2 || st.AccountSlots != providerParallel {
		t.Fatalf("shared slots %d/%d", st.Slots, st.AccountSlots)
	}
	// A share below one still leaves one slot; providers added later get
	// the share too.
	h.svc.SetConcurrencyShare(func(int) int { return 0 })
	cfg := h.svc.Config()
	cfg.Providers = append(cfg.Providers, config.DebridProvider{ID: "x", Kind: config.DebridTorBox, APIKey: "k2", Enabled: true, Priority: 5})
	h.provs["x"] = newFakeProvider("torbox", h.media, movieFiles(1<<20))
	h.svc.UpdateConfig(cfg)
	for _, st := range h.svc.Stats() {
		if st.Slots != 1 {
			t.Fatalf("%s slots %d", st.ID, st.Slots)
		}
	}
	h.svc.SetConcurrencyShare(nil)
	if st := h.svc.Stats()[0]; st.Slots != providerParallel {
		t.Fatalf("restored slots %d", st.Slots)
	}
}

func TestBackoffReachesOtherNodes(t *testing.T) {
	db := store.OpenTest(t)
	hub := clusterbus.NewHub()
	clkA, clkB := newClock(), newClock()
	prov := newFakeProvider("realdebrid", newMediaServer(t, 1<<10), movieFiles(1<<10))
	a := clusterNodeSvc(t, db, hub.Node("a"), prov, clkA.now)
	b := clusterNodeSvc(t, db, hub.Node("b"), prov, clkB.now)
	ea, eb := a.active()[0], b.active()[0]

	// A is rate limited for a minute: B pauses for the same minute.
	a.observe(ea, &rateLimitError{retryAfter: time.Minute})
	if err := eb.blocked(clkB.now()); err == nil {
		t.Fatal("B not paused by A's rate limit")
	}
	if st := b.Stats()[0]; st.State != "backoff" || st.BackoffUntil == nil || st.BackoffUntil.Sub(clkB.now()) > time.Minute+time.Second {
		t.Fatalf("B state %+v", st)
	}
	if err := eb.blocked(clkB.now().Add(61 * time.Second)); err != nil {
		t.Fatal("B's pause outlasts A's")
	}

	// A's success after its pause lifts B's pause.
	clkA.add(2 * time.Minute)
	a.observe(ea, nil)
	if err := eb.blocked(clkB.now()); err != nil {
		t.Fatalf("B still paused after A recovered: %v", err)
	}

	// A rejected key pauses B as auth_failed; an overlong pause from a
	// skewed clock is capped at what B would set itself.
	a.observe(ea, fmt.Errorf("x: %w", ErrAuth))
	if st := b.Stats()[0]; st.State != "auth_failed" {
		t.Fatalf("B state after auth failure %+v", st)
	}
	eb.applyRemote(clkB.now(), backoffEvent{Provider: "rd", State: "backoff", Until: clkB.now().Add(24 * time.Hour).UnixMilli()})
	if eb.until.After(clkB.now().Add(authBackoff)) {
		t.Fatal("remote pause not capped")
	}
	// A's own events never echo back into A.
	if ea.state != "auth_failed" || ea.lastError == "" || ea.lastError[:7] == "another" {
		t.Fatalf("A state %q %q", ea.state, ea.lastError)
	}
}

func TestKnownCachedFollowsOtherNode(t *testing.T) {
	db := store.OpenTest(t)
	hub := clusterbus.NewHub()
	prov := newFakeProvider("realdebrid", newMediaServer(t, 1<<10), movieFiles(1<<10))
	a := clusterNodeSvc(t, db, hub.Node("a"), prov, time.Now)
	b := clusterNodeSvc(t, db, hub.Node("b"), prov, time.Now)
	ctx := context.Background()

	b.setKnown(ctx, "rd", testHash, "", false)
	a.setKnown(ctx, "rd", testHash, "T1", true)
	waitFor(t, "B re-reads the hash", func() bool { return b.KnownCached(testHash) })
	if c, ok, tid := b.knownFor(ctx, "rd", testHash); !c || !ok || tid != "T1" {
		t.Fatalf("B knownFor %v %v %q", c, ok, tid)
	}
	// A bus resync forgets everything remembered in memory.
	hub.Resync("b")
	waitFor(t, "resync clears", func() bool { return !b.KnownCached(testHash) })
	if c, ok, _ := b.knownFor(ctx, "rd", testHash); !c || !ok {
		t.Fatal("B lost the persisted answer")
	}
}

// Two nodes acquiring the same torrent at once add it to the account once.
func TestAcquireClaimAddsOnce(t *testing.T) {
	db := sharedDB(t)
	hub := clusterbus.NewHub()
	prov := newFakeProvider("realdebrid", newMediaServer(t, 1<<20), movieFiles(1<<20))
	prov.cached[testHash] = true
	prov.acqDelay = 150 * time.Millisecond
	a := clusterNodeSvc(t, db, hub.Node("a"), prov, time.Now)
	b := clusterNodeSvc(t, db, hub.Node("b"), prov, time.Now)

	var wg sync.WaitGroup
	payloads := make([]Payload, 4)
	errs := make([]error, 4)
	for i := range payloads {
		svc := a
		if i%2 == 1 {
			svc = b
		}
		wg.Add(1)
		go func(i int, svc *Service) {
			defer wg.Done()
			raw, err := svc.Acquire(context.Background(), torrentRelease(fmt.Sprintf("rel-%d", i)))
			errs[i] = err
			if err == nil {
				payloads[i], _ = DecodePayload(raw)
			}
		}(i, svc)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		if payloads[i].TorrentID != payloads[0].TorrentID {
			t.Fatalf("torrent ids differ: %q vs %q", payloads[i].TorrentID, payloads[0].TorrentID)
		}
	}
	prov.mu.Lock()
	adds := prov.adds
	prov.mu.Unlock()
	if adds != 1 {
		t.Fatalf("torrent added %d times", adds)
	}
	var claimedBy string
	if err := db.QueryRow(`SELECT claimed_by FROM debrid_torrents WHERE provider_id='rd' AND info_hash=?`, testHash).Scan(&claimedBy); err != nil || claimedBy != "" {
		t.Fatalf("claim left behind: %q %v", claimedBy, err)
	}
}

// When the winning node finds the torrent uncached, the waiting node does
// not ask the provider again.
func TestAcquireClaimSharesNotCached(t *testing.T) {
	db := sharedDB(t)
	hub := clusterbus.NewHub()
	prov := newFakeProvider("realdebrid", newMediaServer(t, 1<<10), movieFiles(1<<10))
	prov.acqDelay = 150 * time.Millisecond
	a := clusterNodeSvc(t, db, hub.Node("a"), prov, time.Now)
	b := clusterNodeSvc(t, db, hub.Node("b"), prov, time.Now)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, svc := range []*Service{a, b} {
		wg.Add(1)
		go func(i int, svc *Service) {
			defer wg.Done()
			_, errs[i] = svc.Acquire(context.Background(), torrentRelease("rel"))
		}(i, svc)
	}
	wg.Wait()
	for i, err := range errs {
		if !errors.Is(err, core.ErrNotCached) {
			t.Fatalf("node %d: %v", i, err)
		}
	}
	if n := prov.callCount("acquire"); n != 1 {
		t.Fatalf("provider asked %d times", n)
	}
}

func TestClaimTakeoverAndBoundedWait(t *testing.T) {
	db := sharedDB(t)
	hub := clusterbus.NewHub()
	clk := newClock()
	prov := newFakeProvider("realdebrid", newMediaServer(t, 1<<10), movieFiles(1<<10))
	s := clusterNodeSvc(t, db, hub.Node("a"), prov, clk.now)
	e := s.active()[0]
	ctx := context.Background()

	// A crashed node's claim is taken over once stale.
	if _, err := db.Exec(`INSERT INTO debrid_torrents (provider_id, info_hash, torrent_id, cached, checked_at, claimed_by, claimed_at) VALUES ('rd', ?, '', 0, 0, 'dead/x', ?)`,
		testHash, clk.now().Add(-claimStale-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	id, release, err := s.claimAdd(ctx, e, testHash)
	if err != nil || id != "" {
		t.Fatalf("takeover: %q %v", id, err)
	}
	release()

	// A live claim that never finishes: the wait is bounded.
	if _, err := db.Exec(`UPDATE debrid_torrents SET claimed_by='busy/y', claimed_at=? WHERE info_hash=?`, clk.now().Unix(), testHash); err != nil {
		t.Fatal(err)
	}
	old := claimWait
	claimWait = 0
	defer func() { claimWait = old }()
	if _, _, err := s.claimAdd(ctx, e, testHash); !errors.Is(err, errClaimBusy) {
		t.Fatalf("busy claim: %v", err)
	}
	// A recorded torrent id is used without claiming.
	if _, err := db.Exec(`UPDATE debrid_torrents SET torrent_id='T9' WHERE info_hash=?`, testHash); err != nil {
		t.Fatal(err)
	}
	if id, _, err := s.claimAdd(ctx, e, testHash); err != nil || id != "T9" {
		t.Fatalf("recorded id: %q %v", id, err)
	}
	// Standalone never claims.
	solo := clusterNodeSvc(t, db, nil, prov, clk.now)
	if id, _, err := solo.claimAdd(ctx, solo.active()[0], "ff"+testHash[2:]); id != "" || err != nil {
		t.Fatalf("standalone claim %q %v", id, err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM debrid_torrents WHERE info_hash=?`, "ff"+testHash[2:]).Scan(&n)
	if n != 0 {
		t.Fatal("standalone wrote a claim")
	}
}

// A torrent another node streams from is never removed from the account.
func TestStreamingLeaseBlocksRemoval(t *testing.T) {
	db := sharedDB(t)
	hub := clusterbus.NewHub()
	media := newMediaServer(t, 1<<20)
	prov := newFakeProvider("realdebrid", media, movieFiles(1<<20))
	prov.cached[testHash] = true
	a := clusterNodeSvc(t, db, hub.Node("a"), prov, time.Now)
	b := clusterNodeSvc(t, db, hub.Node("b"), prov, time.Now)
	eng := NewEngine(b, EngineOptions{Inner: &fakeInner{}, Cache: newMemCache(), BlockSize: testBlock, Logger: b.log})
	ctx := context.Background()
	raw, err := b.Acquire(ctx, torrentRelease("rel-l"))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := DecodePayload(raw)
	src, err := eng.Open(ctx, "rel-l", raw)
	if err != nil {
		t.Fatal(err)
	}
	may := a.mayRemove("rd")
	if may == nil {
		t.Fatal("clustered service has no removal check")
	}
	waitFor(t, "lease written", func() bool { return !may(ctx, p.TorrentID) })
	if !may(ctx, "other-torrent") {
		t.Fatal("unleased torrent kept")
	}
	src.Close()
	waitFor(t, "lease dropped on close", func() bool { return may(ctx, p.TorrentID) })

	// An expired lease (a crashed node) does not block removal.
	if _, err := db.Exec(`INSERT INTO debrid_leases (provider_id, torrent_id, node_id, expires_at) VALUES ('rd', 'T-old', 'dead', ?)`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if !may(ctx, "T-old") {
		t.Fatal("expired lease blocks removal")
	}
	// Close drops every lease of this node.
	if _, err := eng.Open(ctx, "rel-l", raw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "lease again", func() bool { return !may(ctx, p.TorrentID) })
	eng.Close()
	if !may(ctx, p.TorrentID) {
		t.Fatal("lease survived engine close")
	}
	if clusterNodeSvc(t, db, nil, prov, time.Now).mayRemove("rd") != nil {
		t.Fatal("standalone checks leases")
	}
}

// Providers consult MayRemove before deleting a torrent they just added.
func TestProvidersKeepLeasedTorrents(t *testing.T) {
	keep := func(context.Context, string) bool { return false }
	f := &fakeRD{cached: false}
	srv := newTestServer(t, f.handler(t))
	req := acquireReq(true)
	req.MayRemove = keep
	if _, err := newRealDebrid(testKey, fastOpts(srv)).Acquire(context.Background(), req); !errors.Is(err, core.ErrNotCached) {
		t.Fatalf("rd: %v", err)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("real-debrid removed a leased torrent: %v", f.deleted)
	}
	ad := &fakeAD{ready: false}
	srv = newTestServer(t, ad.handler(t))
	if _, err := newAllDebrid(testKey, fastOpts(srv)).Acquire(context.Background(), req); !errors.Is(err, core.ErrNotCached) {
		t.Fatalf("ad: %v", err)
	}
	if len(ad.deleted) != 0 {
		t.Fatalf("alldebrid removed a leased torrent: %v", ad.deleted)
	}
}

func newTestServer(t *testing.T, h http.Handler) string {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

// A node waiting for another node's add does not hold its provider slot.
func TestClaimWaitFreesProviderSlot(t *testing.T) {
	db := sharedDB(t)
	hub := clusterbus.NewHub()
	prov := newFakeProvider("realdebrid", newMediaServer(t, 1<<10), movieFiles(1<<10))
	s := clusterNodeSvc(t, db, hub.Node("a"), prov, time.Now)
	s.SetConcurrencyShare(func(int) int { return 1 })
	e := s.active()[0]
	if _, err := db.Exec(`INSERT INTO debrid_torrents (provider_id, info_hash, torrent_id, cached, checked_at, claimed_by, claimed_at) VALUES ('rd', ?, '', 0, 0, 'b/x', ?)`,
		testHash, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := e.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		id, _, err := s.claimAdd(ctx, e, testHash)
		e.release()
		if err != nil {
			t.Error(err)
		}
		got <- id
	}()
	time.Sleep(3 * claimPoll / 2)
	actx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := e.acquire(actx); err != nil {
		t.Fatalf("slot held while waiting: %v", err)
	}
	e.release()
	if _, err := db.Exec(`UPDATE debrid_torrents SET torrent_id='T5', claimed_by='' WHERE info_hash=?`, testHash); err != nil {
		t.Fatal(err)
	}
	if id := <-got; id != "T5" {
		t.Fatalf("waiter got %q", id)
	}
	if n := e.sem.size(); n != 1 {
		t.Fatalf("slots %d", n)
	}
	e.sem.mu.Lock()
	used := e.sem.used
	e.sem.mu.Unlock()
	if used != 0 {
		t.Fatalf("slots in use after release: %d", used)
	}
}
