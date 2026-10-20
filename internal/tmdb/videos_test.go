package tmdb

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMovieVideosParsingAndFallback(t *testing.T) {
	var gotPath, gotLangs string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotLangs = r.URL.Query().Get("include_video_language")
		fmt.Fprint(w, `{"id":603,"results":[
			{"id":"a1","iso_639_1":"en","iso_3166_1":"US","key":"vKQi3bBA1y8","name":"The Signal Trailer","site":"YouTube","size":1080,"type":"Trailer","official":true,"published_at":"2013-02-18T19:50:53.000Z"},
			{"id":"a2","iso_639_1":"de","iso_3166_1":"DE","key":"123456789","name":"Clip","site":"Vimeo","size":720,"type":"Clip","official":false,"published_at":"2020-01-02T03:04:05Z"}
		]}`)
	}))
	defer srv.Close()
	c := testClient(t, srv, "k")
	vs, err := c.MovieVideos(context.Background(), 603, "de-DE")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/movie/603/videos" {
		t.Errorf("path %q", gotPath)
	}
	if gotLangs != "de,en,null" {
		t.Errorf("include_video_language %q, want de,en,null", gotLangs)
	}
	if len(vs) != 2 {
		t.Fatalf("videos %d", len(vs))
	}
	v := vs[0]
	if v.Site != "YouTube" || v.Key != "vKQi3bBA1y8" || v.Type != VideoTrailer || !v.Official || v.Size != 1080 || v.ISO639 != "en" {
		t.Errorf("video %+v", v)
	}
	if p := v.Published(); p.Year() != 2013 || p.Month() != 2 || p.Day() != 18 {
		t.Errorf("published %v", p)
	}
	if vs[1].Type != VideoClip || vs[1].Site != "Vimeo" {
		t.Errorf("video2 %+v", vs[1])
	}
}

func TestVideoLanguageParamAndTVPaths(t *testing.T) {
	if got := videoLanguageParam("en-US"); got != "en,null" {
		t.Errorf("en: %q", got)
	}
	if got := videoLanguageParam(""); got != "en,null" {
		t.Errorf("empty: %q", got)
	}
	if got := videoLanguageParam("fr"); got != "fr,en,null" {
		t.Errorf("fr: %q", got)
	}
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		fmt.Fprint(w, `{"id":1,"results":[]}`)
	}))
	defer srv.Close()
	c := testClient(t, srv, "k", WithCacheTTL(0))
	ctx := context.Background()
	if _, err := c.TVVideos(ctx, 1399, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SeasonVideos(ctx, 1399, 2, "es"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EpisodeVideos(ctx, 1399, 2, 3, "es"); err != nil {
		t.Fatal(err)
	}
	want := []string{"/tv/1399/videos", "/tv/1399/season/2/videos", "/tv/1399/season/2/episode/3/videos"}
	if fmt.Sprint(paths) != fmt.Sprint(want) {
		t.Errorf("paths %v want %v", paths, want)
	}
}
