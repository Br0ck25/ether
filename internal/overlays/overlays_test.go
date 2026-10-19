package overlays

import (
	"bytes"
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
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
