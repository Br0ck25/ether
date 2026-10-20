package cardigann

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

const publicPage = `<html><body>
<table id="torrents"><thead><tr><th>Cat</th><th>Name</th></tr></thead><tbody>
<tr><td class="date-header" colspan="8">2024-03-07</td></tr>
<tr>
 <td><a href="browse.php?cat=1">Movies</a></td>
 <td><a class="title" href="details.php?id=101">The Signal 1999 1080p BluRay x264-GRP</a> <a href="https://www.imdb.com/title/tt0133093/">IMDb</a></td>
 <td><a href="download.php?id=101">DL</a></td>
 <td class="size">8.5 GB</td><td class="seeders">120</td><td class="leechers">4</td><td class="snatched">1,500</td>
 <td><img alt="Free"></td>
</tr>
<tr>
 <td><a href="browse.php?cat=1">Movies</a></td>
 <td><a class="title" href="details.php?id=102">The Signal 1999 2160p WEB-DL</a></td>
 <td><a href="magnet:?xt=urn:btih:0123456789ABCDEF0123456789ABCDEF01234567&amp;dn=signal">Magnet</a></td>
 <td class="size">20 GiB</td><td class="seeders">55</td><td class="leechers">2</td>
 <td class="added">2 hours ago</td>
</tr>
<tr>
 <td><a href="browse.php?cat=3">Music</a></td>
 <td><a class="title" href="details.php?id=103">The Signal 1999 Soundtrack FLAC</a></td>
 <td><a href="download.php?id=103">DL</a></td>
 <td class="size">500 MB</td><td class="seeders">3</td><td class="leechers">0</td>
</tr>
<tr>
 <td><a href="browse.php?cat=1">Movies</a></td>
 <td><a class="title" href="details.php?id=104">The Signal 1999 720p</a> <a href="https://www.imdb.com/title/tt9999999/">IMDb</a></td>
 <td><a href="download.php?id=104">DL</a></td>
 <td class="size">2 GB</td><td class="seeders">1</td><td class="leechers">0</td>
</tr>
<tr>
 <td><a href="browse.php?cat=1">Movies</a></td>
 <td><a class="title" href="details.php?id=105">Unrelated Film 2001</a></td>
 <td><a href="download.php?id=105">DL</a></td>
 <td class="size">1 GB</td><td class="seeders">9</td><td class="leechers">0</td>
</tr>
</tbody></table></body></html>`

func publicSite(t *testing.T, seen *[]string, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	torrent, _ := torrentFile("The.Signal.1999.1080p")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*seen = append(*seen, r.URL.RequestURI())
		mu.Unlock()
		switch {
		case r.URL.Path == "/browse.php":
			if r.URL.Query().Get("sort") != "added" || r.URL.Query().Has("fl") {
				http.Error(w, "unexpected inputs "+r.URL.RawQuery, 400)
				return
			}
			fmt.Fprint(w, publicPage)
		case strings.HasPrefix(r.URL.Path, "/tv/"):
			fmt.Fprint(w, `<table id="torrents"><tbody><tr><td><a href="browse.php?cat=2">TV</a></td><td><a class="title" href="details.php?id=7">Show Name S01E02 720p</a></td><td><a href="download.php?id=7">DL</a></td><td class="size">1 GB</td><td class="seeders">5</td><td class="leechers">1</td><td class="added">1 day ago</td></tr></tbody></table>`)
		case r.URL.Path == "/download.php":
			w.Header().Set("Content-Type", "application/x-bittorrent")
			w.Write(torrent)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestPublicHTMLTracker(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	srv := publicSite(t, &seen, &mu)
	defer srv.Close()
	def := loadDef(t, "public.yml")
	r := testRunner(t, def, srv.URL, runnerSetup{})
	var b budget
	ctx := context.Background()
	rels, err := r.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: "The Signal", Year: 1999, IDs: core.IDs{IMDB: "tt0133093"}}, nil, nil, b.reserve)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "search=tt0133093") || !strings.Contains(seen[0], "c1=1&") || strings.Contains(seen[0], "c2=1") {
		t.Fatalf("requests = %v", seen)
	}
	if b.hits != 1 {
		t.Fatalf("hits = %d", b.hits)
	}
	// The music row is filtered by category, the tt9999999 row by IMDb id.
	// andmatch does not apply to identifier searches.
	names := []string{}
	for _, rel := range rels {
		names = append(names, rel.Name)
	}
	if len(rels) != 3 {
		t.Fatalf("results = %v", names)
	}
	first := rels[0]
	if first.Protocol != core.ProtocolTorrent || first.NZBURL != srv.URL+"/download.php?id=101" || first.GUID != srv.URL+"/details.php?id=101" {
		t.Fatalf("first = %+v", first)
	}
	if first.Size != 9126805504 || first.Seeders != 120 || first.Peers != 124 || first.Grabs != 1500 || first.Category != 2040 {
		t.Fatalf("first numbers = %+v", first)
	}
	if !first.PubDate.Equal(time.Date(2024, 3, 7, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("date header not applied: %v", first.PubDate)
	}
	second := rels[1]
	if second.InfoHash != "0123456789abcdef0123456789abcdef01234567" || !strings.HasPrefix(second.MagnetURL, "magnet:") || second.NZBURL != "" {
		t.Fatalf("magnet row = %+v", second)
	}
	if !second.PubDate.Equal(testNow.Add(-2 * time.Hour)) {
		t.Fatalf("timeago date = %v", second.PubDate)
	}

	// Resolving the .torrent-only result downloads it once, as a grab.
	resolved, err := r.ResolveTorrent(ctx, first, b.reserve)
	if err != nil {
		t.Fatal(err)
	}
	_, wantHash := torrentFile("The.Signal.1999.1080p")
	if resolved.InfoHash != wantHash || !strings.Contains(resolved.MagnetURL, wantHash) || !strings.Contains(resolved.MagnetURL, "tracker.test") {
		t.Fatalf("resolved = %+v", resolved)
	}
	if b.grabs != 1 {
		t.Fatalf("grabs = %d", b.grabs)
	}
	// A known magnet needs no request.
	before := len(seen)
	if _, err := r.ResolveTorrent(ctx, second, b.reserve); err != nil || len(seen) != before || b.grabs != 1 {
		t.Fatalf("magnet resolution made a request: %v", err)
	}
}

func TestPublicKeywordSearchAndTVPath(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	srv := publicSite(t, &seen, &mu)
	defer srv.Close()
	r := testRunner(t, loadDef(t, "public.yml"), srv.URL, runnerSetup{})
	ctx := context.Background()
	rels, err := r.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: "The Signal: Reloaded?", Year: 1999}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Keywords are sanitised then run through keywordsfilters; andmatch
	// drops rows missing a keyword ("Reloaded").
	if !strings.Contains(seen[0], "search=The+Signal+Reloaded+1999") {
		t.Fatalf("request = %s", seen[0])
	}
	if len(rels) != 0 {
		t.Fatalf("andmatch kept %d rows", len(rels))
	}
	seen = nil
	rels, err = r.Search(ctx, core.SearchRequest{Type: core.TypeEpisode, Title: "Show Name", Season: 1, Episode: 2}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || !strings.HasPrefix(seen[0], "/tv/Show+Name+S01E02?") || !strings.Contains(seen[0], "c2=1") {
		t.Fatalf("tv requests = %v", seen)
	}
	if len(rels) != 1 || rels[0].Category != 5040 || !rels[0].PubDate.Equal(testNow.Add(-24*time.Hour)) {
		t.Fatalf("tv results = %+v", rels)
	}
}

func TestJSONTrackerWithHasFilter(t *testing.T) {
	var gotKey, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotQuery = r.Header.Get("X-Api-Key"), r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("q") == "nothing" {
			fmt.Fprint(w, `{"data":null}`)
			return
		}
		fmt.Fprint(w, `{"data":{"count":3,"torrents":[
			{"name":"Film.2020.1080p.WEB","hash":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","size":1073741824,"stats":{"seeders":10,"leechers":2},"added":"2024-03-07T14:05:09","cat":"movies","cached":true,"free":true,"imdb":"tt1234567"},
			{"name":"Film.2020.720p","hash":"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB","size":1,"stats":{"seeders":1,"leechers":0},"added":"2024-03-07T14:05:09","cat":"movies","cached":false,"free":false},
			{"name":"Film.2020.2160p","hash":"CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC","size":"4 GB","stats":{"seeders":"7","leechers":"1"},"added":"2024-03-08T01:00:00","cat":"movies","cached":true,"free":false,"imdb":"tt1234567"}
		]}}`)
	}))
	defer srv.Close()
	r := testRunner(t, loadDef(t, "json.yml"), srv.URL, runnerSetup{settings: map[string]string{"apikey": "test-key"}})
	ctx := context.Background()
	rels, err := r.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: "Film", Year: 2020, IDs: core.IDs{IMDB: "tt1234567", TMDB: 55}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "test-key" || !strings.Contains(gotQuery, "imdb=1234567") || !strings.Contains(gotQuery, "tmdb=55") || !strings.Contains(gotQuery, "cat=movies") || strings.Contains(gotQuery, "q=") {
		t.Fatalf("request key=%q query=%q", gotKey, gotQuery)
	}
	if len(rels) != 2 {
		t.Fatalf("results = %+v", rels)
	}
	if rels[0].InfoHash != strings.Repeat("a", 40) || rels[0].Size != 1073741824 || rels[0].Seeders != 10 || rels[0].Peers != 12 || rels[0].Category != 2000 {
		t.Fatalf("first = %+v", rels[0])
	}
	if !rels[0].PubDate.Equal(time.Date(2024, 3, 7, 14, 5, 9, 0, time.UTC)) {
		t.Fatalf("date = %v", rels[0].PubDate)
	}
	if rels[1].Size != 4<<30 || rels[1].Seeders != 7 || rels[1].MagnetURL == "" {
		t.Fatalf("second = %+v", rels[1])
	}
	none, err := r.Search(ctx, core.SearchRequest{Type: core.TypeMovie, Title: "nothing"}, nil, nil, nil)
	if err != nil || len(none) != 0 {
		t.Fatalf("noResultsMessage: %v %v", none, err)
	}
}

func TestJSONMultipleRowsReadParent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query_term") == "empty" {
			fmt.Fprint(w, `{"data":{"movie_count":0}}`)
			return
		}
		fmt.Fprint(w, `{"data":{"movies":[
			{"title":"Alpha","year":2001,"torrents":[{"quality":"720p","url":"/t/1","size":"1.2 GB","seeds":4},{"quality":"1080p","url":"/t/2","size":"2.4 GB","seeds":9}]},
			{"title":"Beta","year":2002,"torrents":[{"quality":"2160p","url":"https://cdn.test/t/3","size":"8 GB","seeds":1}]},
			{"title":"Gamma","year":2003}
		]}}`)
	}))
	defer srv.Close()
	r := testRunner(t, loadDef(t, "multi.yml"), srv.URL, runnerSetup{})
	rels, err := r.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "Alpha"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, rel := range rels {
		names = append(names, rel.Name)
	}
	if strings.Join(names, "|") != "Alpha 2001 720p|Alpha 2001 1080p|Beta 2002 2160p" {
		t.Fatalf("names = %v", names)
	}
	if rels[0].NZBURL != srv.URL+"/t/1" || rels[2].NZBURL != "https://cdn.test/t/3" || rels[1].Size != parseSize("2.4 GB") {
		t.Fatalf("links = %+v", rels)
	}
	rels, err = r.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "empty"}, nil, nil, nil)
	if err != nil || len(rels) != 0 {
		t.Fatalf("missing attribute: %v %v", rels, err)
	}
}

func TestXMLFeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, `<?xml version="1.0"?><rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>
<item><title>Show.S02E03.1080p</title><pubDate>Thu, 07 Mar 2024 14:05:09 +0000</pubDate><enclosure url="/get/9" length="2147483648"/>
<torznab:attr name="seeders" value="33"/><torznab:attr name="infohash" value="dddddddddddddddddddddddddddddddddddddddd"/></item>
</channel></rss>`)
	}))
	defer srv.Close()
	r := testRunner(t, loadDef(t, "rss.yml"), srv.URL, runnerSetup{})
	rels, err := r.Search(context.Background(), core.SearchRequest{Type: core.TypeEpisode, Title: "Show", Season: 2, Episode: 3}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].Size != 2147483648 || rels[0].Seeders != 33 || rels[0].InfoHash != strings.Repeat("d", 40) || rels[0].Category != 5040 || rels[0].PubDate.IsZero() {
		t.Fatalf("results = %+v", rels)
	}
}

func TestUsenetDefinitionFetchesNZB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/find":
			fmt.Fprint(w, `<table><tr class="release"><td class="name">Film.2021.1080p.WEB</td><td class="size">3 GB</td><td class="posted">2024-01-02</td><td><a class="nzb" href="/nzb/77">NZB</a></td></tr></table>`)
		case "/nzb/77":
			fmt.Fprint(w, `<?xml version="1.0"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"></nzb>`)
		}
	}))
	defer srv.Close()
	r := testRunner(t, loadDef(t, "usenet.yml"), srv.URL, runnerSetup{})
	var b budget
	rels, err := r.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "Film", Year: 2021}, nil, nil, b.reserve)
	if err != nil || len(rels) != 1 {
		t.Fatalf("search: %v %+v", err, rels)
	}
	if rels[0].Protocol != core.ProtocolUsenet || rels[0].NZBURL != srv.URL+"/nzb/77" || rels[0].Size != 3<<30 {
		t.Fatalf("release = %+v", rels[0])
	}
	body, err := r.FetchNZB(context.Background(), rels[0], b.reserve)
	if err != nil || !strings.Contains(string(body), "<nzb") || b.grabs != 1 {
		t.Fatalf("FetchNZB: %v %q grabs=%d", err, body, b.grabs)
	}
}

func TestInfoHashDownloadBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/s":
			fmt.Fprint(w, `<ul><li><span>Film 2022</span><a href="/d/1">details</a></li></ul>`)
		case "/d/1":
			fmt.Fprint(w, `<h1>Film.2022.1080p</h1><code class="hash">EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE</code>`)
		}
	}))
	defer srv.Close()
	r := testRunner(t, loadDef(t, "hash.yml"), srv.URL, runnerSetup{})
	rels, err := r.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "Film"}, nil, nil, nil)
	if err != nil || len(rels) != 1 {
		t.Fatalf("search: %v %+v", err, rels)
	}
	got, err := r.ResolveTorrent(context.Background(), rels[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.InfoHash != strings.Repeat("e", 40) || !strings.Contains(got.MagnetURL, "dn=Film.2022.1080p") {
		t.Fatalf("resolved = %+v", got)
	}
}

func TestCategoryOnlySitesSkipSearch(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	// The RSS fixture maps only TV categories: a movie search is skipped.
	r := testRunner(t, loadDef(t, "rss.yml"), srv.URL, runnerSetup{})
	rels, err := r.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "Film"}, nil, nil, nil)
	if err != nil || len(rels) != 0 || hits != 0 {
		t.Fatalf("rels=%v err=%v hits=%d", rels, err, hits)
	}
}

func TestSearchErrorsAreReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	r := testRunner(t, loadDef(t, "multi.yml"), srv.URL, runnerSetup{})
	_, err := r.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "x"}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err = %v", err)
	}
	budgetErr := func(context.Context, string) error { return core.ErrBudgetExhausted }
	if _, err := r.Search(context.Background(), core.SearchRequest{Type: core.TypeMovie, Title: "x"}, nil, nil, budgetErr); !errors.Is(err, core.ErrBudgetExhausted) {
		t.Fatalf("budget err = %v", err)
	}
}
