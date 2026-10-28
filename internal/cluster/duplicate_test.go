package cluster

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A second process started with a copied node.json refuses to join while
// the original is alive.
func TestPostgresRefusesDuplicateNodeAtStart(t *testing.T) {
	start := pgClusterStart(t)
	a, err := start("a", nil)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := start("clone", func(o *Options) {
		o.Local.Identity = a.local.Identity
		o.IdentityPath = "/data/node.json"
	})
	if !errors.Is(err, ErrDuplicateNode) || !strings.Contains(err.Error(), "delete /data/node.json") {
		t.Fatalf("clone start: %v", err)
	}
	if clone.IsLeader() {
		t.Fatal("clone leads")
	}
	// The original keeps heartbeating under its own incarnation.
	time.Sleep(300 * time.Millisecond)
	var inc string
	if err := a.db.QueryRowContext(context.Background(), `SELECT incarnation FROM cluster_nodes WHERE id=?`, a.local.Identity.NodeID).Scan(&inc); err != nil || inc != a.inc {
		t.Fatalf("row incarnation %q (want %q) err=%v", inc, a.inc, err)
	}
	if a.Err() != nil {
		t.Fatalf("original failed: %v", a.Err())
	}
}

// A row left by this node's crashed previous process does not block a
// restart: it stops advancing, so the new process takes it over.
func TestPostgresTakesOverStaleOwnRow(t *testing.T) {
	start := pgClusterStart(t)
	a, err := start("a", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := newID()
	now := time.Now().UnixMilli()
	if _, err := a.db.ExecContext(context.Background(),
		`INSERT INTO cluster_nodes(id,name,started_at,last_seen,incarnation) VALUES(?,?,?,?,?)`, id, "b", now, now, "crashed"); err != nil {
		t.Fatal(err)
	}
	b, err := start("b", func(o *Options) { o.Local.Identity = Identity{NodeID: id, Name: "b"} })
	if err != nil {
		t.Fatalf("restart after crash: %v", err)
	}
	waitFor(t, "b's row taken over", 3*time.Second, func() bool {
		var inc string
		_ = a.db.QueryRowContext(context.Background(), `SELECT incarnation FROM cluster_nodes WHERE id=?`, id).Scan(&inc)
		return inc == b.inc
	})
}

// Once joined, a foreign write to this node's row (a clone that got past
// the start check) ends this node's membership.
func TestPostgresDetectsHeartbeatConflict(t *testing.T) {
	start := pgClusterStart(t)
	var fatal atomic.Value
	a, err := start("a", func(o *Options) {
		o.OnFatal = func(err error) { fatal.Store(err) }
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a leads", 5*time.Second, a.IsLeader)
	if _, err := a.db.ExecContext(context.Background(), `UPDATE cluster_nodes SET incarnation='other' WHERE id=?`, a.local.Identity.NodeID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "conflict reported", 5*time.Second, func() bool { return fatal.Load() != nil })
	if err := fatal.Load().(error); !errors.Is(err, ErrDuplicateNode) || !errors.Is(a.Err(), ErrDuplicateNode) {
		t.Fatalf("fatal %v / %v", err, a.Err())
	}
	waitFor(t, "a steps down", 3*time.Second, func() bool { return !a.IsLeader() })
	// It no longer heartbeats: the other process keeps the row.
	time.Sleep(300 * time.Millisecond)
	var inc string
	if err := a.db.QueryRowContext(context.Background(), `SELECT incarnation FROM cluster_nodes WHERE id=?`, a.local.Identity.NodeID).Scan(&inc); err != nil || inc != "other" {
		t.Fatalf("row incarnation %q err=%v", inc, err)
	}
}
