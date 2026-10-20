package playback

import (
	"context"
	"testing"
	"time"
)

func TestPlayPromotesInFlightWarmPastSpeculativeBudget(t *testing.T) {
	parent, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	live, stopPlay := context.WithTimeout(parent, 2*time.Second)
	defer stopPlay()
	foreground := make(chan context.Context, 1)
	// The actual Play flight is already waiting when warm work starts.
	foreground <- live
	ctx, cancel := warmPreparationContext(parent, 20*time.Millisecond, foreground)
	defer cancel()
	// A probe completing after the speculative limit should still be usable.
	select {
	case <-ctx.Done():
		t.Fatal("Play lost its in-flight warm work to the speculative timeout")
	case <-time.After(80 * time.Millisecond):
	}
	stopPlay()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("abandoned foreground work kept running")
	}
}

func TestUnclaimedWarmStillExpiresAndShutdownCancelsPromotedWarm(t *testing.T) {
	ctx, cancel := warmPreparationContext(t.Context(), 20*time.Millisecond, nil)
	defer cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("speculative work outlived its budget")
	}
	parent, shutdown := context.WithCancel(context.Background())
	foreground := make(chan context.Context, 1)
	foreground <- context.Background()
	promoted, stop := warmPreparationContext(parent, time.Minute, foreground)
	defer stop()
	shutdown()
	select {
	case <-promoted.Done():
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel promoted work")
	}
}
