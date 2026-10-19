package state

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// twoNodes returns two States on separate handles to one database file, as
// two cluster nodes would see it.
func twoNodes(t *testing.T) (*State, *State) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ether.db")
	open := func() *State {
		db, err := store.OpenPath(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return New(db, Options{})
	}
	return open(), open()
}

func TestScrobbleOnTwoNodesLosesNoViews(t *testing.T) {
	a, b := twoNodes(t)
	key := core.MovieKey(7)
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		for _, s := range []*State{a, b} {
			wg.Add(1)
			go func(s *State) {
				defer wg.Done()
				errs <- s.Scrobble(ctx, 1, key)
			}(s)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	ws, err := b.Get(ctx, 1, key)
	if err != nil || ws.ViewCount != 40 {
		t.Fatalf("view count %d, want 40 (%v)", ws.ViewCount, err)
	}
}

func TestUpdateRetriesAfterConcurrentChange(t *testing.T) {
	s, _ := newTestState(t)
	key := core.MovieKey(8)
	if err := s.Scrobble(ctx, 1, key); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err := s.update(ctx, 1, []core.RatingKey{key}, func(cur *row, now time.Time) (bool, bool, []Event) {
		calls++
		if calls == 1 {
			cur.version-- // as if another node wrote after our read
		}
		cur.ws.ViewCount++
		return false, false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := s.Get(ctx, 1, key)
	if calls != 2 || ws.ViewCount != 2 {
		t.Fatalf("calls=%d views=%d", calls, ws.ViewCount)
	}
}

func TestSaveRejectsStaleRow(t *testing.T) {
	s, _ := newTestState(t)
	key := core.MovieKey(9)
	if err := s.Scrobble(ctx, 1, key); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stale := row{ws: core.WatchStatus{Key: key}, exists: true, version: 99}
	if err := saveTx(ctx, tx, 1, stale); !errors.Is(err, errConflict) {
		t.Fatalf("stale version: %v", err)
	}
	// A row another writer created since we saw it absent.
	if err := saveTx(ctx, tx, 1, row{ws: core.WatchStatus{Key: key}}); !errors.Is(err, errConflict) {
		t.Fatalf("lost insert race: %v", err)
	}
}

func TestPlaylistEditsOnTwoNodesKeepEveryItem(t *testing.T) {
	a, b := twoNodes(t)
	p, err := a.Create(ctx, 1, "Mix", nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		for j, s := range []*State{a, b} {
			wg.Add(1)
			go func(s *State, k core.RatingKey) {
				defer wg.Done()
				errs <- s.Add(ctx, 1, p.Key, []core.RatingKey{k})
			}(s, core.MovieKey(int64(1+i*2+j)))
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := b.GetPlaylist(ctx, 1, p.Key)
	if err != nil || len(got.Items) != 40 {
		t.Fatalf("items %d, want 40 (%v)", len(got.Items), err)
	}
	if err := b.Add(ctx, 2, p.Key, []core.RatingKey{core.MovieKey(99)}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("other user's playlist: %v", err)
	}
}

func TestImportOnTwoNodesRecordsHistoryOnce(t *testing.T) {
	a, b := twoNodes(t)
	at := time.Date(2025, 5, 1, 20, 0, 0, 0, time.UTC)
	batch := ImportBatch{History: []ImportRecord{{UserID: 1, Key: core.MovieKey(5), ViewedAt: at}}}
	var mu sync.Mutex
	events := 0
	for _, s := range []*State{a, b} {
		s.Subscribe(func(ev Event) {
			if ev.Kind == EventScrobble {
				mu.Lock()
				events++
				mu.Unlock()
			}
		})
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, s := range []*State{a, b} {
		wg.Add(1)
		go func(s *State) {
			defer wg.Done()
			errs <- s.ImportTransaction(ctx, func(*sql.Tx) (ImportBatch, error) { return batch, nil })
		}(s)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM state_history WHERE user_id=1`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("history rows %d (%v)", n, err)
	}
	if events != 1 {
		t.Fatalf("scrobble events %d, want 1", events)
	}
	// The index itself refuses a duplicate imported row.
	if _, err := a.db.Exec(`INSERT INTO state_history(user_id,rating_key,viewed_at,completed,imported) VALUES(1,?,?,1,1)`,
		int64(core.MovieKey(5)), at.UnixNano()); err == nil {
		t.Fatal("duplicate imported history accepted")
	}
}
