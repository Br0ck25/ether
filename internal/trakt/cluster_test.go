package trakt

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/state"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestTwoNodesImportEachRecordOnce(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "ether.db")
	var nodes []*Client
	for _, id := range []string{"a", "b"} {
		db, err := store.OpenPath(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		nodes = append(nodes, New(Options{
			Config: config.Trakt{Enabled: true, ClientID: "cid", ClientSecret: "sec"},
			DB:     db, BaseURL: srv.URL, MaxRetryAfter: time.Second, MinPostInterval: -1, NodeID: id,
		}))
	}
	connect(t, nodes[0], 1)
	const count = 40
	at := time.Date(2022, 2, 3, 4, 5, 6, 0, time.UTC)
	for i := 0; i < count; i++ {
		nodes[i%2].enqueueImport(state.Event{Kind: state.EventScrobble, UserID: 1, Key: core.MovieKey(int64(i + 1)), At: at, Imported: true})
	}
	if n := importQueueCount(t, nodes[0]); n != count {
		t.Fatalf("queued %d", n)
	}
	var wg sync.WaitGroup
	for _, c := range nodes {
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			if err := c.drainImports(ctx); err != nil {
				t.Error(err)
			}
		}(c)
	}
	wg.Wait()
	if n := importQueueCount(t, nodes[1]); n != 0 {
		t.Fatalf("left %d records", n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[any]int{}
	for _, h := range f.history {
		movie := h["movies"].([]any)[0].(map[string]any)
		seen[movie["ids"].(map[string]any)["tmdb"]]++
	}
	if len(f.history) != count || len(seen) != count {
		t.Fatalf("history posts %d, distinct %d, want %d", len(f.history), len(seen), count)
	}
}

func TestImportClaimHeldByLiveNodeIsSkipped(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{}
	c := newTestClient(t, f)
	connect(t, c, 1)
	c.enqueueImport(state.Event{Kind: state.EventScrobble, UserID: 1, Key: core.MovieKey(3), At: time.Now(), Imported: true})
	if _, err := c.db.Exec(`UPDATE trakt_import_queue SET claimed_by='other', claimed_at=?`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := c.drainImports(ctx); err != nil || importQueueCount(t, c) != 1 || f.count("/sync/history") != 0 {
		t.Fatalf("claimed record taken: %v", err)
	}
	// The other node went away: its claim expires and the record is retried.
	if _, err := c.db.Exec(`UPDATE trakt_import_queue SET claimed_at=?`, time.Now().Add(-importClaimTimeout-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := c.drainImports(ctx); err != nil || importQueueCount(t, c) != 0 || f.count("/sync/history") != 1 {
		t.Fatalf("expired claim not retried: %v", err)
	}
}
