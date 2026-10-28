package overlays

import (
	"context"
	"image/color"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestPresentationUsesRealCatalogueWithoutProvider(t *testing.T) {
	ctx := context.Background()
	db, e := store.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	s := New(db, Options{Now: func() time.Time { return now }, Facts: func(context.Context, *core.User, *core.Title) Facts { return Facts{Returning: true} }})
	title := &core.Title{Key: core.ShowKey(1), Type: core.TypeShow, Year: 2020, Runtime: 45 * time.Minute, ContentRating: "TV-14", Rating: 8.3, VoteCount: 300, Released: now.Add(-365 * 24 * time.Hour)}
	ctx = context.WithValue(ctx, previewSettingsKey{}, Settings{Enabled: true, Status: true, Quality: true, Ratings: true, Details: true})
	report, e := s.Inspect(ctx, nil, title)
	if e != nil {
		t.Fatal(e)
	}
	labels := strings.Join(report.Labels, "|")
	if labels != "TMDB 8.3/10|2020 · 45m · TV-14" {
		t.Fatalf("unexpected labels: %q", labels)
	}
	if report.Layers[0].State != "unavailable" || report.Layers[1].State != "unavailable" {
		t.Fatalf("missing facts not explained: %+v", report)
	}
	for _, v := range []float64{0, -1, 11, math.NaN(), math.Inf(1)} {
		title.Rating = v
		if strings.Contains(strings.Join(s.Labels(ctx, nil, title), "|"), "TMDB") {
			t.Fatal("invalid score rendered")
		}
	}
	title.Rating = 8.3
	title.VoteCount = 0
	if strings.Contains(strings.Join(s.Labels(ctx, nil, title), "|"), "TMDB") {
		t.Fatal("unrated estimate rendered")
	}
	title.Released = now.Add(-24 * time.Hour)
	if !strings.Contains(strings.Join(s.Labels(ctx, nil, title), "|"), "NEW RELEASE") {
		t.Fatal("recent release omitted")
	}
	title.Released = now.Add(24 * time.Hour)
	if strings.Contains(strings.Join(s.Labels(ctx, nil, title), "|"), "NEW RELEASE") {
		t.Fatal("future release rendered")
	}
}

func TestDisabledEvidenceDoesNoWorkAndVersionIgnoresNonVisualChanges(t *testing.T) {
	ctx := context.Background()
	db, e := store.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	factsCalls := 0
	s := New(db, Options{Facts: func(context.Context, *core.User, *core.Title) Facts { factsCalls++; return Facts{} }})
	title := &core.Title{PosterPath: "/a.jpg", Rating: 8, VoteCount: 10}
	cfg := Settings{Enabled: true, Ratings: true}
	ctx = context.WithValue(ctx, previewSettingsKey{}, cfg)
	before := s.Version(ctx, &core.User{ID: 1}, title, "en")
	title.UpdatedAt = time.Now()
	after := s.Version(ctx, &core.User{ID: 2}, title, "fr")
	if before != after {
		t.Fatal("unrelated metadata/viewer forced cache churn")
	}
	if factsCalls != 0 {
		t.Fatal("ratings-only preset looked up quality/status")
	}
	title.Rating = 9
	if before == s.Version(ctx, nil, title, "en") {
		t.Fatal("changed visible score did not change version")
	}
	cfg.Enabled = false
	ctx = context.WithValue(ctx, previewSettingsKey{}, cfg)
	before = s.Version(ctx, nil, title, "en")
	cfg.Style = "solid"
	cfg.Accent = "#ffffff"
	ctx = context.WithValue(ctx, previewSettingsKey{}, cfg)
	if before != s.Version(ctx, nil, title, "en") {
		t.Fatal("hidden badge style invalidated original")
	}
}

func TestExplanationMatchesPreviewAndEnforcesViewerPolicy(t *testing.T) {
	ctx := context.Background()
	db, e := store.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s := New(db, Options{})
	cat := previewCatalog{title: core.Title{Key: core.MovieKey(1), Rating: 8.4, VoteCount: 12, ContentRating: "R"}}
	router := s.Router(cat, func(*http.Request, int64) (*core.User, error) { return &core.User{ID: 1}, nil })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/explain/"+cat.title.Key.String()+"?settings="+url.QueryEscape(`{"enabled":true,"ratings":true}`), nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "TMDB 8.4/10") {
		t.Fatalf("explain %d %s", w.Code, w.Body.String())
	}
	restricted := s.Router(cat, func(*http.Request, int64) (*core.User, error) { return &core.User{ID: 2, MaxContentRating: "PG"}, nil })
	w = httptest.NewRecorder()
	restricted.ServeHTTP(w, httptest.NewRequest("GET", "/explain/"+cat.title.Key.String(), nil))
	if w.Code != 404 {
		t.Fatal("restricted title leaked explanation")
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/explain/"+cat.title.Key.String()+"?settings="+url.QueryEscape(`{"style":"invalid"}`), nil))
	if w.Code != 400 {
		t.Fatal("invalid draft accepted")
	}
}

func TestQualityClassifiesCroppedWidescreenWithoutInventingAvailability(t *testing.T) {
	ctx := context.Background()
	db, e := store.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	facts := Facts{}
	s := New(db, Options{Facts: func(context.Context, *core.User, *core.Title) Facts { return facts }})
	ctx = context.WithValue(ctx, previewSettingsKey{}, Settings{Enabled: true, Quality: true})
	for _, tc := range []struct {
		w, h int
		want string
	}{{3840, 1600, "PLAYED 4K"}, {1920, 800, "PLAYED 1080p"}, {1280, 534, "PLAYED 720p"}, {720, 480, "PLAYED SD"}} {
		facts.Width, facts.Height = tc.w, tc.h
		got := s.Labels(ctx, nil, &core.Title{})
		if len(got) != 1 || got[0] != tc.want {
			t.Fatalf("%dx%d: %v, want %s", tc.w, tc.h, got, tc.want)
		}
	}
}

func TestSolidCustomAccentChoosesReadableInk(t *testing.T) {
	for _, tc := range []struct {
		background color.NRGBA
		dark       bool
	}{
		{color.NRGBA{G: 160, A: 255}, true},
		{color.NRGBA{R: 255, G: 255, B: 255, A: 255}, true},
		{color.NRGBA{A: 255}, false},
	} {
		got := readableInk(tc.background)
		if (got.R < 128) != tc.dark {
			t.Fatalf("accent %+v chose unreadable ink %+v", tc.background, got)
		}
	}
}
