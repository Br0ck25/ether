package overlays

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type fakeImages struct {
	data  []byte
	paths []string
}

func (f *fakeImages) Get(_ context.Context, path string, _ int) ([]byte, string, error) {
	f.paths = append(f.paths, path)
	return f.data, "image/jpeg", nil
}
func TestVersionedPersonalizedArtworkAndTruthfulQuality(t *testing.T) {
	ctx := context.Background()
	db, e := store.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	img := image.NewRGBA(image.Rect(0, 0, 500, 750))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{20, 30, 40, 255}), image.Point{}, draw.Src)
	var raw bytes.Buffer
	if e = jpeg.Encode(&raw, img, nil); e != nil {
		t.Fatal(e)
	}
	images := &fakeImages{data: raw.Bytes()}
	facts := Facts{}
	s := New(db, Options{Images: images, Facts: func(context.Context, *core.User, *core.Title) Facts { return facts }, Taste: func(context.Context, *core.User) []string { return []string{"drama"} }})
	cfg := Settings{Enabled: true, Status: true, Quality: true, Personalize: true}
	if e = s.SaveSettings(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	title := &core.Title{Key: core.MovieKey(1), PosterPath: "/default.jpg"}
	if e = s.SaveArtwork(ctx, title.Key, Artwork{Posters: []Variant{{Path: "/action.jpg", Tags: []string{"action"}}, {Path: "/drama.jpg", Tags: []string{"drama"}}}, Backdrops: []Variant{{Path: "/textless.jpg"}}}); e != nil {
		t.Fatal(e)
	}
	p, b := s.Paths(ctx, &core.User{ID: 1}, title, "en")
	if p != "/drama.jpg" || b != "/textless.jpg" {
		t.Fatalf("variants %s %s", p, b)
	}
	before := s.Version(ctx, nil, title, "en")
	if labels := s.Labels(ctx, nil, title); len(labels) != 0 {
		t.Fatal("inferred unobserved quality")
	}
	facts = Facts{NewSeason: true, Height: 2160, DolbyVision: true, Atmos: true}
	after := s.Version(ctx, nil, title, "en")
	if before == after {
		t.Fatal("status did not bust artwork cache")
	}
	data, ct, e := s.Poster(ctx, nil, title, "en")
	if e != nil || ct != "image/jpeg" || bytes.Equal(data, raw.Bytes()) {
		t.Fatalf("poster generation failed %v", e)
	}
	decoded, e := jpeg.Decode(bytes.NewReader(data))
	if e != nil || decoded.Bounds().Dx() != 500 || decoded.Bounds().Dy() != 750 {
		t.Fatalf("poster dimensions %v", e)
	}
	cached, _, e := s.Poster(ctx, nil, title, "en")
	if e != nil || !bytes.Equal(data, cached) {
		t.Fatal("generated cache mismatch")
	}
	if e = s.SaveArtwork(ctx, title.Key, Artwork{PinnedPoster: "https://evil.invalid/a.jpg"}); e == nil {
		t.Fatal("external URL accepted as artwork path")
	}
}
func TestPinnedAndLanguageVariants(t *testing.T) {
	variants := []Variant{{Path: "/en.jpg", Language: "en"}, {Path: "/fr.jpg", Language: "fr"}}
	if got := choose(variants, "", "/default.jpg", "fr-CA", nil); got != "/fr.jpg" {
		t.Fatal(got)
	}
	if got := choose(variants, "/pin.jpg", "/default.jpg", "fr", nil); got != "/pin.jpg" {
		t.Fatal(got)
	}
}

type previewCatalog struct {
	core.Catalog
	title core.Title
}

func (c previewCatalog) Get(context.Context, core.RatingKey) (*core.Title, error) {
	v := c.title
	return &v, nil
}
func TestOwnerPreviewHonorsSelectedViewerSafety(t *testing.T) {
	ctx := context.Background()
	db, e := store.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s := New(db, Options{})
	cat := previewCatalog{title: core.Title{Key: core.MovieKey(1), Type: core.TypeMovie, ContentRating: "R"}}
	router := s.Router(cat, func(*http.Request, int64) (*core.User, error) { return &core.User{ID: 2, MaxContentRating: "PG"}, nil })
	r := httptest.NewRequest("GET", "/preview/"+cat.title.Key.String()+"?userId=2", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("restricted preview %d %s", w.Code, w.Body.String())
	}
}

func TestDesignerDefaultsAndValidation(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db, Options{})
	cfg, err := s.Settings(ctx)
	if err != nil || cfg.Style != "glass" || cfg.Position != "bottom" || cfg.Size != "medium" || cfg.Accent != "#d4a574" {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	router := s.Router(nil, nil)
	for _, body := range []string{`{"style":"neon"}`, `{"position":"left"}`, `{"size":"huge"}`, `{"accent":"red"}`, `{"accent":"#gg0000"}`} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("PUT", "/settings", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("invalid %s: %d", body, w.Code)
		}
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/sample?settings="+url.QueryEscape(body), nil))
		if w.Code != 400 {
			t.Fatalf("invalid sample %s: %d", body, w.Code)
		}
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("PUT", "/settings", strings.NewReader(`{"enabled":true,"accent":"#ABCDEF"}`)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var saved Settings
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings(ctx)
	if err != nil || got != saved || got.Accent != "#abcdef" || got.Style != "glass" {
		t.Fatalf("normalized settings: %+v %+v %v", saved, got, err)
	}
}

func TestLocalizationCanBeDisabled(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db, Options{})
	title := &core.Title{Key: core.MovieKey(1), PosterPath: "/default.jpg"}
	if err := s.SaveArtwork(ctx, title.Key, Artwork{Posters: []Variant{{Path: "/fr.jpg", Language: "fr"}}}); err != nil {
		t.Fatal(err)
	}
	for _, localize := range []bool{false, true} {
		if err := s.SaveSettings(ctx, Settings{Enabled: true, Localize: localize}); err != nil {
			t.Fatal(err)
		}
		p, _ := s.Paths(ctx, nil, title, "fr-CA")
		want := "/default.jpg"
		if localize {
			want = "/fr.jpg"
		}
		if p != want {
			t.Fatalf("localize %v: %s", localize, p)
		}
	}
}

func TestSampleAndDesignerCacheVariants(t *testing.T) {
	s := New(nil, Options{}) // The sample must work without a catalog, database, or remote images.
	w := httptest.NewRecorder()
	s.Router(nil, nil).ServeHTTP(w, httptest.NewRequest("GET", "/sample", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("sample: %d %s", w.Code, w.Body.String())
	}
	img, err := jpeg.Decode(bytes.NewReader(w.Body.Bytes()))
	if err != nil || img.Bounds() != image.Rect(0, 0, 500, 750) {
		t.Fatalf("sample dimensions: %v", err)
	}
	base := normalizeSettings(Settings{Enabled: true, Status: true, Quality: true, Ratings: true})
	baseline, _, err := s.Sample(base)
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := s.Sample(base)
	if err != nil || !bytes.Equal(baseline, again) {
		t.Fatal("sample cache not stable")
	}
	for _, change := range []func(*Settings){func(v *Settings) { v.Style = "solid" }, func(v *Settings) { v.Style = "minimal" }, func(v *Settings) { v.Position = "top" }, func(v *Settings) { v.Size = "small" }, func(v *Settings) { v.Size = "large" }, func(v *Settings) { v.Accent = "#33ccff" }, func(v *Settings) { v.Enabled = false }} {
		cfg := base
		change(&cfg)
		variant, _, err := s.Sample(cfg)
		if err != nil || bytes.Equal(baseline, variant) {
			t.Fatalf("settings did not change sample: %+v %v", cfg, err)
		}
	}
}

func TestStyleChangesInvalidatePlexArtworkVersion(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db, Options{Facts: func(context.Context, *core.User, *core.Title) Facts { return Facts{NewSeason: true} }})
	title := &core.Title{Key: core.MovieKey(1), PosterPath: "/default.jpg"}
	if err := s.SaveSettings(ctx, Settings{Enabled: true, Status: true}); err != nil {
		t.Fatal(err)
	}
	before := s.Version(ctx, nil, title, "en")
	if err := s.SaveSettings(ctx, Settings{Enabled: true, Status: true, Style: "minimal"}); err != nil {
		t.Fatal(err)
	}
	if before == s.Version(ctx, nil, title, "en") {
		t.Fatal("style changes must refresh Plex artwork URLs")
	}
}

func TestBadgesRespectPositionAndPreserveSource(t *testing.T) {
	cfg := normalizeSettings(Settings{Enabled: true, Position: "top"})
	img := image.NewRGBA(image.Rect(0, 0, 500, 750))
	original := color.RGBA{R: 40, G: 80, B: 120, A: 255}
	draw.Draw(img, img.Bounds(), image.NewUniform(original), image.Point{}, draw.Src)
	if err := paintBadges(img, []string{"NEW SEASON", "4K HDR", "IMDb 8.6"}, cfg); err != nil {
		t.Fatal(err)
	}
	if img.RGBAAt(25, 30) == original {
		t.Fatal("top badge missing")
	}
	if img.RGBAAt(250, 700) != original {
		t.Fatal("top position altered bottom artwork")
	}
}
