package ratings

import (
	"context"
	"encoding/json"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSourceScalesAndHonestPlexImages(t *testing.T) {
	bs, e := parseOMDb([]byte(`{"Response":"True","Ratings":[{"Source":"Internet Movie Database","Value":"8.2/10"},{"Source":"Rotten Tomatoes","Value":"94%"},{"Source":"Metacritic","Value":"85/100"}]}`))
	if e != nil || len(bs) != 3 {
		t.Fatalf("parse %v %v", bs, e)
	}
	if bs[0].PlexValue() != 8.2 || bs[1].PlexValue() != 9.4 || bs[1].PlexImage() != "rottentomatoes://image.rating.ripe" || bs[2].PlexImage() != "" {
		t.Fatalf("bad scale/icon %v", bs)
	}
	mdb, e := parseMDBList([]byte(`{"ratings":[{"source":"letterboxd","value":4.1,"votes":20},{"source":"popcorn","value":40},{"source":"tomatoes","value":999},{"source":"imdb","value":null}]}`))
	if e != nil || len(mdb) != 2 || mdb[0].Scale != 5 || mdb[0].PlexValue() != 8.2 || mdb[1].PlexImage() != "rottentomatoes://image.rating.spilled" {
		t.Fatalf("MDB scale %v %v", mdb, e)
	}
}
func TestCacheTTLKeysAndRedaction(t *testing.T) {
	ctx := context.Background()
	db, e := store.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("apikey") != "secret" || r.URL.Query().Get("i") != "tt0000001" {
			t.Error("missing owner key or IMDb ID")
		}
		_, _ = w.Write([]byte(`{"Response":"True","Ratings":[{"Source":"Internet Movie Database","Value":"8/10"}]}`))
	}))
	defer srv.Close()
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	s := New(db, Options{OMDbBaseURL: srv.URL, Now: func() time.Time { return now }})
	cfg := Settings{Enabled: true, Provider: "omdb", APIKey: "secret", Audience: "imdb", CacheHours: 1}
	if e = s.SaveSettings(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	read, e := s.Settings(ctx)
	b, _ := json.Marshal(read)
	if e != nil || strings.Contains(string(b), "secret") || read.APIKey != Redacted {
		t.Fatal("API key exposed")
	}
	if e = s.SaveSettings(ctx, read); e != nil {
		t.Fatal(e)
	}
	title := &core.Title{Key: core.MovieKey(1), IDs: core.IDs{IMDB: "tt0000001"}}
	if _, e = s.Refresh(ctx, title); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Refresh(ctx, title); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("cache did not suppress duplicate lookup")
	}
	now = now.Add(2 * time.Hour)
	cached, e := s.Cached(ctx, title)
	if e != nil || cached != nil {
		t.Fatal("expired cache served as current")
	}
	if _, e = s.Refresh(ctx, title); e != nil || calls != 2 {
		t.Fatalf("cache refresh %v %d", e, calls)
	}
}
func TestMDBListRouteAndMissingValues(t *testing.T) {
	ctx := context.Background()
	db, e := store.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tmdb/show/42/" {
			t.Error(r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ratings":[{"source":"imdb","value":8.4},{"source":"letterboxd","value":null}]}`))
	}))
	defer srv.Close()
	s := New(db, Options{MDBListBaseURL: srv.URL})
	if e = s.SaveSettings(ctx, Settings{Enabled: true, Provider: "mdblist", APIKey: "x", CacheHours: 24}); e != nil {
		t.Fatal(e)
	}
	bs, e := s.Refresh(ctx, &core.Title{Key: core.ShowKey(42), Type: core.TypeShow, IDs: core.IDs{TMDB: 42}})
	if e != nil || len(bs) != 1 || bs[0].Value != 8.4 {
		t.Fatalf("bad lookup %v %v", bs, e)
	}
}
