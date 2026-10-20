package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// A cancelled leading Resolve must not hand its context error to a
// concurrent caller of the same flight whose own context is still live.
func TestResolveWaiterSurvivesCancelledLeader(t *testing.T) {
	idx := &fakeIndexer{releases: movieReleases(), delay: 200 * time.Millisecond}
	svc := New(idx, fakeHealth{}, nil, testCfg(), Options{})
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := svc.Resolve(leaderCtx, core.ResolveRequest{Title: matrixTitle(), Client: webCaps()})
		leaderDone <- err
	}()
	for idx.searches.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	waiterDone := make(chan error, 1)
	var winner string
	go func() {
		res, err := svc.Resolve(context.Background(), core.ResolveRequest{Title: matrixTitle(), Client: webCaps()})
		if res != nil {
			winner = res.Release.ID
		}
		waiterDone <- err
	}()
	time.Sleep(20 * time.Millisecond) // the waiter joins the leader's flight
	cancelLeader()
	if err := <-leaderDone; err == nil {
		t.Fatal("leader should observe its own cancellation")
	}
	if err := <-waiterDone; err != nil || winner != "hd" {
		t.Fatalf("waiter got winner=%q err=%v", winner, err)
	}
}
