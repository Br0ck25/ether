package stream

import (
	"context"
	"errors"
	"github.com/braedonsaunders/ether/internal/core"
	"sync"
	"testing"
	"time"
)

type heldHealthFetcher struct {
	*fakeFetcher
	started, canceled, release chan struct{}
	once                       sync.Once
}

func (f *heldHealthFetcher) BodyPart(ctx context.Context, id string, groups []string, prio core.Priority) ([]byte, core.ArticlePart, error) {
	f.once.Do(func() { close(f.started) })
	select {
	case <-ctx.Done():
		close(f.canceled)
		<-f.release
		return nil, core.ArticlePart{}, ctx.Err()
	case <-f.release:
		return f.fakeFetcher.BodyPart(ctx, id, groups, prio)
	}
}
func TestHealthCancellationJoinsOnlyItsLastWaiter(t *testing.T) {
	for _, health := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "health"}[health], func(t *testing.T) {
			f := &heldHealthFetcher{fakeFetcher: newFakeFetcher(), started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			defer release.Do(func() { close(f.release) })
			doc := post(t, f.fakeFetcher, 10000, postFile{name: "test.mkv", data: mkvBytes(1, 10000)})
			e := newTestEngine(t, f, nil, Options{})
			rel, err := e.acquire(doc)
			if err != nil {
				t.Fatal(err)
			}
			defer e.release(rel)
			parent := t.Context()
			if health {
				parent = context.WithValue(parent, healthWorkKey{}, true)
			}
			ctx, cancel := context.WithCancel(parent)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, e := e.segment(ctx, rel, rel.files[0], 0, core.PriorityLive, fetchOpts{}); done <- e }()
			<-f.started
			cancel()
			<-f.canceled
			if health {
				select {
				case <-done:
					t.Fatal("health returned before its worker stopped")
				case <-time.After(20 * time.Millisecond):
				}
			}
			if !health {
				select {
				case err = <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("ordinary reader waited on worker cleanup")
				}
			}
			release.Do(func() { close(f.release) })
			if health {
				select {
				case err = <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("health never joined")
				}
			}
		})
	}
}
func TestHealthCancellationDoesNotCancelSharedLiveWaiter(t *testing.T) {
	f := &heldHealthFetcher{fakeFetcher: newFakeFetcher(), started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(f.release) })
	doc := post(t, f.fakeFetcher, 10000, postFile{name: "test.mkv", data: mkvBytes(1, 10000)})
	e := newTestEngine(t, f, nil, Options{})
	rel, err := e.acquire(doc)
	if err != nil {
		t.Fatal(err)
	}
	defer e.release(rel)
	health, cancel := context.WithCancel(context.WithValue(t.Context(), healthWorkKey{}, true))
	defer cancel()
	hdone, ldone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := e.segment(health, rel, rel.files[0], 0, core.PriorityLive, fetchOpts{})
		hdone <- err
	}()
	<-f.started
	go func() {
		_, err := e.segment(t.Context(), rel, rel.files[0], 0, core.PriorityLive, fetchOpts{})
		ldone <- err
	}()
	deadline := time.Now().Add(time.Second)
	for e.coalesced.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("live waiter did not join")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err = <-hdone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("health waited on another reader")
	}
	select {
	case <-f.canceled:
		t.Fatal("shared live work canceled")
	default:
	}
	release.Do(func() { close(f.release) })
	select {
	case err = <-ldone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("live waiter did not finish")
	}
}
