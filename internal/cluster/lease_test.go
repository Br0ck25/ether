package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

func TestUpdateLeaseAcquireRenewExpireHandoff(t *testing.T) {
	db := store.OpenTest(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 1, 3, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	a := &UpdateLease{DB: db, NodeID: "a", TTL: 20 * time.Minute, Now: clock}
	b := &UpdateLease{DB: db, NodeID: "b", TTL: 20 * time.Minute, Now: clock}

	if h, err := a.Holder(ctx); err != nil || h != nil {
		t.Fatalf("fresh lease holder %+v %v", h, err)
	}
	mustAcquire := func(l *UpdateLease, want bool) {
		t.Helper()
		got, err := l.Acquire(ctx, "1.2.0")
		if err != nil || got != want {
			t.Fatalf("%s.Acquire = %v, %v; want %v", l.NodeID, got, err, want)
		}
	}
	mustAcquire(a, true)
	mustAcquire(b, false) // one node at a time
	h, _ := b.Holder(ctx)
	if h == nil || h.NodeID != "a" || h.Version != "1.2.0" || !h.AcquiredAt.Equal(now) || !h.ExpiresAt.Equal(now.Add(20*time.Minute)) {
		t.Fatalf("holder %+v", h)
	}

	// Renewal extends the lease but keeps the acquisition time.
	now = now.Add(15 * time.Minute)
	mustAcquire(a, true)
	h, _ = a.Holder(ctx)
	if !h.AcquiredAt.Equal(now.Add(-15*time.Minute)) || !h.ExpiresAt.Equal(now.Add(20*time.Minute)) {
		t.Fatalf("renewed holder %+v", h)
	}
	now = now.Add(19 * time.Minute)
	mustAcquire(b, false) // still within the renewed lease

	// A holder that stops renewing (died mid-update) loses the lease.
	now = now.Add(2 * time.Minute)
	if h, _ := b.Holder(ctx); h != nil {
		t.Fatalf("expired lease still reported: %+v", h)
	}
	mustAcquire(b, true)
	mustAcquire(a, false) // a cannot renew a lease b took over
	if err := a.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if h, _ := a.Holder(ctx); h == nil || h.NodeID != "b" {
		t.Fatalf("release by a non-holder changed the lease: %+v", h)
	}

	// Release hands over at once.
	if err := b.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if h, _ := a.Holder(ctx); h != nil {
		t.Fatalf("released lease still held: %+v", h)
	}
	mustAcquire(a, true)

	// A rolled-back release is skipped cluster-wide; a newer one is not.
	if err := a.ReportFailed(ctx, "1.2.0"); err != nil {
		t.Fatal(err)
	}
	if h, _ := a.Holder(ctx); h != nil {
		t.Fatalf("ReportFailed should release the reporter's lease: %+v", h)
	}
	if ok, err := b.Acquire(ctx, "1.2.0"); ok || !errors.Is(err, ErrVersionFailed) {
		t.Fatalf("failed version acquired: %v %v", ok, err)
	}
	if ok, err := b.Acquire(ctx, "1.2.1"); !ok || err != nil {
		t.Fatalf("newer version: %v %v", ok, err)
	}
	// ReportFailed by a non-holder keeps the holder's lease.
	if err := a.ReportFailed(ctx, "1.2.0"); err != nil {
		t.Fatal(err)
	}
	if h, _ := a.Holder(ctx); h == nil || h.NodeID != "b" {
		t.Fatalf("holder after foreign ReportFailed: %+v", h)
	}
}

func TestPeerAddrsSkipsDrainingNodes(t *testing.T) {
	now := time.Now()
	nodes := []Node{
		{ID: "self", Name: "a", Addrs: []string{"http://10.0.0.1:32400"}, LastSeen: now},
		{ID: "n2", Name: "b", Addrs: []string{"http://10.0.0.2:32400"}, LastSeen: now, Draining: true},
		{ID: "n3", Name: "c", Addrs: []string{"http://10.0.0.3:32400"}, LastSeen: now},
	}
	got := PeerAddrs(nodes, "self", now, time.Minute)
	if len(got) != 1 || got[0] != "http://10.0.0.3:32400" {
		t.Fatalf("peers %v", got)
	}
}
