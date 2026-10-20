package releaseparse

import "testing"

func TestEpisodeTitleIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		ok         bool
	}{
		{"Space.Pilot.S02E02.Light.1080p.WEB.H264-GRP", "Stone and Sky", false},
		{"Space.Pilot.S02E02.Stone.and.Sky.1080p.WEB.H264-GRP", "Stone & Sky", true},
		{"Space.Pilot.S02E02.1080p.WEB.H264-GRP", "Stone and Sky", true},
		{"Space.Pilot.S02E02.REPACK.1080p.WEB.H264-GRP", "Stone and Sky", true},
		{"Space.Pilot.S02E02.ENG.1080p.WEB.H264-GRP", "Stone and Sky", true},
		{"Space.Pilot.S02E02.Light.1080p.WEB.H264-GRP", "Episode 2", true},
		{"Space.Pilot.S02E02.Stone.and.Sky.Part.2.720p.WEB.H264-GRP", "Stone and Sky (2)", true},
		{"Space.Pilot.S02E02.Stone.and.Sky.Pt02.720p.WEB.H264-GRP", "Stone and Sky (2)", true},
		{"Space.Pilot.S02E02.Stone.and.Sky.2.720p.WEB.H264-GRP", "Stone and Sky Part 2", true},
		{"Space.Pilot.S02E02.Stone.and.Sky.Part.1.720p.WEB.H264-GRP", "Stone and Sky (2)", false},
		{"Space.Pilot.S02E02.A.Part.of.Me.720p.WEB.H264-GRP", "A Part of Me", true},
	} {
		if got := EpisodeTitleMatches(tc.name, tc.want); got != tc.ok {
			t.Errorf("%q vs %q: %v", tc.name, tc.want, got)
		}
	}
	if got := EpisodeSeriesYear("Space.Pilot.2005.S02E02.2006.1080p", 2005); got != 2005 {
		t.Fatalf("series year %d", got)
	}
	if got := EpisodeSeriesYear("Space.Pilot.S02E02.2006.1080p", 2006); got != 0 {
		t.Fatalf("episode title became series year: %d", got)
	}
}
