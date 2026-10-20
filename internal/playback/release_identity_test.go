package playback

import (
	"context"
	"errors"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestFallbackCannotOpenConflictingEpisodeIdentity(t *testing.T) {
	h := newHarness(t, nil, nil)
	s := &session{title: &core.Title{Type: core.TypeEpisode, ShowYear: 2005, Title: "Stone and Sky"}}
	wrong := core.Release{ID: "wrong", Name: "Space.Pilot.2023.S02E02.1080p.WEB"}
	_, err := h.m.openGeneration(t.Context(), s, &core.Resolution{Release: wrong}, nil)
	if !errors.Is(err, core.ErrNotAvailable) || h.engine.openCount() != 0 {
		t.Fatalf("conflicting fallback reached engine: err=%v opens=%d", err, h.engine.openCount())
	}
	if errors.Is(err, core.ErrReleaseRejected) {
		t.Fatal("identity mismatch must not blacklist a release valid for another show")
	}
}

func TestAlternatesSkipConflictingEpisodeBeforeFetching(t *testing.T) {
	h := newHarness(t, nil, nil)
	s := &session{title: &core.Title{Type: core.TypeEpisode, ShowYear: 2005, Title: "Stone and Sky"}}
	var fetched []string
	h.m.deps.FetchNZB = func(_ context.Context, rel core.Release) ([]byte, error) {
		fetched = append(fetched, rel.ID)
		return []byte("nzb-" + rel.ID), nil
	}
	res, err := h.m.nextResolution(t.Context(), s, nil, []core.Release{
		{ID: "reboot", Name: "Space.Pilot.2023.S02E02.1080p.WEB"},
		{ID: "other-episode", Name: "Space.Pilot.S02E02.Other.Story.1080p.WEB"},
		{ID: "correct", Name: "Space.Pilot.2005.S02E02.1080p.WEB"},
	})
	if err != nil || res == nil || res.Release.ID != "correct" || len(fetched) != 1 || fetched[0] != "correct" {
		t.Fatalf("alternate identity: res=%+v err=%v fetched=%v", res, err, fetched)
	}
}

func TestCachedTorrentRequiresCurrentEpisodeTarget(t *testing.T) {
	title := &core.Title{Type: core.TypeEpisode, ShowYear: 2005, Title: "Stone and Sky"}
	rel := core.Release{Name: "Space.Pilot.2005.S02E02.1080p.WEB", Protocol: core.ProtocolTorrent}
	if releaseMatchesTitle(rel, title) {
		t.Fatal("old descriptor without an episode identity was reusable")
	}
	rel.Target = &core.ReleaseTarget{ShowYear: 2005, EpisodeTitle: "Stone and Sky"}
	if !releaseMatchesTitle(rel, title) {
		t.Fatal("current identity target was rejected")
	}
	rel.Target.EpisodeTitle = "Other Story"
	if releaseMatchesTitle(rel, title) {
		t.Fatal("descriptor for another episode was reusable")
	}
}
