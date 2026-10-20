package stats

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestStartupClosesOnlyThisNodesPlays(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ether.db")
	dbA, err := store.OpenPath(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	dbB, err := store.OpenPath(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	a := New(dbA, Options{Now: clock, NodeID: "node-a"})
	if err := a.Record(ctx, &core.PlaySession{ID: "live-on-a", UserID: 1, Key: 5, StartedAt: now}, false, ""); err != nil {
		t.Fatal(err)
	}
	b := New(dbB, Options{Now: clock, NodeID: "node-b"})
	if err := b.Record(ctx, &core.PlaySession{ID: "live-on-b", UserID: 2, Key: 6, StartedAt: now}, false, ""); err != nil {
		t.Fatal(err)
	}
	// Node B restarts: its own interrupted play closes, node A's stays live.
	New(dbB, Options{Now: clock, NodeID: "node-b"})
	ended := func(id string) int64 {
		var e int64
		if err := dbA.QueryRow(`SELECT ended FROM stats_plays WHERE id=?`, id).Scan(&e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	if ended("live-on-a") != 0 || ended("live-on-b") == 0 {
		t.Fatalf("a=%d b=%d", ended("live-on-a"), ended("live-on-b"))
	}
}

func TestSummaryDayAndHourLabels(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	start := time.Date(2026, 10, 19, 7, 30, 0, 0, time.UTC)
	svc := New(db, Options{Now: func() time.Time { return start }})
	for i, at := range []time.Time{start, start.Add(17 * time.Hour)} {
		p := &core.PlaySession{ID: string(rune('a' + i)), UserID: 1, Key: 5, StartedAt: at}
		if err := svc.Record(ctx, p, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	sum, err := svc.Summary(ctx, start.Add(-time.Hour), start.Add(48*time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Daily) != 2 || sum.Daily[0].Label != "2026-10-19" || sum.Daily[1].Label != "2026-10-20" {
		t.Fatalf("daily %+v", sum.Daily)
	}
	if len(sum.Hourly) != 2 || sum.Hourly[0].Label != "00" || sum.Hourly[1].Label != "07" {
		t.Fatalf("hourly %+v", sum.Hourly)
	}
}
