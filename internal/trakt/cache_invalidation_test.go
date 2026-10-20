package trakt

import (
	"context"
	"testing"
)

func TestInvalidationPreventsInflightAccountDataFromRepopulatingCache(t *testing.T) {
	c := New(Options{})
	defer c.Close()
	key := cacheKey("watchlist", 1, 50)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_, _ = cachedCall(c, context.Background(), key, func(context.Context) (string, error) {
			close(started)
			<-release
			return "old account", nil
		})
	}()
	<-started
	c.invalidateUser(1) // reconnect or a newly committed watch event
	if _, err := cachedCall(c, context.Background(), key, func(context.Context) (string, error) { return "new account", nil }); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	got, err := cachedCall(c, context.Background(), key, func(context.Context) (string, error) { return "new account", nil })
	if err != nil || got != "new account" {
		t.Fatalf("old in-flight response overwrote invalidation: %q, %v", got, err)
	}
}
