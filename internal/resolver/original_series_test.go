package resolver

import (
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

func TestCanonicalOriginalSeriesNameWithoutReportedIDs(t *testing.T) {
	title := &core.Title{Type: core.TypeEpisode, ShowTitle: "Space Show", ShowOriginalTitle: "Spectacle Spatial", ShowYear: 2020, Title: "The Arrival", OriginalTitle: "L Arrivee", Season: 1, Episode: 2, Runtime: 45 * time.Minute}
	cfg := testCfg()
	s := New(&fakeIndexer{}, fakeHealth{}, nil, cfg, Options{})
	profile := SelectProfile(cfg.Quality, webCaps(), nil)
	for _, tt := range []struct{ name, want string }{
		{"Spectacle.Spatial.2020.S01E02.1080p.WEB.H264-GRP", ""},
		{"L.Arrivee.2020.S01E02.1080p.WEB.H264-GRP", "title mismatch"},
		{"Spectacle.Spatial.2020.S01E03.1080p.WEB.H264-GRP", "season/episode mismatch"},
	} {
		r := rel("r", tt.name, 1500)
		r.Info = releaseparse.Parse(r.Name)
		if got := s.rejectReason(r, title, searchRequestFor(title), profile, cfg.Resolver, webCaps(), nil, false); got != tt.want {
			t.Fatalf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
}
