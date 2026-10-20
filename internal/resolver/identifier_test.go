package resolver

import (
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

func TestReportedIDsDisambiguateWithoutBypassingEpisodeOrYear(t *testing.T) {
	title := &core.Title{Key: core.EpisodeKey(123, 2, 2), Type: core.TypeEpisode, Title: "Stone and Sky", ShowTitle: "Space Pilot", ShowYear: 2005, ShowIDs: core.IDs{TMDB: 123}, Season: 2, Episode: 2, Runtime: 45 * time.Minute}
	cfg := testCfg()
	svc := New(&fakeIndexer{}, fakeHealth{}, nil, cfg, Options{})
	profile := SelectProfile(cfg.Quality, webCaps(), nil)
	for _, tt := range []struct {
		name, release string
		ids           core.IDs
		want          string
	}{
		{"translated title with evidence", "Pilote.Spatial.S02E02.Pierre.et.Ciel.1080p.WEB.H264-GRP", core.IDs{TMDB: 123}, ""},
		{"query ID alone proves nothing", "Pilote.Spatial.S02E02.1080p.WEB.H264-GRP", core.IDs{}, "title mismatch"},
		{"obfuscated without reported ID", "a1b2c3d4e5f6g7h8.S02E02.1080p.WEB.H264-GRP", core.IDs{}, "title mismatch"},
		{"conflicting ID with exact name", "Space.Pilot.S02E02.1080p.WEB.H264-GRP", core.IDs{TMDB: 456}, "indexer title identifier mismatch"},
		{"wrong episode despite matching ID", "Space.Pilot.S02E03.1080p.WEB.H264-GRP", core.IDs{TMDB: 123}, "season/episode mismatch"},
		{"wrong reboot despite matching ID", "Space.Pilot.2023.S02E02.1080p.WEB.H264-GRP", core.IDs{TMDB: 123}, "series year mismatch"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := rel("r", tt.release, 1500)
			r.Info, r.TitleIDs = releaseparse.Parse(r.Name), tt.ids
			if got := svc.rejectReason(r, title, searchRequestFor(title), profile, cfg.Resolver, webCaps(), nil, false); got != tt.want {
				t.Fatalf("rejection = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTorrentTargetRetainsReportedIdentityWithoutInventingIt(t *testing.T) {
	req := core.SearchRequest{Type: core.TypeEpisode, Season: 2, Episode: 2, IDs: core.IDs{TMDB: 123}}
	release := core.Release{ID: "r", Protocol: core.ProtocolTorrent, Info: core.ReleaseInfo{FullSeason: true}, TitleIDs: core.IDs{TMDB: 123, IMDB: "tt0000123"}}
	got := withTarget(release, req)
	id := got.ID
	got = withTarget(got, req)
	if got.Target.ExpectedIDs != req.IDs || got.Target.ReportedIDs != release.TitleIDs || got.ID != id {
		t.Fatalf("target identity changed: %+v", got.Target)
	}
	release.TitleIDs = core.IDs{}
	if target := withTarget(release, req).Target; target.ReportedIDs != (core.IDs{}) {
		t.Fatal("request IDs became reported IDs")
	}
}
