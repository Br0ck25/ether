package app

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/cluster"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/usenet"
)

// liveMembers is a clustered runtime whose live membership the test sets.
type liveMembers struct {
	fakeMembers
	mu sync.Mutex
}

func (f *liveMembers) Nodes(context.Context) ([]cluster.Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.nodes), nil
}

func (f *liveMembers) set(nodes ...cluster.Node) {
	f.mu.Lock()
	f.nodes = nodes
	f.mu.Unlock()
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testFetcher(t *testing.T, connections int) *usenet.Fetcher {
	t.Helper()
	f := usenet.New([]config.UsenetProvider{{ID: "p", Host: "news.invalid", Enabled: true, Connections: connections}}, usenet.Options{Logger: quietLog()})
	t.Cleanup(func() { f.Close() })
	return f
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLiveBudgetMembers(t *testing.T) {
	self := cluster.Node{ID: "self", Caps: cluster.Caps{Weight: 2}}
	m := liveBudgetMembers([]cluster.Node{
		{ID: "b", Live: true, Caps: cluster.Caps{Weight: 1}},
		{ID: "dead", Live: false},
	}, self)
	if len(m.weights) != 2 || m.weights["self"] != 2 || m.weights["b"] != 1 || m.self != "self" {
		t.Fatalf("members %+v", m)
	}
	if m.key() != "b=1;self=2;" {
		t.Fatalf("key %q", m.key())
	}
}

func TestRunBudgetsFollowsMembership(t *testing.T) {
	solo := cluster.NewSolo(cluster.Local{Identity: cluster.Identity{NodeID: "self", Name: "a"}}, nil)
	fm := &liveMembers{fakeMembers: fakeMembers{Solo: solo}}
	self, b := cluster.Node{ID: "self", Live: true}, cluster.Node{ID: "b", Live: true}
	fm.set(self, b)
	a := &App{cluster: fm, fetcher: testFetcher(t, 10), log: quietLog()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.runBudgets(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitUntil(t, "half the connections", func() bool { return a.fetcher.MaxConnections() == 5 })

	// A third node joins (announced on the members topic).
	fm.set(self, b, cluster.Node{ID: "c", Live: true, Caps: cluster.Caps{Weight: 3}})
	_ = fm.Publish(ctx, cluster.TopicMembers, []byte("c"))
	waitUntil(t, "a fifth after c joins", func() bool { return a.fetcher.MaxConnections() == 2 })

	// c leaves: its share comes back.
	fm.set(self, b)
	_ = fm.Publish(ctx, cluster.TopicMembers, []byte("c"))
	waitUntil(t, "half again after c leaves", func() bool { return a.fetcher.MaxConnections() == 5 })
}

func TestRunBudgetsStandaloneKeepsConfiguredCounts(t *testing.T) {
	solo := cluster.NewSolo(cluster.Local{Identity: cluster.Identity{NodeID: "self"}}, nil)
	a := &App{cluster: solo, fetcher: testFetcher(t, 10), log: quietLog()}
	a.runBudgets(context.Background()) // returns at once standalone
	if got := a.fetcher.MaxConnections(); got != 10 {
		t.Fatalf("standalone connections %d", got)
	}
}

// Two real nodes on one PostgreSQL schema split a provider's connections
// and the survivor takes the whole limit back when the other leaves.
func TestBudgetsAcrossNodesPostgres(t *testing.T) {
	raw := os.Getenv(store.TestPostgresEnv)
	if raw == "" {
		t.Skip("set " + store.TestPostgresEnv + " to run PostgreSQL tests")
	}
	ctx := context.Background()
	schemaDB, err := store.OpenTestPostgres(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { schemaDB.Close() })
	var schema string
	if err := schemaDB.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	node := func(id string) (*App, *cluster.Postgres) {
		db, err := sql.Open(store.PostgresDriverName, u.String())
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(3)
		pg, err := cluster.NewPostgres(cluster.Options{
			DB: db, Local: cluster.Local{Identity: cluster.Identity{NodeID: id, Name: id}}, Logger: quietLog(),
			HeartbeatInterval: 100 * time.Millisecond, LiveAfter: 700 * time.Millisecond, ElectionInterval: 100 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := pg.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pg.Close(); db.Close() })
		a := &App{cluster: pg, fetcher: testFetcher(t, 10), log: quietLog()}
		rctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { a.runBudgets(rctx); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
		return a, pg
	}
	a, _ := node("node-a")
	b, pgB := node("node-b")
	waitUntil(t, "both nodes take half", func() bool {
		return a.fetcher.MaxConnections() == 5 && b.fetcher.MaxConnections() == 5
	})
	pgB.Close() // leaves: deletes its row and announces it
	waitUntil(t, "a takes the whole limit back", func() bool { return a.fetcher.MaxConnections() == 10 })
}
