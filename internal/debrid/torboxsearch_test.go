package debrid

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

type fakeTorBoxSearch struct {
	mu       sync.Mutex
	requests []*http.Request
	body     string
	status   int
}

func (f *fakeTorBoxSearch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	body, status := f.body, f.status
	f.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (f *fakeTorBoxSearch) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

var (
	hashCached   = strings.Repeat("c1", 20)
	hashUncached = strings.Repeat("d2", 20)
)

const torBoxSearchBody = `{"success":true,"error":null,"detail":"Found torrents.","data":{"torrents":[
 {"hash":"` + "c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1" + `","raw_title":"Show.S01E03.1080p.WEB-DL.DDP5.1.H.264-GRP","size":2147483648,
  "magnet":"magnet:?xt=urn:btih:c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1","cached":true,"age":"3d","last_known_seeders":120,"last_known_peers":4},
 {"hash":"` + "d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2" + `","raw_title":"Show.S01E03.720p.HDTV.x264-OTHER","size":"900000000",
  "magnet":"","cached":false,"age":12,"last_known_seeders":3},
 {"hash":"","raw_title":"Show.S01E03.2160p.WEB-DL-NOHASH","size":1,"magnet":"","cached":true}
],"total_torrents":3}}`

func torBoxHarness(t *testing.T, fake *fakeTorBoxSearch) *harness {
	t.Helper()
	h := newHarness(t, 1<<20, "torbox")
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	h.svc.opts.TorBoxSearchURL = srv.URL
	return h
}

func episodeRequest() core.SearchRequest {
	return core.SearchRequest{Type: core.TypeEpisode, IDs: core.IDs{IMDB: "tt1234567", TVDB: 99}, Title: "Show", Season: 1, Episode: 3}
}

func TestTorBoxSearchFeedsReleasesAndCacheAnswers(t *testing.T) {
	fake := &fakeTorBoxSearch{body: torBoxSearchBody}
	h := torBoxHarness(t, fake)
	inner := &fakeInner{results: []core.Release{
		{ID: "u1", Name: "Show.S01E03.1080p.WEB-DL-USENET"},
		// The same torrent from a configured indexer: kept once.
		{ID: "ix", Name: "Show.S01E03.1080p.WEB-DL.DDP5.1.H.264-GRP", Indexer: "tz", Protocol: core.ProtocolTorrent, InfoHash: hashCached},
	}}
	got, err := NewIndexer(inner, h.svc).Search(context.Background(), episodeRequest())
	if err != nil {
		t.Fatal(err)
	}
	if fake.count() != 1 {
		t.Fatalf("torbox search requests %d", fake.count())
	}
	req := fake.requests[0]
	if req.URL.Path != "/torrents/imdb:tt1234567" || req.URL.Query().Get("season") != "1" ||
		req.URL.Query().Get("episode") != "3" || req.URL.Query().Get("check_cache") != "true" {
		t.Fatalf("request %s", req.URL)
	}
	if req.Header.Get("Authorization") != "Bearer k" || strings.Contains(req.URL.String(), "k&") {
		t.Fatalf("auth header %q url %s", req.Header.Get("Authorization"), req.URL)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.ID)
	}
	// The cached torrent (from the indexer) leads; the uncached TorBox
	// result is dropped under cached-only; the hashless one is skipped.
	if len(got) != 2 || got[0].ID != "ix" || got[1].ID != "u1" {
		t.Fatalf("results %v", names)
	}
	if !h.svc.KnownCached(hashCached) {
		t.Fatal("torbox's cache answer was not recorded")
	}
	// No bulk check for a hash TorBox already answered.
	if n := h.provs["torbox-0"].callCount("check"); n != 0 {
		t.Fatalf("redundant bulk checks: %d", n)
	}
	if st := h.svc.SearchStatus(); !st.Enabled || st.Searches != 1 || st.Results != 2 {
		t.Fatalf("status %+v", st)
	}
}

func TestTorBoxSearchStandsAloneAndMapsFields(t *testing.T) {
	fake := &fakeTorBoxSearch{body: torBoxSearchBody}
	h := torBoxHarness(t, fake)
	cfg := h.svc.Config()
	cfg.CachedOnly = false
	h.svc.UpdateConfig(cfg)
	inner := &failingInner{err: errors.New("no indexers answered")}
	got, err := NewIndexer(inner, h.svc).Search(context.Background(), episodeRequest())
	if err != nil {
		t.Fatalf("indexer failure hid torbox results: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("results %+v", got)
	}
	r := got[0]
	if r.Protocol != core.ProtocolTorrent || r.InfoHash != hashCached || r.Indexer != "TorBox" || r.Size != 2<<30 ||
		r.Seeders != 120 || r.Peers != 4 || r.Info.Height != 1080 || r.MagnetURL == "" {
		t.Fatalf("release %+v", r)
	}
	if age := time.Since(r.PubDate); age < 71*time.Hour || age > 73*time.Hour {
		t.Fatalf("age %v", age)
	}
	u := got[1]
	if u.InfoHash != hashUncached || u.Size != 900000000 || !strings.HasPrefix(u.MagnetURL, "magnet:?xt=urn:btih:"+hashUncached) {
		t.Fatalf("uncached release %+v", u)
	}
}

type failingInner struct {
	fakeInner
	err error
}

func (f *failingInner) Search(context.Context, core.SearchRequest) ([]core.Release, error) {
	return nil, f.err
}

func TestTorBoxSearchOffWhenDisabledOrNoID(t *testing.T) {
	fake := &fakeTorBoxSearch{body: torBoxSearchBody}
	h := torBoxHarness(t, fake)
	x := NewIndexer(&fakeInner{}, h.svc)
	// No usable id: no request.
	if _, err := x.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "Untitled"}); err != nil {
		t.Fatal(err)
	}
	cfg := h.svc.Config()
	cfg.Providers[0].DisableSearch = true
	h.svc.UpdateConfig(cfg)
	if _, err := x.Search(context.Background(), episodeRequest()); err != nil {
		t.Fatal(err)
	}
	if fake.count() != 0 {
		t.Fatalf("searched %d times", fake.count())
	}
	if h.svc.SearchesTorBox() {
		t.Fatal("search reported on")
	}
	// Other kinds never search.
	h.svc.UpdateConfig(config.Debrid{CachedOnly: true, Providers: []config.DebridProvider{{ID: "rd", Kind: "realdebrid", APIKey: "k", Enabled: true}}})
	if h.svc.SearchesTorBox() {
		t.Fatal("real-debrid searches")
	}
}

func TestTorBoxSearchNoMetadataIsEmpty(t *testing.T) {
	fake := &fakeTorBoxSearch{status: http.StatusNotFound,
		body: `{"success":false,"error":"NO_METADATA","detail":"Could not find metadata. Please try again later.","data":null}`}
	h := torBoxHarness(t, fake)
	got, err := NewIndexer(&fakeInner{}, h.svc).Search(context.Background(),
		core.SearchRequest{Type: core.TypeMovie, IDs: core.IDs{TMDB: 603}})
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v err %v", got, err)
	}
	if fake.requests[0].URL.Path != "/torrents/tmdb:603" {
		t.Fatalf("path %s", fake.requests[0].URL.Path)
	}
}

func TestTorBoxAge(t *testing.T) {
	for in, want := range map[string]time.Duration{
		`5`: 5 * 24 * time.Hour, `"5"`: 5 * 24 * time.Hour, `"3d"`: 3 * 24 * time.Hour,
		`"12h"`: 12 * time.Hour, `"2 weeks"`: 14 * 24 * time.Hour, `"1y"`: 365 * 24 * time.Hour,
		`"30 mins"`: 30 * time.Minute, `null`: 0, `""`: 0, `"soon"`: 0,
	} {
		if got := torBoxAge([]byte(in)); got != want {
			t.Errorf("%s: %v, want %v", in, got, want)
		}
	}
}
