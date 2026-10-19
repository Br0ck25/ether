package tmdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func testClient(t *testing.T, srv *httptest.Server, key string, opts ...Option) *Client {
	t.Helper()
	opts = append([]Option{WithBaseURL(srv.URL)}, opts...)
	c := New(key, slog.Default(), opts...)
	t.Cleanup(c.Close)
	return c
}

func TestAuthDetection(t *testing.T) {
	if !IsBearerKey("eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJ4In0.signature") {
		t.Fatal("JWT should be bearer")
	}
	if IsBearerKey("0123456789abcdef0123456789abcdef") {
		t.Fatal("32-char hex should be v3 api_key")
	}
	var sawAuth, sawKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawKey = r.URL.Query().Get("api_key")
		fmt.Fprint(w, `{"genres":[{"id":28,"name":"Action"}]}`)
	}))
	defer srv.Close()
	ctx := context.Background()
	bearer := testClient(t, srv, "aaa.bbb.ccc")
	if _, err := bearer.Genres(ctx, "movie"); err != nil {
		t.Fatal(err)
	}
	if sawAuth != "Bearer aaa.bbb.ccc" || sawKey != "" {
		t.Fatalf("bearer: auth=%q api_key=%q", sawAuth, sawKey)
	}
	v3 := testClient(t, srv, "0123456789abcdef0123456789abcdef", WithCacheTTL(0))
	if _, err := v3.Genres(ctx, "movie"); err != nil {
		t.Fatal(err)
	}
	if sawAuth != "" || sawKey != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("v3: auth=%q api_key=%q", sawAuth, sawKey)
	}
}

func TestConfigurationAndImageURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/configuration" {
			t.Errorf("path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"images":{"base_url":"http://image.tmdb.org/t/p/","secure_base_url":"https://image.tmdb.org/t/p/","poster_sizes":["w500","original"]}}`)
	}))
	defer srv.Close()
	c := testClient(t, srv, "k")
	// Before config fetch, default base is used.
	if got := c.ImageURL("/abc.jpg", "w500"); got != "https://image.tmdb.org/t/p/w500/abc.jpg" {
		t.Fatalf("default image url: %s", got)
	}
	cfg, err := c.Configuration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Images.SecureBaseURL == "" {
		t.Fatal("no secure base")
	}
	if got := c.ImageURL("abc.jpg", ""); got != "https://image.tmdb.org/t/p/original/abc.jpg" {
		t.Fatalf("image url: %s", got)
	}
	if got := c.ImageURL("", "w500"); got != "" {
		t.Fatalf("empty path: %q", got)
	}
	var fn core.ImageURLFunc = c.ImageURLFunc()
	if fn("/x.jpg", "w200") != "https://image.tmdb.org/t/p/w200/x.jpg" {
		t.Fatal("ImageURLFunc mismatch")
	}
}

func TestMovieDetailsAndDiscoverParams(t *testing.T) {
	var lastQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastQuery = r.URL.RawQuery
		switch {
		case strings.HasPrefix(r.URL.Path, "/movie/550"):
			fmt.Fprint(w, `{"id":550,"title":"Fight Club","release_date":"1999-10-15","runtime":139,"vote_average":8.4,"vote_count":26280,"popularity":61.4,"imdb_id":"tt0137523","genres":[{"id":18,"name":"Drama"}],"credits":{"cast":[{"id":287,"name":"Brad Pitt","character":"Tyler Durden"}],"crew":[{"id":7467,"name":"David Fincher","job":"Director"}]},"release_dates":{"results":[{"iso_3166_1":"US","release_dates":[{"certification":"R","type":3}]}]}}`)
		case r.URL.Path == "/discover/movie":
			fmt.Fprint(w, `{"page":2,"results":[{"id":1,"title":"A","genre_ids":[28]}],"total_pages":5,"total_results":99}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := testClient(t, srv, "k")
	d, err := c.MovieDetails(context.Background(), 550)
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Fight Club" || d.Runtime != 139 || d.IMDBID != "tt0137523" {
		t.Fatalf("details: %+v", d)
	}
	if !strings.Contains(lastQuery, "append_to_response=credits") && !strings.Contains(lastQuery, "append_to_response=credits%2Crelease_dates") {
		t.Fatalf("append_to_response missing: %s", lastQuery)
	}
	p, err := c.DiscoverMovies(context.Background(), DiscoverParams{Page: 2, SortBy: "popularity.desc", VoteCountGTE: 100, Year: 1999})
	if err != nil {
		t.Fatal(err)
	}
	if p.TotalResults != 99 || len(p.Results) != 1 {
		t.Fatalf("discover: %+v", p)
	}
	for _, want := range []string{"page=2", "sort_by=popularity.desc", "vote_count.gte=100", "primary_release_year=1999"} {
		if !strings.Contains(lastQuery, want) {
			t.Errorf("discover query missing %q in %q", want, lastQuery)
		}
	}
}

func TestRetryOn429And5xx(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"status_message":"rate limited"}`)
			return
		}
		if n == 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"genres":[{"id":18,"name":"Drama"}]}`)
	}))
	defer srv.Close()
	c := testClient(t, srv, "k")
	// Backoff starts at 500ms; accept the ~1.5s cost to prove retry behaviour.
	gs, err := c.Genres(context.Background(), "movie")
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 || attempts.Load() != 3 {
		t.Fatalf("genres=%v attempts=%d", gs, attempts.Load())
	}
}

func TestNotFoundAndNoKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"status_message":"The resource you requested could not be found."}`)
	}))
	defer srv.Close()
	c := testClient(t, srv, "k")
	_, err := c.MovieDetails(context.Background(), 999999)
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	empty := testClient(t, srv, "")
	if _, err := empty.Genres(context.Background(), "movie"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("want ErrNoKey, got %v", err)
	}
}

func TestCacheAndETag(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, `{"genres":[{"id":28,"name":"Action"}]}`)
	}))
	defer srv.Close()
	c := testClient(t, srv, "k", WithCacheTTL(30*time.Millisecond))
	ctx := context.Background()
	if _, err := c.Genres(ctx, "movie"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Genres(ctx, "movie"); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("cache: hits=%d want 1", hits.Load())
	}
	time.Sleep(50 * time.Millisecond)
	gs, err := c.Genres(ctx, "movie") // expired -> conditional -> 304 -> cached body
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 || gs[0].Name != "Action" {
		t.Fatalf("after 304: %v", gs)
	}
	if hits.Load() != 2 {
		t.Fatalf("etag: hits=%d want 2", hits.Load())
	}
}

func TestCoalescing(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(80 * time.Millisecond)
		fmt.Fprint(w, `{"genres":[{"id":28,"name":"Action"}]}`)
	}))
	defer srv.Close()
	c := testClient(t, srv, "k")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Genres(context.Background(), "tv")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("coalescing: hits=%d want 1", hits.Load())
	}
}

func TestRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"genres":[]}`)
	}))
	defer srv.Close()
	// Burst 1, then 20/s => 3 distinct requests need ~100ms of refill.
	c := testClient(t, srv, "k", WithRateLimit(20, 1), WithCacheTTL(0))
	start := time.Now()
	for _, mt := range []string{"movie", "tv", "movie"} {
		// Distinct URLs would be ideal; Genres caches per URL with TTL 0 so each goes out.
		if _, err := c.Genres(context.Background(), mt); err != nil {
			t.Fatal(err)
		}
		// Invalidate cache expiry is immediate with TTL 0 (expires == now).
		time.Sleep(2 * time.Millisecond)
	}
	if elapsed := time.Since(start); elapsed < 70*time.Millisecond {
		t.Fatalf("rate limiter too fast: %v", elapsed)
	}
}

func TestContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		fmt.Fprint(w, `{"genres":[]}`)
	}))
	defer srv.Close()
	c := testClient(t, srv, "k")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Genres(ctx, "movie"); err == nil {
		t.Fatal("want context error")
	}
}

func TestEndpointsSmoke(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/tv/100/season/1/episode/2/external_ids"):
			fmt.Fprint(w, `{"id":55,"imdb_id":"tt123","tvdb_id":42}`)
		case strings.HasPrefix(r.URL.Path, "/tv/100/season/1"):
			fmt.Fprint(w, `{"id":10,"name":"Season 1","season_number":1,"episodes":[{"id":55,"name":"Pilot","episode_number":1,"season_number":1,"air_date":"2020-01-01","runtime":45,"still_path":"/s.jpg"}]}`)
		case strings.HasPrefix(r.URL.Path, "/collection/10"):
			fmt.Fprint(w, `{"id":10,"name":"Saga","parts":[{"id":1,"title":"One"}]}`)
		case strings.HasPrefix(r.URL.Path, "/find/tt0137523"):
			if r.URL.Query().Get("external_source") != "imdb_id" {
				t.Errorf("external_source=%s", r.URL.Query().Get("external_source"))
			}
			fmt.Fprint(w, `{"movie_results":[{"id":550,"title":"Fight Club"}]}`)
		case strings.HasPrefix(r.URL.Path, "/person/287"):
			fmt.Fprint(w, `{"id":287,"name":"Brad Pitt"}`)
		case strings.HasPrefix(r.URL.Path, "/search/multi"):
			fmt.Fprint(w, `{"page":1,"results":[{"id":550,"media_type":"movie","title":"Fight Club"}],"total_pages":1,"total_results":1}`)
		case strings.HasPrefix(r.URL.Path, "/trending/movie/week"):
			fmt.Fprint(w, `{"page":1,"results":[{"id":550,"title":"Fight Club"}],"total_pages":1,"total_results":1}`)
		case strings.HasPrefix(r.URL.Path, "/tv/popular"):
			fmt.Fprint(w, `{"page":1,"results":[{"id":100,"name":"Show"}],"total_pages":1,"total_results":1}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := testClient(t, srv, "k")
	ctx := context.Background()
	sd, err := c.SeasonDetails(ctx, 100, 1)
	if err != nil || len(sd.Episodes) != 1 || sd.Episodes[0].Runtime == nil || *sd.Episodes[0].Runtime != 45 {
		t.Fatalf("season: %+v %v", sd, err)
	}
	ext, err := c.EpisodeExternalIDs(ctx, 100, 1, 2)
	if err != nil || ext.IMDBID != "tt123" {
		t.Fatalf("ext: %+v %v", ext, err)
	}
	coll, err := c.CollectionDetails(ctx, 10)
	if err != nil || coll.Name != "Saga" || len(coll.Parts) != 1 {
		t.Fatalf("collection: %+v %v", coll, err)
	}
	found, err := c.FindByExternalID(ctx, "imdb", "tt0137523")
	if err != nil || len(found.MovieResults) != 1 {
		t.Fatalf("find: %+v %v", found, err)
	}
	p, err := c.PersonDetails(ctx, 287)
	if err != nil || p.Name != "Brad Pitt" {
		t.Fatalf("person: %+v %v", p, err)
	}
	multi, err := c.SearchMulti(ctx, "fight", 1)
	if err != nil || len(multi.Results) != 1 || multi.Results[0].AsMovie().Title != "Fight Club" {
		t.Fatalf("multi: %+v %v", multi, err)
	}
	tr, err := c.TrendingMovies(ctx, "week", 1)
	if err != nil || len(tr.Results) != 1 {
		t.Fatalf("trending: %+v %v", tr, err)
	}
	tv, err := c.TVList(ctx, "popular", 1)
	if err != nil || len(tv.Results) != 1 || tv.Results[0].Name != "Show" {
		t.Fatalf("tvlist: %+v %v", tv, err)
	}
	if got := ParseDate("1999-10-15").Year(); got != 1999 {
		t.Fatalf("ParseDate year %d", got)
	}
	if !ParseDate("").IsZero() || !ParseDate("junk").IsZero() {
		t.Fatal("ParseDate junk should be zero")
	}
}
