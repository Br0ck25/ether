package resolver

import (
	"fmt"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

// Include parsing, identity and client/profile gates, quality scoring,
// diagnostic traces and sorting. Network/provider latency is intentionally
// excluded so improvements to name matching have a measurable local budget.
func BenchmarkSelectFrom1000Releases(b *testing.B) {
	cfg := testCfg()
	s := New(&fakeIndexer{}, fakeHealth{}, nil, cfg, Options{})
	b.Cleanup(func() { s.Close() })
	title := &core.Title{Key: core.MovieKey(123), Type: core.TypeMovie, Title: "Space Pilot", Year: 2020, Runtime: 100 * time.Minute, IDs: core.IDs{TMDB: 123}}
	caps := webCaps()
	profile := SelectProfile(cfg.Quality, caps, nil)
	profile.MaxHeight = 1080
	req := core.ResolveRequest{Title: title, Client: caps}
	releases := make([]core.Release, 1000)
	for i := range releases {
		name, height, ids := "Space.Pilot", 1080, core.IDs{TMDB: 123}
		switch i % 4 {
		case 1:
			name = "Pilote.Spatial"
		case 2:
			ids.TMDB = 456
		case 3:
			height = 2160
		}
		releases[i] = core.Release{ID: fmt.Sprint(i), Name: fmt.Sprintf("%s.2020.%dp.WEB.H264-GROUP%d", name, height, i), Size: 4 << 30, TitleIDs: ids}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for i := range releases {
			releases[i].Info = releaseparse.Parse(releases[i].Name)
		}
		trace := Trace{}
		accepted, _, err := s.rankCandidates(b.Context(), req, searchRequestFor(title), profile, releases, &trace, "")
		if err != nil || len(accepted) != 500 || len(trace.Candidates) != 1000 {
			b.Fatalf("incorrect identity/quality selection: accepted=%d traces=%d err=%v", len(accepted), len(trace.Candidates), err)
		}
	}
}
