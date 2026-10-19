package playback

import (
	"context"
	"errors"
	"github.com/braedonsaunders/ether/internal/core"
	"testing"
	"time"
)

func TestChannelProgrammeOverlapCountsAsOneBoundedStream(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 10000, time.Minute)}, []string{"A"}, func(h *harness, o *Options) { h.cfg.Server.DefaultStreamLimit = 1; h.cfg.Server.MaxStreams = 1 })
	ctx := context.Background()
	group := "0123456789abcdef0123456789abcdef"
	var sessions []*core.PlaySession
	for i, key := range []core.RatingKey{movieKey, movie2, movie3} {
		caps := defaultCaps(string(rune('a' + i)))
		ps, err := h.m.PrepareChannel(ctx, owner, key, caps, group)
		if err != nil {
			t.Fatal(err)
		}
		if ps.PlaybackGroup != group {
			t.Fatal("group not carried on snapshot")
		}
		sessions = append(sessions, ps)
	}
	if _, err := h.m.PrepareChannel(ctx, owner, epKey, defaultCaps("fourth"), group); !errors.Is(err, core.ErrStreamLimit) {
		t.Fatalf("unbounded overlap: %v", err)
	}
	if _, err := h.m.Prepare(ctx, owner, epKey, defaultCaps("another-viewer")); !errors.Is(err, core.ErrStreamLimit) {
		t.Fatalf("ordinary stream bypass: %v", err)
	}
	if _, err := h.m.Prepare(ctx, friend, movieKey, defaultCaps("friend")); !errors.Is(err, core.ErrStreamLimit) {
		t.Fatalf("server limit bypass: %v", err)
	}
	h.m.Stop(sessions[0].ID)
	if _, err := h.m.PrepareChannel(ctx, owner, epKey, defaultCaps("fourth"), group); err != nil {
		t.Fatalf("did not free programme overlap: %v", err)
	}
	if len(h.m.Sessions()) != 3 {
		t.Fatal("programmes replaced instead of overlapping")
	}
}

func TestPassthroughIPTVReservesAgainstLaterVOD(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 10000, time.Minute)}, []string{"A"}, func(h *harness, o *Options) { h.cfg.Server.DefaultStreamLimit = 1; h.cfg.Server.MaxStreams = 1 })
	group := "abcdef0123456789abcdef0123456789"
	if err := h.m.BeginChannel(owner, defaultCaps("iptv"), group); err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.Prepare(context.Background(), owner, movieKey, defaultCaps("vod")); !errors.Is(err, core.ErrStreamLimit) {
		t.Fatalf("VOD bypassed IPTV reservation: %v", err)
	}
	if err := h.m.BeginChannel(friend, defaultCaps("other"), "1234567890abcdef1234567890abcdef"); !errors.Is(err, core.ErrStreamLimit) {
		t.Fatalf("server limit bypass: %v", err)
	}
	if _, err := h.m.PrepareChannel(context.Background(), owner, movieKey, defaultCaps("programme"), group); err != nil {
		t.Fatal("source in reserved group rejected", err)
	}
	h.m.EndChannel(group)
	for _, s := range h.m.Sessions() {
		h.m.Stop(s.ID)
	}
	if _, err := h.m.Prepare(context.Background(), owner, movieKey, defaultCaps("vod")); err != nil {
		t.Fatalf("reservation did not release: %v", err)
	}
}
