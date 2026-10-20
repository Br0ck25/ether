package updater

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
)

// fakeLease is a cluster update lease shared by fake nodes.
type fakeLease struct {
	mu      sync.Mutex
	now     func() time.Time
	ttl     time.Duration
	holder  string
	version string
	expires time.Time
	failed  string
	log     []string // acquisitions, in order
}

func (l *fakeLease) acquire(node, version string) (bool, string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed != "" && l.failed == version {
		return false, "", fmt.Errorf("fake: %w", ErrClusterRolledBack)
	}
	now := l.now()
	if l.holder != "" && l.holder != node && now.Before(l.expires) {
		return false, l.holder, nil
	}
	if l.holder != node {
		l.log = append(l.log, node+"@"+version)
	}
	l.holder, l.version, l.expires = node, version, now.Add(l.ttl)
	return true, node, nil
}

func (l *fakeLease) release(node string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder == node {
		l.holder = ""
	}
}

func (l *fakeLease) current() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder == "" || !l.now().Before(l.expires) {
		return ""
	}
	return l.holder
}

// fakeCoord is one node's Coordinator over a fakeLease.
type fakeCoord struct {
	lease    *fakeLease
	node     string
	mu       sync.Mutex
	draining bool
	drains   []bool
}

func (c *fakeCoord) AcquireUpdate(_ context.Context, version string) (bool, string, error) {
	return c.lease.acquire(c.node, version)
}
func (c *fakeCoord) ReleaseUpdate(context.Context) error { c.lease.release(c.node); return nil }
func (c *fakeCoord) ReportFailed(_ context.Context, version string) error {
	c.lease.mu.Lock()
	c.lease.failed = version
	c.lease.mu.Unlock()
	c.lease.release(c.node)
	return nil
}
func (c *fakeCoord) SetDraining(d bool) {
	c.mu.Lock()
	c.draining = d
	c.drains = append(c.drains, d)
	c.mu.Unlock()
}
func (c *fakeCoord) isDraining() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.draining
}

// coordHarness is a self-applying node with a Coordinator and an always
// open maintenance window, staged with 2.0.0 after its first check.
func coordHarness(t *testing.T, lease *fakeLease, node string, mutate func(*ManagerOptions)) (*harness, *fakeCoord) {
	t.Helper()
	c := &fakeCoord{lease: lease, node: node}
	h := newHarness(t, func(o *ManagerOptions) {
		o.Coordinator, o.NodeName = c, node
		if mutate != nil {
			mutate(o)
		}
	})
	h.cfg.MaintenanceStart, h.cfg.MaintenanceEnd = "00:00", "00:00"
	return h, c
}

func TestCoordinatedUpdateDrainsThenInstalls(t *testing.T) {
	lease := &fakeLease{ttl: 20 * time.Minute}
	h, c := coordHarness(t, lease, "a", nil)
	lease.now = h.clock.Now
	h.active = 2
	h.run(DefaultFirstCheckDelay)
	st := h.m.Status()
	if st.State != StateDraining || !c.isDraining() || lease.current() != "a" || len(h.applied) != 0 {
		t.Fatalf("state %s draining %v holder %q applied %v", st.State, c.isDraining(), lease.current(), h.applied)
	}
	if st.Cluster == nil || !st.Cluster.LeaseHeld || !st.Cluster.Draining || st.Cluster.MaxDrainSeconds != int(DefaultMaxDrain/time.Second) {
		t.Fatalf("cluster status %+v", st.Cluster)
	}
	// The lease is renewed while draining (TTL 20m, drain lasts longer).
	h.run(25 * time.Minute)
	if lease.current() != "a" || len(h.applied) != 0 {
		t.Fatalf("lease lost or installed early: holder %q applied %v", lease.current(), h.applied)
	}
	// This node's sessions end: install at once (no quiet period needed —
	// nothing new was admitted).
	h.active = 0
	h.run(DefaultTick)
	if len(h.applied) != 1 || len(h.restarts) != 1 {
		t.Fatalf("applied %v restarts %v", h.applied, h.restarts)
	}
	if lease.current() != "a" {
		t.Fatal("the lease must stay held through the restart")
	}
}

func TestCoordinatedUpdateInstallsAfterMaxDrain(t *testing.T) {
	lease := &fakeLease{ttl: 20 * time.Minute}
	h, _ := coordHarness(t, lease, "a", nil)
	lease.now = h.clock.Now
	h.cfg.MaxDrain = 0 // default 30m
	h.active = 1
	h.run(DefaultFirstCheckDelay)
	h.run(DefaultMaxDrain - 2*DefaultTick)
	if len(h.applied) != 0 {
		t.Fatal("installed before the drain time was up")
	}
	h.run(3 * DefaultTick)
	if len(h.applied) != 1 {
		t.Fatalf("not installed after max drain (state %s)", h.state())
	}
}

func TestCoordinatedUpdatePostponedWhenWindowCloses(t *testing.T) {
	lease := &fakeLease{ttl: 20 * time.Minute}
	h, c := coordHarness(t, lease, "a", nil)
	lease.now = h.clock.Now
	h.cfg.MaintenanceStart, h.cfg.MaintenanceEnd = "12:00", "12:10"
	h.active = 1
	h.run(DefaultFirstCheckDelay)
	if h.state() != StateDraining {
		t.Fatalf("state %s", h.state())
	}
	h.run(10 * time.Minute) // 12:10: window closed
	if h.state() != StateWaitingWindow || c.isDraining() || lease.current() != "" || len(h.applied) != 0 {
		t.Fatalf("state %s draining %v holder %q", h.state(), c.isDraining(), lease.current())
	}
}

// TestRollingUpdateOneNodeAtATime stages the same release on two nodes
// sharing a lease: one drains and installs; the other waits its turn
// until the first node's new version has confirmed its health.
func TestRollingUpdateOneNodeAtATime(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)}
	lease := &fakeLease{ttl: 20 * time.Minute, now: clock.Now}
	shared := func(o *ManagerOptions) { o.Now = clock.Now }
	a, ca := coordHarness(t, lease, "a", shared)
	b, cb := coordHarness(t, lease, "b", shared)
	step := func(d time.Duration, hs ...*harness) {
		for end := clock.Now().Add(d); clock.Now().Before(end); {
			clock.Advance(DefaultTick)
			for _, h := range hs {
				h.m.step(context.Background())
			}
		}
	}
	a.active, b.active = 1, 0
	// a checks first and takes the lease; b stages a tick later.
	step(DefaultFirstCheckDelay, a)
	step(DefaultTick, a, b)
	if lease.current() != "a" || a.state() != StateDraining || b.state() != StateWaitingTurn || cb.isDraining() {
		t.Fatalf("a %s b %s holder %q", a.state(), b.state(), lease.current())
	}
	if st := b.m.Status(); st.Cluster.LeaseHolder != "a" {
		t.Fatalf("b's view of the holder: %+v", st.Cluster)
	}
	// b is idle but must not install while a holds the lease.
	step(10*time.Minute, a, b)
	if len(b.applied) != 0 {
		t.Fatal("b installed while a held the lease")
	}
	a.active = 0
	step(DefaultTick, a, b)
	if len(a.applied) != 1 || !ca.isDraining() {
		t.Fatalf("a did not install: %v", a.applied)
	}

	// a's new process confirms 2.0.0 while holding the lease.
	var p Pending
	if ok, _ := readJSON(PendingPath(a.dir), &p); !ok {
		t.Fatal("no pending marker")
	}
	a2, _ := coordHarness(t, lease, "a", func(o *ManagerOptions) {
		o.Now = clock.Now
		o.CurrentVersion = "2.0.0"
		o.Dir, o.Target = a.dir, a.target
		o.Boot = StartupResult{Confirming: &p}
		o.NewClient = func(config.Updates) (Checker, error) { return &fakeChecker{}, nil } // 2.0.0 is current
	})
	step(DefaultHealthPeriod-DefaultTick, a2, b)
	if lease.current() != "a" || len(b.applied) != 0 {
		t.Fatalf("lease released before a confirmed: holder %q", lease.current())
	}
	step(2*DefaultTick, a2, b)
	if len(b.applied) != 1 {
		t.Fatalf("b never got its turn (b %s, holder %q)", b.state(), lease.current())
	}
	if got := fmt.Sprint(lease.log); got != "[a@2.0.0 b@2.0.0]" {
		t.Fatalf("lease order %s", got)
	}
}

func TestCoordinatedRollbackSkippedClusterWide(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)}
	lease := &fakeLease{ttl: 20 * time.Minute, now: clock.Now}
	lease.holder, lease.expires = "a", clock.Now().Add(time.Hour) // a was updating
	a, _ := coordHarness(t, lease, "a", func(o *ManagerOptions) {
		o.Now = clock.Now
		o.Boot = StartupResult{RolledBack: &FailedUpdate{Version: "2.0.0", From: "1.0.0"}}
	})
	b, _ := coordHarness(t, lease, "b", func(o *ManagerOptions) { o.Now = clock.Now })
	clock.Advance(DefaultTick)
	a.m.step(context.Background())
	if lease.current() != "" || lease.failed != "2.0.0" {
		t.Fatalf("rollback not reported: holder %q failed %q", lease.current(), lease.failed)
	}
	for end := clock.Now().Add(DefaultFirstCheckDelay + time.Minute); clock.Now().Before(end); {
		clock.Advance(DefaultTick)
		b.m.step(context.Background())
	}
	st := b.m.Status()
	if len(b.applied) != 0 || st.FailedVersion != "2.0.0" || st.Available != nil {
		t.Fatalf("b installed or kept a release that rolled back elsewhere: %+v", st)
	}
}

func TestLeftoverLeaseReleasedAtStartup(t *testing.T) {
	lease := &fakeLease{ttl: 20 * time.Minute}
	h, _ := coordHarness(t, lease, "a", nil)
	lease.now = h.clock.Now
	lease.holder, lease.expires = "a", h.clock.Now().Add(time.Hour)
	h.run(DefaultTick)
	if lease.current() != "" {
		t.Fatal("a lease left by this node's previous process was not released")
	}
}
