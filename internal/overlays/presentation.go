package overlays

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// LayerReport describes exactly why a layer is or isn't present in the rendered
// poster. The designer and production compositor share this evaluation.
type LayerReport struct {
	Key    string   `json:"key"`
	State  string   `json:"state"` // ready, unavailable, off
	Labels []string `json:"labels"`
	Reason string   `json:"reason"`
}
type Presentation struct {
	Labels []string      `json:"labels"`
	Layers []LayerReport `json:"layers"`
}

func (s *Service) Inspect(ctx context.Context, u *core.User, t *core.Title) (Presentation, error) {
	cfg, err := s.Settings(ctx)
	if err != nil {
		return Presentation{}, err
	}
	return s.presentation(ctx, u, t, cfg), nil
}

func (s *Service) presentation(ctx context.Context, u *core.User, t *core.Title, cfg Settings) Presentation {
	out := Presentation{Labels: []string{}, Layers: []LayerReport{}}
	var f Facts
	if cfg.Enabled && (cfg.Status || cfg.Quality) {
		if s.o.FactsFor != nil {
			f = s.o.FactsFor(ctx, u, t, cfg.Status, cfg.Quality)
		} else if s.o.Facts != nil {
			f = s.o.Facts(ctx, u, t)
		}
	}
	add := func(key string, on bool, labels []string, ready, missing string) {
		l := LayerReport{Key: key, State: "off", Labels: []string{}, Reason: "Layer switched off"}
		if !cfg.Enabled {
			l.Reason = "Ether posters switched off"
		} else if on {
			l.Labels = labels
			if l.Labels == nil {
				l.Labels = []string{}
			}
			l.State = "unavailable"
			l.Reason = missing
			if len(labels) > 0 {
				l.State = "ready"
				l.Reason = ready
				out.Labels = append(out.Labels, labels...)
			}
		}
		out.Layers = append(out.Layers, l)
	}
	var status []string
	if cfg.Enabled && cfg.Status {
		switch {
		case f.NewSeason:
			status = append(status, "NEW SEASON")
		case f.NewEpisode:
			status = append(status, "NEW EPISODE")
		case (t.Type == core.TypeMovie || t.Type == core.TypeShow) && !t.Released.IsZero() && !t.Released.After(s.o.Now()) && s.o.Now().Sub(t.Released) < 30*24*time.Hour:
			status = append(status, "NEW RELEASE")
		}
		// Returning Series is a broad lifecycle status, not evidence of a recent
		// release. Never turn it into a time-sensitive badge.
		if !f.LastChance.IsZero() && f.LastChance.After(s.o.Now()) {
			status = append(status, "LEAVING ROW "+f.LastChance.Format("02 Jan"))
		}
	}
	add("status", cfg.Status, status, "Recent releases, unwatched episodes or a scheduled row expiry", "No recent release or unwatched new episode")
	var quality []string
	if cfg.Enabled && cfg.Quality {
		var q []string
		switch {
		case f.Width >= 3840 || f.Height >= 2160:
			q = append(q, "4K")
		case f.Width >= 1920 || f.Height >= 1080:
			q = append(q, "1080p")
		case f.Width >= 1280 || f.Height >= 720:
			q = append(q, "720p")
		case f.Height > 0:
			q = append(q, "SD")
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
			quality = append(quality, "PLAYED "+strings.Join(q, " · "))
		}
	}
	add("quality", cfg.Quality, quality, "Verified from this viewer's playback within 30 days; the next release may differ", "No verified playback yet; release-name guesses are excluded")
	var scores []string
	if cfg.Enabled && cfg.Ratings {
		if s.o.Ratings != nil {
			if bs, e := s.o.Ratings.Cached(ctx, t); e == nil {
				for _, b := range bs {
					if b.Scale > 0 && b.Value > 0 && b.Value <= b.Scale && !math.IsNaN(b.Value) && !math.IsInf(b.Value, 0) && !math.IsInf(b.Scale, 0) {
						scores = append(scores, fmt.Sprintf("%s %.1f/%.0f", strings.ToUpper(b.Source), b.Value, b.Scale))
					}
					if len(scores) == 2 {
						break
					}
				}
			}
		}
		// TMDB scores are already in the catalogue. They need no extra provider,
		// network request or invented IMDb identity.
		if len(scores) == 0 && t.VoteCount > 0 && t.Rating > 0 && t.Rating <= 10 && !math.IsNaN(t.Rating) {
			scores = append(scores, fmt.Sprintf("TMDB %.1f/10", t.Rating))
		}
	}
	add("ratings", cfg.Ratings, scores, "Source-labelled catalogue or cached provider scores", "No rated votes yet; optional rating providers can add more sources")
	var details []string
	if cfg.Enabled && cfg.Details {
		var parts []string
		if t.Year > 0 {
			parts = append(parts, fmt.Sprint(t.Year))
		}
		if t.Runtime > 0 {
			parts = append(parts, fmt.Sprintf("%dm", int(t.Runtime/time.Minute)))
		}
		if t.ContentRating != "" {
			parts = append(parts, t.ContentRating)
		}
		if len(parts) > 0 {
			details = append(details, strings.Join(parts, " · "))
		}
	}
	add("details", cfg.Details, details, "Year, runtime and content rating from the catalogue", "No catalogue details available")
	return out
}
