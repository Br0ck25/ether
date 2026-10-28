package cluster

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestPostgresDrainingHandsOverLeadership: a leader that starts draining
// for an update heartbeats the flag (leaving the published union) and
// hands leadership to a node that stays up; StepDown releases the lock at
// once and for good.
func TestPostgresDrainingHandsOverLeadership(t *testing.T) {
	node := pgCluster(t)
	var drainA atomic.Bool
	a := node("a", func(o *Options) {
		o.Local.Draining = drainA.Load
		o.Local.Migrations = map[string]int{"cluster": 2, "state": 2}
		o.Local.Addrs = func(context.Context) []string { return []string{"http://10.0.0.1:32400"} }
	})
	waitFor(t, "a leads", 5*time.Second, a.IsLeader)
	b := node("b", func(o *Options) {
		o.Local.Addrs = func(context.Context) []string { return []string{"http://10.0.0.2:32400"} }
	})
	waitFor(t, "b sees a's addresses", 5*time.Second, func() bool {
		nodes, _ := b.Nodes(context.Background())
		return len(PeerAddrs(nodes, b.Self().ID, time.Now(), time.Minute)) == 1
	})

	drainA.Store(true)
	a.Beat()
	waitFor(t, "a hands over leadership", 5*time.Second, func() bool { return !a.IsLeader() && b.IsLeader() })
	nodes, err := b.Nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.ID == a.Self().ID && (!n.Draining || n.Migrations["state"] != 2) {
			t.Fatalf("a's heartbeat %+v", n)
		}
	}
	if peers := PeerAddrs(nodes, b.Self().ID, time.Now(), time.Minute); len(peers) != 0 {
		t.Fatalf("draining node still published: %v", peers)
	}
	time.Sleep(500 * time.Millisecond)
	if a.IsLeader() {
		t.Fatal("draining node re-acquired leadership while another node can lead")
	}

	// Drain ends (update postponed): a is published again and may lead
	// once b steps down.
	drainA.Store(false)
	a.Beat()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	start := time.Now()
	b.StepDown(ctx)
	cancel()
	if b.IsLeader() || time.Since(start) > 2*time.Second {
		t.Fatalf("StepDown did not release promptly (%v)", time.Since(start))
	}
	waitFor(t, "a takes over within an election interval", 3*time.Second, a.IsLeader)
	time.Sleep(500 * time.Millisecond)
	if b.IsLeader() {
		t.Fatal("a resigned node led again")
	}
}
