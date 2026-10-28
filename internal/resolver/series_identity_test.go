package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestEpisodeRejectsSameNamedRebootAndUsesSeriesYear(t *testing.T) {
	title := &core.Title{Key: core.EpisodeKey(100, 2, 2), Type: core.TypeEpisode, Title: "Stone and Sky", ShowTitle: "Space Pilot", ShowYear: 2005, Year: 2006, Season: 2, Episode: 2, Runtime: 45 * time.Minute}
	wrong := rel("reboot", "Space.Pilot.2023.S02E02.1080p.WEB.H264-GRP", 2000)
	wrong.Grabs = 10000
	right := rel("original", "Space.Pilot.2005.S02E02.1080p.WEB.H264-GRP", 2000)
	for _, r := range []core.Release{right, rel("no-year", "Space.Pilot.S02E02.1080p.WEB.H264-GRP", 2000)} {
		idx := &fakeIndexer{releases: []core.Release{wrong, rel("unnamed-reboot", "Space.Pilot.S02E02.Light.1080p.WEB.H264-GRP", 2000), r}}
		svc := New(idx, fakeHealth{}, nil, testCfg(), Options{})
		got, err := svc.Resolve(context.Background(), core.ResolveRequest{Title: title, Client: webCaps()})
		if err != nil || got.Release.ID != r.ID {
			t.Fatalf("selected a reboot for the original: %+v %v", got, err)
		}
		if search := searchRequestFor(title); search.Year != 2005 {
			t.Fatalf("search used episode year %d instead of series year", search.Year)
		}
	}
}

func TestTorrentTargetCarriesSeriesAndEpisodeIdentity(t *testing.T) {
	req := core.SearchRequest{Type: core.TypeEpisode, Year: 2005, Season: 2, Episode: 2, EpisodeTitle: "Stone and Sky"}
	rel := core.Release{ID: "r", Protocol: core.ProtocolTorrent}
	for range 2 {
		rel = withTarget(rel, req)
		if rel.ID != "r" || rel.Target == nil || rel.Target.ShowYear != 2005 || rel.Target.EpisodeTitle != req.EpisodeTitle {
			t.Fatalf("lost target identity: %+v", rel.Target)
		}
	}
}
