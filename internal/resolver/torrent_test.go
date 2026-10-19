package resolver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// torrentIndexer records the releases FetchNZB saw and answers "not cached"
// for chosen ids.
type torrentIndexer struct {
	*fakeIndexer
	mu        sync.Mutex
	fetched   []core.Release
	notCached map[string]bool
}

func (f *torrentIndexer) FetchNZB(_ context.Context, r core.Release) ([]byte, error) {
	f.mu.Lock()
	f.fetched = append(f.fetched, r)
	f.mu.Unlock()
	if f.notCached[r.Name] {
		return nil, fmt.Errorf("debrid: %w", core.ErrNotCached)
	}
	return []byte("payload:" + r.ID), nil
}

func episodeTitle() *core.Title {
	return &core.Title{
		Key: core.EpisodeKey(1396, 1, 3), Type: core.TypeEpisode,
		ShowTitle: "Breaking Bad", Season: 1, Episode: 3, Runtime: 49 * time.Minute,
		ShowIDs: core.IDs{TVDB: 81189},
	}
}

func torrent(r core.Release) core.Release {
	r.Protocol = core.ProtocolTorrent
	r.InfoHash = strings.Repeat("ab", 20)
	return r
}

func TestTorrentSeasonPackStreamsTheEpisode(t *testing.T) {
	idx := &torrentIndexer{fakeIndexer: &fakeIndexer{releases: []core.Release{
		rel("usenet-pack", "Breaking.Bad.S01.COMPLETE.720p.BluRay.x264-GRP", 10000),
		torrent(rel("wrong-season", "Breaking.Bad.S02.720p.BluRay.x264-GRP", 10000)),
		torrent(rel("pack", "Breaking.Bad.S01.720p.BluRay.x264-GRP", 10000)),
	}}}
	svc := New(idx, fakeHealth{}, nil, testCfg(), Options{})
	res, err := svc.Resolve(context.Background(), core.ResolveRequest{Title: episodeTitle(), Client: webCaps()})
	if err != nil {
		t.Fatalf("resolve: %v traces %+v", err, svc.Traces())
	}
	want := core.TargetReleaseID("pack", core.ReleaseTarget{Season: 1, Episode: 3})
	if res.Release.ID != want || res.Release.Target == nil || res.Release.Target.Episode != 3 {
		t.Fatalf("release %+v", res.Release)
	}
	if got := idx.fetched[len(idx.fetched)-1]; got.Target == nil || got.Target.Season != 1 || got.ID != want {
		t.Fatalf("fetch saw %+v", got)
	}
	reasons := map[string]string{}
	for _, c := range svc.Traces()[0].Candidates {
		reasons[c.ReleaseID] = c.Reason
	}
	// The 10 GB pack is not judged against one episode's runtime; Usenet
	// packs stay skipped.
	if !strings.Contains(reasons["usenet-pack"], "season pack") || !strings.Contains(reasons[core.TargetReleaseID("wrong-season", core.ReleaseTarget{Season: 1, Episode: 3})], "mismatch") || reasons[want] != "" {
		t.Fatalf("reasons %v", reasons)
	}
}

func TestNotCachedTorrentIsSkippedWithoutCooloffOrBadMark(t *testing.T) {
	idx := &torrentIndexer{fakeIndexer: &fakeIndexer{releases: []core.Release{
		torrent(rel("t1", "The.Matrix.1999.1080p.BluRay.x264-AAA", 9000)),
		torrent(rel("t2", "The.Matrix.1999.1080p.WEB-DL.x264-BBB", 8000)),
	}}, notCached: map[string]bool{"The.Matrix.1999.1080p.BluRay.x264-AAA": true, "The.Matrix.1999.1080p.WEB-DL.x264-BBB": true}}
	svc := New(idx, fakeHealth{}, nil, testCfg(), Options{})
	_, err := svc.Resolve(context.Background(), core.ResolveRequest{Title: matrixTitle(), Client: appleTVCaps()})
	if !errors.Is(err, core.ErrNotAvailable) || !errors.Is(err, core.ErrNotCached) {
		t.Fatalf("got %v", err)
	}
	if svc.isBad("t1") || svc.isBad("t2") {
		t.Fatal("an uncached torrent was marked bad")
	}
	if known, _ := svc.Available(context.Background(), matrixTitle().Key); known {
		t.Fatal("not-cached results cooled the title off")
	}
	// Once one is cached it wins on the next resolve.
	idx.notCached = map[string]bool{"The.Matrix.1999.1080p.BluRay.x264-AAA": true}
	res, err := svc.Resolve(context.Background(), core.ResolveRequest{Title: matrixTitle(), Client: appleTVCaps()})
	if err != nil || res.Release.ID != "t2" {
		t.Fatalf("second resolve %+v %v", res, err)
	}
}
