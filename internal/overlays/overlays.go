// Package overlays creates truthful, versioned poster variants in pure Go.
// Quality badges require observed release facts; metadata estimates are never
// treated as evidence of 4K/HDR/Dolby Vision/Atmos availability.
package overlays

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/braedonsaunders/ether/internal/artwork"
	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/ratings"
	"github.com/braedonsaunders/ether/internal/store"
	xdraw "golang.org/x/image/draw"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"strings"
	"sync"
	"time"
)

func init() {
	store.Register("overlays", 1, `CREATE TABLE overlays_settings(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL);CREATE TABLE overlays_artwork(item_key INTEGER PRIMARY KEY,value TEXT NOT NULL);`)
}

type Settings struct {
	Enabled     bool   `json:"enabled"`
	Status      bool   `json:"status"`
	Quality     bool   `json:"quality"`
	Ratings     bool   `json:"ratings"`
	Personalize bool   `json:"personalize"`
	Localize    bool   `json:"localize"`
	Style       string `json:"style"`
	Position    string `json:"position"`
	Accent      string `json:"accent"`
	Size        string `json:"size"`
}
type Variant struct {
	Path     string   `json:"path"`
	Language string   `json:"language,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}
type Artwork struct {
	Posters        []Variant `json:"posters"`
	Backdrops      []Variant `json:"backdrops"`
	PinnedPoster   string    `json:"pinnedPoster,omitempty"`
	PinnedBackdrop string    `json:"pinnedBackdrop,omitempty"`
}
type Facts struct {
	NewEpisode  bool
	NewSeason   bool
	Returning   bool
	LastChance  time.Time
	Height      int
	HDR         bool
	DolbyVision bool
	Atmos       bool
}
type ImageSource interface {
	Get(context.Context, string, int) ([]byte, string, error)
}
type Options struct {
	Images   ImageSource
	Allowed  func(*core.User, *core.Title) bool
	Facts    func(context.Context, *core.User, *core.Title) Facts
	Ratings  *ratings.Service
	Taste    func(context.Context, *core.User) []string
	Now      func() time.Time
	Variants func(context.Context, *core.Title, string) (Artwork, error)
}
type Service struct {
	db         *sql.DB
	o          Options
	mu         sync.Mutex
	cache      map[string][]byte
	cacheBytes int64
}

func New(db *sql.DB, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{db: db, o: o, cache: map[string][]byte{}}
}

type previewSettingsKey struct{}

func (s *Service) Settings(ctx context.Context) (Settings, error) {
	if v, ok := ctx.Value(previewSettingsKey{}).(Settings); ok {
		return normalizeSettings(v), nil
	}
	var v Settings
	var raw string
	e := s.db.QueryRowContext(ctx, `SELECT value FROM overlays_settings WHERE id=1`).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return normalizeSettings(v), nil
	}
	if e != nil {
		return v, e
	}
	e = json.Unmarshal([]byte(raw), &v)
	return normalizeSettings(v), e
}
func (s *Service) SaveSettings(ctx context.Context, v Settings) error {
	v = normalizeSettings(v)
	if err := validateSettings(v); err != nil {
		return err
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO overlays_settings(id,value) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value`, string(raw))
	return e
}
func (s *Service) Artwork(ctx context.Context, key core.RatingKey) (Artwork, error) {
	var raw string
	var v Artwork
	e := s.db.QueryRowContext(ctx, `SELECT value FROM overlays_artwork WHERE item_key=?`, key).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return v, nil
	}
	if e != nil {
		return v, e
	}
	e = json.Unmarshal([]byte(raw), &v)
	return v, e
}
func (s *Service) SaveArtwork(ctx context.Context, key core.RatingKey, v Artwork) error {
	if len(v.Posters) > 50 || len(v.Backdrops) > 50 {
		return errors.New("maximum 50 artwork variants")
	}
	for _, list := range [][]Variant{v.Posters, v.Backdrops} {
		for _, a := range list {
			if !artwork.ValidPath(a.Path) || len(a.Tags) > 20 {
				return errors.New("invalid artwork variant")
			}
		}
	}
	for _, p := range []string{v.PinnedPoster, v.PinnedBackdrop} {
		if p != "" && !artwork.ValidPath(p) {
			return errors.New("invalid pinned artwork")
		}
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO overlays_artwork(item_key,value) VALUES(?,?) ON CONFLICT(item_key) DO UPDATE SET value=excluded.value`, key, string(raw))
	return e
}
func choose(variants []Variant, pinned, fallback, lang string, taste []string) string {
	if pinned != "" {
		return pinned
	}
	best := fallback
	score := -1
	for _, v := range variants {
		n := 0
		if v.Language == strings.Split(lang, "-")[0] {
			n += 100
		} else if v.Language != "" {
			continue
		}
		for _, tag := range v.Tags {
			for _, want := range taste {
				if strings.EqualFold(tag, want) {
					n += 10
				}
			}
		}
		if n > score {
			best = v.Path
			score = n
		}
	}
	return best
}

// Paths returns owner-approved variants, selected by language and actual tags.
func (s *Service) Paths(ctx context.Context, u *core.User, t *core.Title, lang string) (string, string) {
	poster, backdrop := t.PosterPath, t.BackdropPath
	cfg, e := s.Settings(ctx)
	if e != nil || !cfg.Enabled {
		return poster, backdrop
	}
	v, e := s.Artwork(ctx, t.Key)
	if e != nil {
		return poster, backdrop
	}
	var taste []string
	if cfg.Personalize && s.o.Taste != nil {
		taste = s.o.Taste(ctx, u)
	}
	if !cfg.Localize {
		lang = ""
	}
	return choose(v.Posters, v.PinnedPoster, poster, lang, taste), choose(v.Backdrops, v.PinnedBackdrop, backdrop, "", taste)
}
func (s *Service) Labels(ctx context.Context, u *core.User, t *core.Title) []string {
	cfg, e := s.Settings(ctx)
	if e != nil || !cfg.Enabled {
		return nil
	}
	var labels []string
	var f Facts
	if s.o.Facts != nil {
		f = s.o.Facts(ctx, u, t)
	}
	if cfg.Status {
		switch {
		case f.NewSeason:
			labels = append(labels, "NEW SEASON")
		case f.NewEpisode:
			labels = append(labels, "NEW EPISODE")
		case f.Returning:
			labels = append(labels, "RETURNING")
		}
		if !f.LastChance.IsZero() && f.LastChance.After(s.o.Now()) {
			labels = append(labels, "LAST CHANCE IN ROW "+f.LastChance.Format("02 Jan"))
		}
	}
	if cfg.Quality {
		var q []string
		if f.Height >= 2160 {
			q = append(q, "4K")
		}
		if f.DolbyVision {
			q = append(q, "DV")
		} else if f.HDR {
			q = append(q, "HDR")
		}
		if f.Atmos {
			q = append(q, "ATMOS")
		}
		if len(q) > 0 {
			labels = append(labels, strings.Join(q, " · "))
		}
	}
	if cfg.Ratings && s.o.Ratings != nil {
		bs, e := s.o.Ratings.Cached(ctx, t)
		if e == nil {
			for _, b := range bs {
				labels = append(labels, fmt.Sprintf("%s %.1f/%.0f", strings.ToUpper(b.Source), b.Value, b.Scale))
			}
		}
	}
	return labels
}
func (s *Service) Version(ctx context.Context, u *core.User, t *core.Title, lang string) string {
	p, b := s.Paths(ctx, u, t, lang)
	cfg, _ := s.Settings(ctx)
	raw, _ := json.Marshal([]any{p, b, t.LogoPath, s.Labels(ctx, u, t), t.UpdatedAt.Unix(), cfg})
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:8])
}

// Poster decodes bounded images and uses a bounded generated-image cache.
func (s *Service) Poster(ctx context.Context, u *core.User, t *core.Title, lang string) ([]byte, string, error) {
	if s.o.Images == nil {
		return nil, "", errors.New("artwork unavailable")
	}
	path, _ := s.Paths(ctx, u, t, lang)
	data, ct, e := s.o.Images.Get(ctx, path, 500)
	if e != nil {
		return nil, "", e
	}
	labels := s.Labels(ctx, u, t)
	if len(labels) == 0 {
		return data, ct, nil
	}
	settings, e := s.Settings(ctx)
	if e != nil {
		return nil, "", e
	}
	return s.composite(data, labels, settings)
}

func (s *Service) composite(data []byte, labels []string, settings Settings) ([]byte, string, error) {
	settings = normalizeSettings(settings)
	if err := validateSettings(settings); err != nil {
		return nil, "", err
	}
	styleKey, _ := json.Marshal([]any{labels, settings, "compositor-v2"})
	hash := sha256.Sum256(append(append([]byte(nil), data...), styleKey...))
	key := hex.EncodeToString(hash[:])
	s.mu.Lock()
	cached := s.cache[key]
	s.mu.Unlock()
	if cached != nil {
		return cached, "image/jpeg", nil
	}
	// Decoding, scaling and encoding run outside the lock: it only guards
	// the cache, so concurrent posters render in parallel. Two requests for
	// one poster may both render it; the later store wins.
	cfg, _, e := image.DecodeConfig(bytes.NewReader(data))
	if e != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 20_000_000 {
		return nil, "", errors.New("poster dimensions invalid")
	}
	src, _, e := image.Decode(bytes.NewReader(data))
	if e != nil {
		return nil, "", e
	}
	w := 500
	h := src.Bounds().Dy() * w / src.Bounds().Dx()
	if h > 1500 {
		h = 1500
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	if err := paintBadges(dst, labels, settings); err != nil {
		return nil, "", err
	}
	var out bytes.Buffer
	if e = jpeg.Encode(&out, dst, &jpeg.Options{Quality: 88}); e != nil {
		return nil, "", e
	}
	generated := out.Bytes()
	s.mu.Lock()
	if s.cacheBytes+int64(len(generated)) > 16<<20 {
		s.cache = map[string][]byte{}
		s.cacheBytes = 0
	}
	s.cacheBytes -= int64(len(s.cache[key]))
	s.cache[key] = generated
	s.cacheBytes += int64(len(generated))
	s.mu.Unlock()
	return generated, "image/jpeg", nil
}

func (s *Service) Backdrop(ctx context.Context, u *core.User, t *core.Title, lang string) ([]byte, string, error) {
	if s.o.Images == nil {
		return nil, "", errors.New("artwork unavailable")
	}
	_, path := s.Paths(ctx, u, t, lang)
	return s.o.Images.Get(ctx, path, 1280)
}

func (s *Service) Logo(ctx context.Context, t *core.Title) ([]byte, string, error) {
	if s.o.Images == nil {
		return nil, "", errors.New("artwork unavailable")
	}
	return s.o.Images.Get(ctx, t.LogoPath, 500)
}

func (s *Service) visible(u *core.User, t *core.Title) bool {
	if t == nil || t.Adult {
		return false
	}
	if u != nil {
		if catalog.RatingExceeds(t.ContentRating, u.MaxContentRating) {
			return false
		}
		if len(u.AllowedSections) > 0 {
			section := 1
			if t.Type != core.TypeMovie {
				section = 2
			}
			found := false
			for _, id := range u.AllowedSections {
				found = found || id == section
			}
			if !found {
				return false
			}
		}
	}
	return s.o.Allowed == nil || s.o.Allowed(u, t)
}
