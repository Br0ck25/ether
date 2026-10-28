package resolver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/newznab"
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

type rescueFakeIndexer struct {
	*fakeIndexer
	rescued []core.Release
}

func (r *rescueFakeIndexer) SearchRescue(_ context.Context, _ core.SearchRequest) ([]core.Release, error) {
	return append([]core.Release(nil), r.rescued...), nil
}

func TestObscureTitlesResolveAgainstAllOddsInMilliseconds(t *testing.T) {
	ctx := context.Background()

	// 1. Classic world cinema: primary ID search returns an expired/missing
	// Usenet post; SearchRescue finds an untagged AKA release ("Le Samourai
	// AKA The Godson 1967") in milliseconds.
	deadPost := rel("dead-id", "Le.Samourai.1967.720p.BluRay.x264-OLD", 4000)
	deadPost.TitleIDs = core.IDs{IMDB: "tt0062229"}
	akaRescue := rel("aka-criterion", "Le.Samourai.AKA.The.Godson.1967.1080p.BluRay.x264-CRITERION", 7500)
	idx := &rescueFakeIndexer{
		fakeIndexer: &fakeIndexer{releases: []core.Release{deadPost}},
		rescued:     []core.Release{akaRescue},
	}
	svc := New(idx, fakeHealth{unhealthy: map[string]bool{"dead-id": true}}, nil, testCfg(), Options{})
	t.Cleanup(func() { _ = svc.Close() })

	samourai := &core.Title{
		Key: core.MovieKey(551), Type: core.TypeMovie,
		IDs:   core.IDs{TMDB: 551, IMDB: "tt0062229"},
		Title: "The Godson", OriginalTitle: "Le Samouraï", Year: 1967,
		Runtime: 105 * time.Minute,
	}
	start := time.Now()
	res, err := svc.Resolve(ctx, core.ResolveRequest{Title: samourai, Client: appleTVCaps()})
	elapsed := time.Since(start)
	if err != nil || res.Release.ID != "aka-criterion" {
		t.Fatalf("classic AKA rescue failed: res=%+v err=%v", res, err)
	}
	if elapsed > 25*time.Millisecond {
		t.Fatalf("expected sub-25ms resolution, took %s", elapsed)
	}

	// 2. Classic 1990s sitcom on Apple TV (4K profile, MinSizeMBPerMin=15):
	// only a 140 MB 480p PDTV.XviD.AC3 episode exists, plus a conflicting UK
	// show release and a miniseries Part release.
	tvIdx := &fakeIndexer{releases: []core.Release{
		rel("uk-wrong", "The.Office.UK.S01E02.Diversity.Day.480p.DVDRip.XviD.AC3-GRP", 140),
		rel("us-pdtv", "The.Office.US.S01.E02.Diversity.Day.480p.PDTV.XviD.AC3-FOV", 140),
	}}
	svcTV := New(tvIdx, fakeHealth{}, nil, testCfg(), Options{})
	t.Cleanup(func() { _ = svcTV.Close() })
	officeEp := &core.Title{
		Key: core.EpisodeKey(2316, 1, 2), Type: core.TypeEpisode,
		ShowTitle: "The Office", ShowYear: 2005, Title: "Diversity Day",
		Season: 1, Episode: 2, Runtime: 22 * time.Minute,
		Countries: []string{"United States of America"},
	}
	resTV, err := svcTV.Resolve(ctx, core.ResolveRequest{Title: officeEp, Client: appleTVCaps()})
	if err != nil || resTV.Release.ID != "us-pdtv" {
		t.Fatalf("legacy 480p PDTV on 4K profile failed: res=%+v err=%v traces=%+v", resTV, err, svcTV.Traces())
	}

	// 3. Miniseries named "Part 3" instead of S01E03.
	miniIdx := &fakeIndexer{releases: []core.Release{
		rel("part2", "Band.of.Brothers.Part.2.Day.of.Days.1080p.BluRay.x264-GRP", 4500),
		rel("part3", "Band.of.Brothers.Part.3.Carentan.1080p.BluRay.x264-GRP", 4500),
	}}
	svcMini := New(miniIdx, fakeHealth{}, nil, testCfg(), Options{})
	t.Cleanup(func() { _ = svcMini.Close() })
	miniEp := &core.Title{
		Key: core.EpisodeKey(4613, 1, 3), Type: core.TypeEpisode,
		ShowTitle: "Band of Brothers", ShowYear: 2001, Title: "Carentan",
		Season: 1, Episode: 3, Runtime: 65 * time.Minute,
	}
	resMini, err := svcMini.Resolve(ctx, core.ResolveRequest{Title: miniEp, Client: appleTVCaps()})
	if err != nil || resMini.Release.ID != "part3" {
		t.Fatalf("miniseries Part 3 failed: res=%+v err=%v traces=%+v", resMini, err, svcMini.Traces())
	}
}

func BenchmarkObscureTitleResolve(b *testing.B) {
	cfg := testCfg()
	deadPost := rel("dead-id", "Le.Samourai.1967.720p.BluRay.x264-OLD", 4000)
	deadPost.TitleIDs = core.IDs{IMDB: "tt0062229"}
	akaRescue := rel("aka-criterion", "Le.Samourai.AKA.The.Godson.1967.1080p.BluRay.x264-CRITERION", 7500)
	samourai := &core.Title{
		Key: core.MovieKey(551), Type: core.TypeMovie,
		IDs:   core.IDs{TMDB: 551, IMDB: "tt0062229"},
		Title: "The Godson", OriginalTitle: "Le Samouraï", Year: 1967,
		Runtime: 105 * time.Minute,
	}
	caps := appleTVCaps()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		idx := &rescueFakeIndexer{
			fakeIndexer: &fakeIndexer{releases: []core.Release{deadPost}},
			rescued:     []core.Release{akaRescue},
		}
		svc := New(idx, fakeHealth{unhealthy: map[string]bool{"dead-id": true}}, nil, cfg, Options{})
		res, err := svc.Resolve(b.Context(), core.ResolveRequest{Title: samourai, Client: caps})
		_ = svc.Close()
		if err != nil || res.Release.ID != "aka-criterion" {
			b.Fatalf("res=%+v err=%v", res, err)
		}
	}
}

func TestFullFleet12IndexerObscureFanOut(t *testing.T) {
	// Every indexer must enter its first search before any may answer. This
	// proves concurrent fan-out without a host-speed threshold under -race.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	allEntered := make(chan struct{})
	var entered atomic.Int32
	const fakeNZBPayload = `<?xml version="1.0"?><nzb><file poster="a" date="1" subject="x"><groups><group>alt.binaries.test</group></groups><segments><segment bytes="10" number="1">m1@test</segment></segments></file></nzb>`
	var cfgs []config.Indexer
	for i := 0; i < 12; i++ {
		idxNum := i
		var firstSearch sync.Once
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			base := "http://" + r.Host
			switch q.Get("t") {
			case "caps":
				fmt.Fprint(w, `<caps><searching><search available="yes" supportedParams="q"/><movie-search available="yes" supportedParams="q,imdbid,tmdbid"/><tv-search available="yes" supportedParams="q,tvdbid,tmdbid,imdbid,season,ep"/></searching></caps>`)
			case "get":
				fmt.Fprint(w, fakeNZBPayload)
			default:
				firstSearch.Do(func() {
					if entered.Add(1) == 12 {
						close(allEntered)
					}
				})
				select {
				case <-allEntered:
				case <-ctx.Done():
					http.Error(w, "fan-out never reached every indexer", http.StatusGatewayTimeout)
					return
				}
				// Indexer 0 returns an expired/broken Usenet post on imdbid;
				// Indexer 11 (the last of 12 indexers!) holds the healthy
				// release under the collapsed initialism alias "MASH 1970".
				if idxNum == 0 && q.Get("imdbid") != "" {
					fmt.Fprintf(w, `<rss version="2.0"><channel><item><title>MASH.1970.720p.BluRay.x264-DEAD</title><guid>dead-guid</guid><link>%s/api?t=get&amp;id=dead</link><size>4000000000</size></item></channel></rss>`, base)
					return
				}
				if idxNum == 11 && q.Get("q") == "MASH 1970" {
					fmt.Fprintf(w, `<rss version="2.0"><channel><item><title>MASH.1970.1080p.BluRay.x264-CLASSIC</title><guid>live-guid</guid><link>%s/api?t=get&amp;id=live</link><size>7500000000</size></item></channel></rss>`, base)
					return
				}
				fmt.Fprint(w, `<rss version="2.0"><channel></channel></rss>`)
			}
		}))
		t.Cleanup(srv.Close)
		cfgs = append(cfgs, config.Indexer{
			ID: fmt.Sprintf("idx-%d", i), Name: fmt.Sprintf("Idx%d", i),
			URL: srv.URL, APIKey: "k", Priority: i + 1, Enabled: true,
		})
	}
	mgr := newznab.NewManager(nil, cfgs, newznab.Options{SearchCacheTTL: -1})
	// Warm caps once as a running server has.
	for _, cl := range mgr.Clients() {
		_, _ = cl.Caps(context.Background())
	}
	svc := New(mgr, fakeHealth{unhealthy: map[string]bool{"dead": true}}, nil, testCfg(), Options{})
	t.Cleanup(func() { _ = svc.Close() })

	mash := &core.Title{
		Key: core.MovieKey(651), Type: core.TypeMovie,
		IDs:   core.IDs{TMDB: 651, IMDB: "tt0066026"},
		Title: "M*A*S*H", Year: 1970, Runtime: 116 * time.Minute,
	}
	start := time.Now()
	res, err := svc.Resolve(ctx, core.ResolveRequest{Title: mash, Client: appleTVCaps()})
	elapsed := time.Since(start)
	if err != nil || res.Release.Name != "MASH.1970.1080p.BluRay.x264-CLASSIC" {
		t.Fatalf("12-indexer obscure fan-out failed: res=%+v err=%v", res, err)
	}
	if entered.Load() != 12 {
		t.Fatalf("only %d indexers searched", entered.Load())
	}
	t.Logf("12-indexer obscure fan-out + rescue + health check resolved in %s", elapsed)
}
