package trakt

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/state"
)

func importQueueCount(t *testing.T, c *Client) int {
	t.Helper()
	var n int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM trakt_import_queue`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBulkImportSurvivesQueueLimitFailureAndWorkerRestart(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{}
	c := newTestClient(t, f)
	connect(t, c, 1)
	c.qsize = 2
	st := state.New(c.db, state.Options{})
	unsub := c.SubscribeTo(st)
	defer unsub()
	const count = 1205 // larger than the default live event queue
	at := time.Date(2022, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := st.ImportTransaction(ctx, func(*sql.Tx) (state.ImportBatch, error) {
		b := state.ImportBatch{}
		for i := range count {
			b.History = append(b.History, state.ImportRecord{UserID: 1, Key: core.MovieKey(int64(i + 1)), ViewedAt: at})
		}
		return b, nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := importQueueCount(t, c); n != count || len(c.queue) != 0 {
		t.Fatalf("pending imports=%d live queue=%d", n, len(c.queue))
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer failing.Close()
	base := c.base
	c.base = failing.URL
	if err := c.drainImports(ctx); err == nil || importQueueCount(t, c) != count {
		t.Fatal("temporary failure must retain the entire import")
	}
	c.Close()
	// A fresh worker loads persisted records without an in-memory notification.
	resumed := New(Options{Config: c.cfg, DB: c.db, BaseURL: base, MinPostInterval: -1})
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { resumed.Start(workerCtx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(8 * time.Second)
	for importQueueCount(t, resumed) > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := importQueueCount(t, resumed); n != 0 {
		t.Fatalf("worker left %d records", n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.history) != count || len(f.scrobbles) != 0 {
		t.Fatalf("history=%d active scrobbles=%d", len(f.history), len(f.scrobbles))
	}
	for _, h := range f.history {
		movie := h["movies"].([]any)[0].(map[string]any)
		if movie["watched_at"] != at.Format(time.RFC3339) {
			t.Fatalf("historical date changed: %v", movie)
		}
	}
}

func TestImportQueueSkipsProgressAndDoesNotCrossAccounts(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{}
	c := newTestClient(t, f)
	connect(t, c, 1)
	ev := state.Event{UserID: 1, Key: core.MovieKey(949), Imported: true, At: time.Now()}
	ev.Kind = state.EventProgress
	c.enqueueImport(ev)
	ev.Kind, ev.At = state.EventScrobble, time.Time{}
	c.enqueueImport(ev)
	if importQueueCount(t, c) != 0 {
		t.Fatal("partial progress or undated history queued")
	}
	ev.At = time.Now()
	c.enqueueImport(ev)
	// A pending live event interrupts historical delivery before another POST.
	c.queue = []state.Event{{Kind: state.EventProgress, UserID: 1, Key: ev.Key}}
	if err := c.drainImports(ctx); err != nil || importQueueCount(t, c) != 1 || f.count("/sync/history") != 0 {
		t.Fatalf("bulk history starved live playback: %v", err)
	}
	c.queue = nil
	tokens, _, _ := c.Tokens(ctx, 1)
	tokens.Username = "different-account"
	if err := c.saveTokens(ctx, tokens); err != nil {
		t.Fatal(err)
	}
	if err := c.drainImports(ctx); err != nil || importQueueCount(t, c) != 0 || f.count("/sync/history") != 0 {
		t.Fatalf("account change transferred history: %v", err)
	}
	c.enqueueImport(ev)
	if err := c.Disconnect(ctx, 1); err != nil || importQueueCount(t, c) != 0 {
		t.Fatalf("disconnect retained pending history: %v", err)
	}
}
