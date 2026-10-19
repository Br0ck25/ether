package migrate

import (
	"context"
	"encoding/json"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type catalogFake struct{ core.Catalog }

func (catalogFake) Get(_ context.Context, k core.RatingKey) (*core.Title, error) {
	return &core.Title{Key: k, Title: "Example", Year: 2024, Type: core.TypeMovie, IDs: core.IDs{TMDB: 11}}, nil
}
func (catalogFake) Search(context.Context, string, []core.MediaType, int) ([]core.Title, error) {
	return []core.Title{{Key: core.MovieKey(11), Title: "Example", Year: 2024, Type: core.TypeMovie}}, nil
}
func TestPMSDryRunAndIdempotentImport(t *testing.T) {
	ctx := context.Background()
	db, e := store.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.Exec(`INSERT INTO auth_users(id,plex_id,username,created_at,updated_at)VALUES(1,99,'viewer',0,0)`)
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" {
			t.Errorf("PMS mutation %s", r.Method)
		}
		if r.Header.Get("X-Plex-Token") != "secret" {
			t.Error("missing access token")
		}
		w.Header().Set("Content-Type", "application/xml")
		switch r.URL.Path {
		case "/identity":
			w.Write([]byte(`<MediaContainer machineIdentifier="fixture"/>`))
		case "/library/sections":
			w.Write([]byte(`<MediaContainer><Directory key="1" type="movie" title="Movies"/></MediaContainer>`))
		case "/library/sections/1/all":
			w.Write([]byte(`<MediaContainer totalSize="2"><Video ratingKey="a" type="movie" title="Example" viewCount="3" viewOffset="20000" duration="100000" lastViewedAt="1700000000" userRating="8"><Guid id="tmdb://11"/></Video><Video ratingKey="bad" type="movie" title="Unknown" guid="plex://movie/opaque" viewCount="1"/></MediaContainer>`))
		case "/library/sections/1/collections":
			w.Write([]byte(`<MediaContainer><Directory key="5" title="Favourites"/></MediaContainer>`))
		case "/library/collections/5/children", "/playlists/6/items":
			w.Write([]byte(`<MediaContainer><Video ratingKey="a" type="movie" title="Example"><Guid id="tmdb://11"/></Video></MediaContainer>`))
		case "/status/sessions/history/all":
			w.Write([]byte(`<MediaContainer><Video ratingKey="a" type="movie" title="Example" viewedAt="1700000000"/></MediaContainer>`))
		case "/playlists":
			w.Write([]byte(`<MediaContainer><Playlist ratingKey="6" title="My list"/></MediaContainer>`))
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := New(db, catalogFake{})
	p, e := s.Preview(ctx, Request{URL: srv.URL, Token: "secret", Viewers: []Viewer{{LocalUserID: 1, Name: "Viewer"}}})
	if e != nil {
		t.Fatal(e)
	}
	if calls < 6 || len(p.Items) != 1 || len(p.Lists) != 2 || len(p.Warnings) < 1 {
		t.Fatalf("preview %+v calls %d", p, calls)
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "secret") {
		t.Fatal("preview leaked token")
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM state_watch`).Scan(&n)
	if n != 0 {
		t.Fatal("dry run mutated state")
	}
	for i := 0; i < 2; i++ {
		p, e = s.Import(ctx, p.ID)
		if e != nil || !p.Imported {
			t.Fatalf("import %v %v", p, e)
		}
	}
	for _, table := range []string{"state_watch", "state_history", "state_playlists", "collections_specs"} {
		if e = db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); e != nil || n != 1 {
			t.Fatalf("%s count=%d err=%v", table, n, e)
		}
	}
	var count int
	var offset int64
	db.QueryRow(`SELECT view_count,view_offset_ns FROM state_watch`).Scan(&count, &offset)
	if count != 3 || offset != 20000000000 {
		t.Fatalf("state counts %d offset %d", count, offset)
	}
}
func TestLetterboxdCSVImportExport(t *testing.T) {
	ctx := context.Background()
	db, e := store.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.Exec(`INSERT INTO auth_users(id,plex_id,created_at,updated_at)VALUES(1,99,0,0)`)
	s := New(db, catalogFake{})
	p, e := s.PreviewCSV(ctx, 1, "letterboxd", "history", strings.NewReader("Date,Name,Year,Rating,Watched Date\n2024-03-01,Example,2024,4.5,2024-03-01\n"))
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Items) != 1 || p.Items[0].Rating != 9 {
		t.Fatalf("plan %+v", p)
	}
	if _, e = s.Import(ctx, p.ID); e != nil {
		t.Fatal(e)
	}
	var out strings.Builder
	if e = s.ExportCSV(ctx, 1, "letterboxd", "history", &out); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), "Example,2024,4.5,2024-03-01,11") {
		t.Fatalf("export %s", out.String())
	}
}
func TestRatingAndWatchlistImportPreserveProgress(t *testing.T) {
	ctx := context.Background()
	db, e := store.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.Exec(`INSERT INTO auth_users(id,plex_id,created_at,updated_at)VALUES(1,99,0,0)`)
	key := core.MovieKey(11)
	db.Exec(`INSERT INTO state_watch(user_id,rating_key,view_offset_ns,view_count)VALUES(?,?,123000000000,3)`, 1, key)
	s := New(db, catalogFake{})
	for _, kind := range []string{"ratings", "watchlist"} {
		p, e := s.PreviewCSV(ctx, 1, "letterboxd", kind, strings.NewReader("Name,Year,Rating\nExample,2024,4.5\n"))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Import(ctx, p.ID); e != nil {
			t.Fatal(e)
		}
	}
	var offset int64
	var count int
	var rating float64
	if e = db.QueryRow(`SELECT view_offset_ns,view_count,user_rating FROM state_watch WHERE user_id=1`).Scan(&offset, &count, &rating); e != nil {
		t.Fatal(e)
	}
	if offset != 123000000000 || count != 3 || rating != 9 {
		t.Fatalf("overwrote local state offset=%d count=%d rating=%f", offset, count, rating)
	}
}
