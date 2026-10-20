package clusterbus

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/cluster"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

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

type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) apply(_ context.Context, hints []string, all bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if all {
		r.calls = append(r.calls, "*")
		return
	}
	r.calls = append(r.calls, strings.Join(hints, ","))
}

func (r *recorder) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func TestInvalidatorSkipsOwnEventsAndReachesPeers(t *testing.T) {
	hub := NewHub()
	var ra, rb recorder
	a := NewInvalidator(hub.Node("a"), "auth", quiet(), ra.apply)
	b := NewInvalidator(hub.Node("b"), "auth", quiet(), rb.apply)
	defer a.Close()
	defer b.Close()

	a.Notify("token:abc")
	waitFor(t, "b applies", func() bool { return len(rb.get()) == 1 })
	if got := rb.get()[0]; got != "token:abc" {
		t.Fatalf("hint = %q", got)
	}
	time.Sleep(20 * time.Millisecond)
	if len(ra.get()) != 0 {
		t.Fatalf("publisher reloaded on its own event: %v", ra.get())
	}
	// No hint means everything.
	b.Notify("")
	waitFor(t, "a applies all", func() bool { return len(ra.get()) == 1 })
	if ra.get()[0] != "*" {
		t.Fatalf("got %v", ra.get())
	}
	// A bus resync drops everything on that node only.
	hub.Resync("b")
	waitFor(t, "b resync", func() bool { return len(rb.get()) == 2 })
	if rb.get()[1] != "*" || len(ra.get()) != 1 {
		t.Fatalf("a=%v b=%v", ra.get(), rb.get())
	}
}

func TestInvalidatorNilAndOtherComponentsIgnored(t *testing.T) {
	var iv *Invalidator
	iv.Notify("x") // must not panic
	iv.Close()
	if NewInvalidator(nil, "auth", nil, func(context.Context, []string, bool) {}) != nil {
		t.Fatal("nil bus must give a nil invalidator")
	}
	hub := NewHub()
	var r recorder
	b := NewInvalidator(hub.Node("b"), "safety", quiet(), r.apply)
	defer b.Close()
	a := NewInvalidator(hub.Node("a"), "auth", quiet(), func(context.Context, []string, bool) {})
	defer a.Close()
	a.Notify("1")
	time.Sleep(20 * time.Millisecond)
	if len(r.get()) != 0 {
		t.Fatalf("other component's event applied: %v", r.get())
	}
}

func TestInvalidatorCoalescesBursts(t *testing.T) {
	hub := NewHub()
	block := make(chan struct{})
	var r recorder
	first := true
	b := NewInvalidator(hub.Node("b"), "c", quiet(), func(ctx context.Context, h []string, all bool) {
		if first {
			first = false
			<-block
		}
		r.apply(ctx, h, all)
	})
	defer b.Close()
	a := hub.Node("a")
	pub := func(h string) { _ = a.Publish(context.Background(), "invalidate:c", Tag("x", []byte(h))) }
	pub("1")
	time.Sleep(10 * time.Millisecond)
	for i := 0; i < 3; i++ {
		pub("2")
	}
	pub("3")
	close(block)
	waitFor(t, "two applies", func() bool { return b.Applied() == 2 })
	if got := r.get(); len(got) != 2 || got[0] != "1" || got[1] != "2,3" {
		t.Fatalf("calls %v", got)
	}
	// Too many distinct hints degrade to all (queue only; no worker).
	iv := &Invalidator{hints: map[string]bool{}, kick: make(chan struct{}, 1)}
	for i := 0; i < maxHints+5; i++ {
		iv.queue(strings.Repeat("k", i+1), false)
	}
	if !iv.all || len(iv.hints) != 0 {
		t.Fatalf("all=%v hints=%d", iv.all, len(iv.hints))
	}
}

func TestTagRoundTripAndHubLimits(t *testing.T) {
	o, p := Untag(Tag("me", []byte("a\nb")))
	if o != "me" || string(p) != "a\nb" {
		t.Fatalf("%q %q", o, p)
	}
	if o, p := Untag([]byte("bare")); o != "bare" || p != nil {
		t.Fatalf("%q %q", o, p)
	}
	hub := NewHub()
	n := hub.Node("a")
	if err := n.Publish(context.Background(), "t", make([]byte, MaxPayload+1)); err == nil {
		t.Fatal("oversized payload accepted")
	}
	hub.Node("b")
	hub.Kill("b")
	live, _ := n.LiveNodes(context.Background())
	if !live["a"] || live["b"] {
		t.Fatalf("live %v", live)
	}
}

func TestFromClusterSolo(t *testing.T) {
	solo := cluster.NewSolo(cluster.Local{Identity: cluster.Identity{NodeID: "n1", Name: "one"}}, quiet())
	n := FromCluster(solo)
	if n.NodeID() != "n1" {
		t.Fatal(n.NodeID())
	}
	live, err := n.LiveNodes(context.Background())
	if err != nil || !live["n1"] {
		t.Fatalf("%v %v", live, err)
	}
	got := make(chan string, 1)
	defer n.Subscribe("x", func(p []byte) { got <- string(p) })()
	if err := n.Publish(context.Background(), "x", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if v := <-got; v != "hi" {
		t.Fatal(v)
	}
}
