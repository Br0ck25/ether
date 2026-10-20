package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

func TestTwoNodesDeliverEachEventOnce(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	got := map[string]int{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e Event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			t.Error(err)
		}
		mu.Lock()
		got[e.Message]++
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer up.Close()
	path := filepath.Join(t.TempDir(), "ether.db")
	var nodes []*Service
	for _, id := range []string{"a", "b"} {
		db, err := store.OpenPath(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		nodes = append(nodes, New(db, Options{NodeID: id}))
	}
	if err := nodes[0].Save(ctx, Channel{ID: "hook", Kind: "webhook", Enabled: true, Endpoint: up.URL, Events: []string{"*"}, PerMinute: 60}); err != nil {
		t.Fatal(err)
	}
	const events = 20
	for i := 0; i < events; i++ {
		if err := nodes[i%2].Emit(ctx, Event{Kind: "test", Message: fmt.Sprint("event-", i)}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for _, s := range nodes {
		wg.Add(1)
		go func(s *Service) {
			defer wg.Done()
			for i := 0; i < events; i++ {
				if err := s.Drain(ctx); err != nil {
					t.Error(err)
					return
				}
			}
		}(s)
	}
	wg.Wait()
	if len(got) != events {
		t.Fatalf("delivered %d distinct events, want %d", len(got), events)
	}
	for m, n := range got {
		if n != 1 {
			t.Fatalf("%s delivered %d times", m, n)
		}
	}
}

func TestAbandonedClaimIsFailedNotResent(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	s := New(db, Options{Now: func() time.Time { return now }})
	if _, err := db.Exec(`INSERT INTO notify_queue(channel,event,created,due,status,attempted,claimed_by,claimed_at) VALUES('x','{}',?,?,'sending',?,'gone',?)`,
		now.Unix(), now.Unix(), now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	status := func() string {
		var st string
		if err := db.QueryRow(`SELECT status FROM notify_queue`).Scan(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if err := s.Drain(ctx); err != nil || status() != "sending" {
		t.Fatalf("fresh claim touched: %v %s", err, status())
	}
	now = now.Add(claimTimeout + time.Second)
	if err := s.Drain(ctx); err != nil || status() != "failed" {
		t.Fatalf("abandoned claim: %v %s", err, status())
	}
}
