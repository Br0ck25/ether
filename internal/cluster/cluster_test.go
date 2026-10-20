package cluster

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLoadIdentityPersists(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.NodeID) != 32 || a.Name == "" {
		t.Fatalf("identity %+v", a)
	}
	b, err := LoadIdentity(dir)
	if err != nil || b.NodeID != a.NodeID {
		t.Fatalf("reload %+v err=%v, want id %s", b, err, a.NodeID)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, NodeFile))
	if strings.Contains(string(raw), `"name"`) {
		t.Fatalf("hostname default should not be persisted: %s", raw)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(filepath.Join(dir, NodeFile)); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode())
		}
	}
	// An owner-chosen name is kept.
	if err := os.WriteFile(filepath.Join(dir, NodeFile), []byte(`{"nodeId":"abc","name":" den "}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadIdentity(dir)
	if err != nil || c.NodeID != "abc" || c.Name != "den" {
		t.Fatalf("%+v %v", c, err)
	}
	if err := os.WriteFile(filepath.Join(dir, NodeFile), []byte(`{nope`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIdentity(dir); err == nil {
		t.Fatal("corrupt node.json must not be replaced silently")
	}
}

func TestEnvelopeRoundTripAndLimits(t *testing.T) {
	for _, payload := range [][]byte{nil, []byte(`{"a":"<b>&"}`), {0, 1, 2, 0xff}, []byte("héllo")} {
		s, err := encodeEnvelope("invalidate:auth", "n1", "i1", payload)
		if err != nil {
			t.Fatal(err)
		}
		env, got, err := decodeEnvelope(s)
		if err != nil || env.Topic != "invalidate:auth" || env.From != "n1" || env.Inc != "i1" || string(got) != string(payload) {
			t.Fatalf("round trip %q -> %+v %q %v", payload, env, got, err)
		}
	}
	if _, err := encodeEnvelope("t", "n", "i", make([]byte, MaxPayload+1)); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
	// Text that escapes heavily can exceed NOTIFY's limit while under
	// MaxPayload.
	if _, err := encodeEnvelope("t", "n", "i", []byte(strings.Repeat("\x01", MaxPayload))); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("escaped oversize: %v", err)
	}
	s, err := encodeEnvelope("t", "n", "i", []byte(strings.Repeat("x", MaxPayload)))
	if err != nil || len(s) > maxNotify {
		t.Fatalf("max payload: len=%d err=%v", len(s), err)
	}
	for _, topic := range []string{"", strings.Repeat("t", maxTopic+1), "a\x00b"} {
		if _, err := encodeEnvelope(topic, "n", "i", nil); !errors.Is(err, ErrBadTopic) {
			t.Fatalf("topic %q: %v", topic, err)
		}
	}
}

func TestSolo(t *testing.T) {
	var load atomic.Int64
	load.Store(3)
	s := NewSolo(Local{
		Identity: Identity{NodeID: "n1", Name: "box"}, Version: "1.2.3",
		Addrs: func(context.Context) []string { return []string{"http://10.0.0.2:32400"} },
		Caps:  func() Caps { return Caps{Transcode: true, HWAccel: "nvenc"} },
		Load:  func() int { return int(load.Load()) },
	}, nil)
	var c Cluster = s
	if c.Mode() != ModeSolo || !c.IsLeader() || c.Epoch() != 1 {
		t.Fatal("solo is always the leader")
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	self := c.Self()
	if self.ID != "n1" || self.Name != "box" || self.Version != "1.2.3" || !self.Leader || !self.Live ||
		self.Load != 3 || !self.Caps.Transcode || len(self.Addrs) != 1 {
		t.Fatalf("self %+v", self)
	}
	nodes, err := c.Nodes(context.Background())
	if err != nil || len(nodes) != 1 || nodes[0].ID != "n1" {
		t.Fatalf("nodes %+v %v", nodes, err)
	}

	// RunAsLeader runs fn directly with the caller's context.
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "v")
	ran := false
	c.RunAsLeader(ctx, "job", func(jctx context.Context) { ran = jctx.Value(ctxKey{}) == "v" })
	if !ran {
		t.Fatal("RunAsLeader did not run fn with ctx")
	}

	// In-process bus.
	var got []string
	unsub := c.Subscribe("timeline", func(p []byte) { got = append(got, string(p)) })
	c.Subscribe("other", func([]byte) { t.Error("wrong topic delivered") })
	c.Subscribe("timeline", func([]byte) { panic("boom") }) // isolated
	if err := c.Publish(ctx, "timeline", []byte("a")); err != nil {
		t.Fatal(err)
	}
	unsub()
	unsub()
	_ = c.Publish(ctx, "timeline", []byte("b"))
	if strings.Join(got, ",") != "a" {
		t.Fatalf("got %v", got)
	}
	if err := c.Publish(ctx, "timeline", make([]byte, MaxPayload+1)); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("solo must enforce the payload limit: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRunAsLeaderFollowsLeadership(t *testing.T) {
	l := newLeadership()
	log := discardLogger()
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	starts, stops := 0, 0
	running := false
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAsLeader(ctx, log, "job", l, func(jctx context.Context) {
			mu.Lock()
			starts++
			running = true
			mu.Unlock()
			<-jctx.Done()
			mu.Lock()
			stops++
			running = false
			mu.Unlock()
		})
	}()
	state := func() (int, int, bool) { mu.Lock(); defer mu.Unlock(); return starts, stops, running }
	time.Sleep(30 * time.Millisecond)
	if s, _, _ := state(); s != 0 {
		t.Fatal("started without leadership")
	}
	l.set(true, 7)
	waitFor(t, "start", time.Second, func() bool { _, _, r := state(); return r })
	l.set(false, 0)
	waitFor(t, "stop", time.Second, func() bool { s, st, r := state(); return s == 1 && st == 1 && !r })
	l.set(true, 8)
	waitFor(t, "restart", time.Second, func() bool { s, _, r := state(); return s == 2 && r })
	// A lose/regain flip faster than the runner observes still restarts.
	l.set(false, 0)
	l.set(true, 9)
	waitFor(t, "restart after flip", time.Second, func() bool { s, st, r := state(); return s == 3 && st == 2 && r })
	cancel()
	<-done
	if _, st, r := state(); st != 3 || r {
		t.Fatal("fn not stopped on ctx cancel")
	}
}

func TestRunAsLeaderDoesNotRestartFinishedJob(t *testing.T) {
	l := newLeadership()
	l.set(true, 1)
	ctx, cancel := context.WithCancel(context.Background())
	var runs atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAsLeader(ctx, discardLogger(), "once", l, func(context.Context) { runs.Add(1) })
	}()
	waitFor(t, "first run", time.Second, func() bool { return runs.Load() == 1 })
	time.Sleep(30 * time.Millisecond)
	if runs.Load() != 1 {
		t.Fatal("finished job restarted while still leading")
	}
	l.set(false, 0)
	l.set(true, 2)
	waitFor(t, "rerun after regain", time.Second, func() bool { return runs.Load() == 2 })
	cancel()
	<-done
}

func TestPeerAddrs(t *testing.T) {
	now := time.Now()
	nodes := []Node{
		{ID: "self", Name: "a", Addrs: []string{"http://10.0.0.1:32400"}, LastSeen: now},
		{ID: "z", Name: "zeta", Addrs: []string{"http://10.0.0.9:32400", "http://203.0.113.1:32400"}, LastSeen: now},
		{ID: "b", Name: "beta", Addrs: []string{"http://10.0.0.2:32400", "http://203.0.113.1:32400"}, LastSeen: now.Add(-50 * time.Second)},
		{ID: "dead", Name: "alpha", Addrs: []string{"http://10.0.0.3:32400"}, LastSeen: now.Add(-61 * time.Second)},
	}
	got := strings.Join(PeerAddrs(nodes, "self", now, time.Minute), ",")
	want := "http://10.0.0.2:32400,http://203.0.113.1:32400,http://10.0.0.9:32400"
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}
