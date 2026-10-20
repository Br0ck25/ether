package cluster

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

// pgCluster opens a fresh schema on ETHER_TEST_POSTGRES and returns a
// factory for nodes, each with its own pool on that schema (as separate
// processes would have).
func pgCluster(t *testing.T) func(name string, tweak func(*Options)) *Postgres {
	t.Helper()
	start := pgClusterStart(t)
	return func(name string, tweak func(*Options)) *Postgres {
		t.Helper()
		p, err := start(name, tweak)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
}

// pgClusterStart is pgCluster returning Start's error instead of failing.
func pgClusterStart(t *testing.T) func(name string, tweak func(*Options)) (*Postgres, error) {
	t.Helper()
	raw := os.Getenv(store.TestPostgresEnv)
	if raw == "" {
		t.Skip("set " + store.TestPostgresEnv + " to run PostgreSQL cluster tests")
	}
	ctx := context.Background()
	base, err := store.OpenTestPostgres(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	var schema string
	if err := base.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return func(name string, tweak func(*Options)) (*Postgres, error) {
		t.Helper()
		db, err := sql.Open(store.PostgresDriverName, u.String())
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(3)
		db.SetConnMaxIdleTime(2 * time.Second)
		o := Options{
			DB:                db,
			Local:             Local{Identity: Identity{NodeID: newID(), Name: name}, Version: "test"},
			Logger:            discardLogger(),
			HeartbeatInterval: 100 * time.Millisecond, LiveAfter: 700 * time.Millisecond,
			ElectionInterval: 100 * time.Millisecond, ProbeTimeout: 2 * time.Second,
			StepDownHold: 100 * time.Millisecond, IdleSessionTimeout: 3 * time.Second,
			ListenPing: 300 * time.Millisecond, MinBackoff: 50 * time.Millisecond, MaxBackoff: 500 * time.Millisecond,
		}
		if tweak != nil {
			tweak(&o)
		}
		p, err := NewPostgres(o)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			p.Close()
			db.Close()
		})
		return p, p.Start(ctx)
	}
}

// terminate ends a node's dedicated backend ("leader" or "bus") as a
// network failure or server restart would.
func terminate(t *testing.T, p *Postgres, role string) {
	t.Helper()
	var n int
	err := p.db.QueryRowContext(context.Background(),
		`SELECT COUNT(pg_terminate_backend(pid)) FROM pg_stat_activity WHERE application_name = ?`,
		"ether-"+role+"/"+shortID(p.local.Identity.NodeID)).Scan(&n)
	if err != nil || n == 0 {
		t.Fatalf("terminate %s backend: n=%d err=%v", role, n, err)
	}
}

func TestPostgresLeaderFailover(t *testing.T) {
	node := pgCluster(t)
	a := node("a", func(o *Options) { o.StepDownHold = 3 * time.Second })
	waitFor(t, "a leads", 5*time.Second, a.IsLeader)
	epochA := a.Epoch()
	if epochA < 1 {
		t.Fatalf("epoch %d", epochA)
	}
	var lost atomic.Bool
	a.OnLeaderChange(func(v bool) {
		if !v {
			lost.Store(true)
		}
	})
	jobCancelled := make(chan struct{})
	jobStarted := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runs atomic.Int32
	go a.RunAsLeader(ctx, "singleton", func(jctx context.Context) {
		if runs.Add(1) != 1 {
			return // restarted after regaining leadership
		}
		close(jobStarted)
		<-jctx.Done()
		close(jobCancelled)
	})
	<-jobStarted

	b := node("b", nil)
	time.Sleep(400 * time.Millisecond)
	if b.IsLeader() {
		t.Fatal("two leaders")
	}
	nodes, err := b.Nodes(context.Background())
	if err != nil || len(nodes) != 2 {
		t.Fatalf("nodes %+v %v", nodes, err)
	}
	for _, n := range nodes {
		if (n.ID == a.Self().ID) != n.Leader {
			t.Fatalf("leader flag wrong: %+v", nodes)
		}
	}

	terminate(t, a, "leader")
	waitFor(t, "a steps down", 5*time.Second, func() bool { return lost.Load() && !a.IsLeader() })
	select {
	case <-jobCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("leader job not cancelled on loss of leadership")
	}
	waitFor(t, "b takes over", 5*time.Second, b.IsLeader)
	if b.Epoch() <= epochA {
		t.Fatalf("epoch did not advance: %d -> %d", epochA, b.Epoch())
	}
	if a.IsLeader() {
		t.Fatal("a re-acquired while b leads")
	}

	// A clean shutdown hands leadership back promptly.
	b.Close()
	waitFor(t, "a leads again", 8*time.Second, a.IsLeader)
	waitFor(t, "leader job restarted", 2*time.Second, func() bool { return runs.Load() == 2 })
}

func TestPostgresBus(t *testing.T) {
	node := pgCluster(t)
	a := node("a", nil)
	b := node("b", nil)

	var mu sync.Mutex
	var gotB, gotA []string
	resync := make(chan struct{}, 4)
	b.Subscribe("timeline", func(p []byte) { mu.Lock(); gotB = append(gotB, string(p)); mu.Unlock() })
	a.Subscribe("timeline", func(p []byte) { mu.Lock(); gotA = append(gotA, string(p)); mu.Unlock() })
	b.Subscribe(TopicResync, func([]byte) { resync <- struct{}{} })
	ctx := context.Background()
	if err := a.Publish(ctx, "timeline", []byte(`{"key":1}`)); err != nil {
		t.Fatal(err)
	}
	bin := string([]byte{0, 0xfe, 'x'})
	if err := a.Publish(ctx, "timeline", []byte(bin)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "delivery to b", 5*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(gotB) == 2 })
	time.Sleep(200 * time.Millisecond) // any echo would have arrived by now
	mu.Lock()
	if gotB[0] != `{"key":1}` || gotB[1] != bin || len(gotA) != 2 {
		t.Fatalf("b=%q a=%q (a must see its own events exactly once)", gotB, gotA)
	}
	mu.Unlock()
	if err := a.Publish(ctx, "timeline", make([]byte, MaxPayload+1)); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
	// Idle pings must not be mistaken for failures.
	time.Sleep(time.Second)
	select {
	case <-resync:
		t.Fatal("resync without a reconnect")
	default:
	}

	terminate(t, b, "bus")
	select {
	case <-resync:
	case <-time.After(5 * time.Second):
		t.Fatal("no resync after bus reconnect")
	}
	waitFor(t, "bus back up", 5*time.Second, b.BusConnected)
	if err := a.Publish(ctx, "timeline", []byte("after")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "delivery after reconnect", 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(gotB) == 3 && gotB[2] == "after"
	})
}

func TestPostgresLiveness(t *testing.T) {
	node := pgCluster(t)
	var addrs atomic.Value
	addrs.Store([]string{"http://10.0.0.1:32400"})
	var joins atomic.Int32
	a := node("a", func(o *Options) {
		o.Local.Addrs = func(context.Context) []string { return addrs.Load().([]string) }
		o.Local.Load = func() int { return 2 }
		o.Local.Caps = func() Caps { return Caps{Transcode: true, HWAccel: "vaapi"} }
	})
	a.Subscribe(TopicMembers, func([]byte) { joins.Add(1) })
	b := node("b", nil)
	waitFor(t, "b's join announced", 5*time.Second, func() bool { return joins.Load() >= 1 })

	waitFor(t, "a's addresses in its heartbeat", 5*time.Second, func() bool {
		ns, _ := b.Nodes(context.Background())
		for _, n := range ns {
			if n.ID == a.Self().ID {
				return len(n.Addrs) == 1 && n.Load == 2 && n.Caps.HWAccel == "vaapi" && n.Name == "a"
			}
		}
		return false
	})
	addrs.Store([]string{"http://10.0.0.1:32400", "http://10.0.0.7:32400"})
	waitFor(t, "address change heartbeated", 5*time.Second, func() bool {
		ns, _ := b.Nodes(context.Background())
		for _, n := range ns {
			if n.ID == a.Self().ID {
				return strings.Join(n.Addrs, ",") == "http://10.0.0.1:32400,http://10.0.0.7:32400"
			}
		}
		return false
	})

	// b crashes: it stops heartbeating but its row stays.
	bID := b.Self().ID
	b.halt()
	waitFor(t, "b expires", 5*time.Second, func() bool {
		ns, err := a.Nodes(context.Background())
		return err == nil && len(ns) == 1 && ns[0].ID == a.Self().ID
	})
	all, err := a.AllNodes(context.Background())
	if err != nil || len(all) != 2 {
		t.Fatalf("all %+v %v", all, err)
	}
	for _, n := range all {
		if n.ID == bID && (n.Live || n.Leader) {
			t.Fatalf("dead node reported live/leader: %+v", n)
		}
	}
	waitFor(t, "a leads alone", 5*time.Second, a.IsLeader)

	// A clean leave removes the row.
	aID := a.Self().ID
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := a.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM cluster_nodes WHERE id=?`, aID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("row after close: n=%d err=%v", n, err)
	}
}

func TestPostgresPrunesLongDeadNodes(t *testing.T) {
	node := pgCluster(t)
	a := node("a", nil)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour).UnixMilli()
	if _, err := a.db.ExecContext(ctx, `INSERT INTO cluster_nodes(id,name,started_at,last_seen) VALUES('ghost','ghost',?,?)`, old, old); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.lastPrune = time.Time{}
	a.mu.Unlock()
	waitFor(t, "ghost pruned", 5*time.Second, func() bool {
		var n int
		_ = a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cluster_nodes WHERE id='ghost'`).Scan(&n)
		return n == 0
	})
}
