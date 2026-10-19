package state

import (
	"context"
	"database/sql"
	"errors"
	"github.com/braedonsaunders/ether/internal/core"
	"testing"
	"time"
)

func TestImportEventsCommitAndDeduplicate(t *testing.T) {
	s, _ := newTestState(t)
	ctx := context.Background()
	key := core.MovieKey(42)
	viewed := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
	var events []Event
	s.Subscribe(func(ev Event) {
		// A subscriber must see committed rows, and may call State without deadlock.
		history, err := s.History(ctx, ev.UserID, time.Time{}, 100)
		if err != nil || len(history) != 1 {
			t.Errorf("event before commit: %v %+v", err, history)
		}
		events = append(events, ev)
	})
	r := ImportRecord{UserID: 1, Key: key, Views: 2, Rating: 8, ViewedAt: viewed}
	prepare := func(*sql.Tx) (ImportBatch, error) {
		return ImportBatch{Records: []ImportRecord{r}, History: []ImportRecord{r}}, nil
	}
	if err := s.ImportTransaction(ctx, prepare); err != nil {
		t.Fatal(err)
	}
	var completions, ratings int
	for _, ev := range events {
		if !ev.Imported {
			t.Fatal("import emitted a playback event")
		}
		if ev.Kind == EventScrobble {
			completions++
			if !ev.At.Equal(viewed) {
				t.Fatal("historical date lost")
			}
		}
		if ev.Kind == EventRated {
			ratings++
		}
	}
	if completions != 1 || ratings != 1 {
		t.Fatalf("events: %+v", events)
	}
	events = nil
	if err := s.ImportTransaction(ctx, prepare); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("repeated history emitted duplicate events: %+v", events)
	}
}

func TestImportRollbackDoesNotNotify(t *testing.T) {
	s, _ := newTestState(t)
	ctx := context.Background()
	count := 0
	s.Subscribe(func(Event) { count++ })
	err := s.ImportTransaction(ctx, func(*sql.Tx) (ImportBatch, error) {
		return ImportBatch{Records: []ImportRecord{{UserID: 1, Key: core.MovieKey(1), Views: 1}}, History: []ImportRecord{{UserID: 0, Key: core.MovieKey(2), ViewedAt: time.Now()}}}, nil
	})
	if err == nil || count != 0 {
		t.Fatalf("rollback events %d err %v", count, err)
	}
	history, err := s.History(ctx, 1, time.Time{}, 100)
	if err != nil || len(history) != 0 {
		t.Fatal("rolled-back import persisted", history, err)
	}
	err = s.ImportTransaction(ctx, func(*sql.Tx) (ImportBatch, error) { return ImportBatch{}, errors.New("list import failed") })
	if err == nil || count != 0 {
		t.Fatal("callback rollback notified subscribers")
	}
}
