package tmdb

import (
	"context"
	"testing"
	"time"
)

func TestSetRateShare(t *testing.T) {
	c := New("k", nil)
	defer c.Close()
	check := func(nodes int, rate float64, burst int) {
		t.Helper()
		c.SetRateShare(nodes)
		if r, b := c.RateLimit(); r != rate || b != burst {
			t.Fatalf("SetRateShare(%d) = %v/s burst %d, want %v/s burst %d", nodes, r, b, rate, burst)
		}
	}
	check(1, 40, 40)
	check(2, 20, 20)
	check(4, 10, 10)
	check(20, MinClusterRate, MinClusterRate) // floor
	check(0, 40, 40)

	// The floor never raises a configured rate that is already lower.
	slow := New("k", nil, WithRateLimit(2, 1))
	defer slow.Close()
	slow.SetRateShare(4)
	if r, b := slow.RateLimit(); r != 2 || b != 1 {
		t.Fatalf("slow client: %v/s burst %d", r, b)
	}
}

func TestRateLimiterFollowsRateChanges(t *testing.T) {
	r := newRateLimiter(50, 1)
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 6; i++ { // 1 from the burst + 5 at 50/s ≈ 100ms
		if err := r.wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el < 80*time.Millisecond {
		t.Fatalf("50/s limiter too fast: %v", el)
	}
	r.setRate(5000, 1)
	start = time.Now()
	for i := 0; i < 20; i++ {
		if err := r.wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el > 80*time.Millisecond {
		t.Fatalf("raised rate not applied: %v", el)
	}
}

func TestRateLimiterCancelReturnsReservation(t *testing.T) {
	r := newRateLimiter(1, 1)
	if err := r.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := r.wait(ctx); err == nil {
		t.Fatal("want a context error while the bucket is empty")
	}
	r.mu.Lock()
	tokens := r.tokens
	r.mu.Unlock()
	if tokens < -0.1 {
		t.Fatalf("cancelled reservation kept: tokens %v", tokens)
	}
}
