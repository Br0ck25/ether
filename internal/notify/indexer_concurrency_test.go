package notify

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/newznab"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/usenet"
)

func TestBlockedIndexerAlertDoesNotBlockOtherHealthEvents(t *testing.T) {
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	svc := New(db, Options{})
	done := make(chan struct{})
	go func() { defer close(done); svc.ObserveIndexer(newznab.Event{Indexer: "blocked", Kind: "search"}) }()
	defer func() { conn.Close(); <-done }()
	deadline := time.Now().Add(time.Second)
	for db.Stats().WaitCount == 0 {
		if time.Now().After(deadline) {
			t.Fatal("alert did not reach database")
		}
		time.Sleep(time.Millisecond)
	}
	other := make(chan struct{})
	go func() {
		svc.ObserveIndexer(newznab.Event{Indexer: "healthy", Kind: "search", Success: true})
		close(other)
	}()
	select {
	case <-other:
	case <-time.After(time.Second):
		t.Error("blocked queue write held all health events")
	}
	conn.Close()
	<-other
}

func TestBlockedProviderSampleDoesNotBlockIndexerEvents(t *testing.T) {
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	svc := New(db, Options{Providers: func() []usenet.ProviderStats {
		return []usenet.ProviderStats{{ID: "blocked", State: usenet.StateBackoff}}
	}})
	done := make(chan struct{})
	go func() { defer close(done); _ = svc.SampleHealth(context.Background()) }()
	defer func() { conn.Close(); <-done }()
	deadline := time.Now().Add(time.Second)
	for db.Stats().WaitCount == 0 {
		if time.Now().After(deadline) {
			t.Fatal("provider alert did not reach database")
		}
		time.Sleep(time.Millisecond)
	}
	other := make(chan struct{})
	go func() {
		svc.ObserveIndexer(newznab.Event{Indexer: "healthy", Kind: "search", Success: true})
		close(other)
	}()
	select {
	case <-other:
	case <-time.After(time.Second):
		t.Error("provider queue write blocked indexer health event")
	}
	conn.Close()
	<-other
}
