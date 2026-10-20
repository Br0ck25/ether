package personalize

import (
	"context"
	"math"

	"github.com/braedonsaunders/ether/internal/core"
)

// Trend intelligence: a per-title momentum ("buzz") score from public
// crowd signals.
//
//	tmdb  = 0.45·day + 0.35·week + 0.20·max(0, day − week)
//	trakt = 0.45·watching now + 0.35·most watched this week
//	        + 0.10·popular + 0.10·anticipated
//	buzz  = tmdb                                   (Trakt not configured)
//	      = 0.55·tmdb + 0.45·trakt (+0.1 when both agree)
//
// Each term is the title's rank score in that list (1 for the top, falling
// linearly). day − week is velocity: rising today rather than established
// this week. Trakt's public lists are read whenever the owner has set up
// the Trakt integration (its client id is enough); without it buzz is
// TMDB-only. Lists are cached by the catalogue and the Trakt client, so a
// build costs no extra TMDB calls beyond the trending lists other rows
// already read.

// TrendSource is optionally implemented by the Trakt dependency
// (internal/trakt.Client): Trakt's public, crowd-wide lists. It returns
// (nil, nil) when Trakt is not configured.
type TrendSource interface {
	PublicList(ctx context.Context, list string, t core.MediaType, limit int) ([]core.Title, error)
}

// Trakt public list names (internal/trakt).
const (
	traktTrending      = "trending"
	traktWatchedWeekly = "watched_weekly"
	traktPopular       = "popular"
	traktAnticipated   = "anticipated"
)

// buzzEntry is one title's trend momentum.
type buzzEntry struct {
	score float64
	// components, for reasons
	day, week, watching, weekly float64
}

// buzzMap is a build's trend momentum by taste key.
type buzzMap struct {
	items map[core.RatingKey]buzzEntry
	trakt bool // Trakt public lists contributed
}

func (m *buzzMap) score(k core.RatingKey) float64 {
	if m == nil {
		return 0
	}
	return m.items[tasteKey(k)].score
}

// reason is the honest phrase for a title's buzz.
func (m *buzzMap) reason(k core.RatingKey) string {
	if m == nil {
		return ""
	}
	e := m.items[tasteKey(k)]
	switch {
	case e.watching >= 0.5:
		return "People are watching this right now"
	case e.day > 0 && e.day >= e.week+0.2:
		return "Rising fast today"
	case e.weekly >= 0.5:
		return "One of the most watched this week"
	case e.day > 0:
		return "Trending today"
	case e.week > 0 || e.weekly > 0:
		return "Trending this week"
	}
	return "Trending now"
}

// scores returns the plain score map a model keeps for assembly.
func (m *buzzMap) scores() map[core.RatingKey]float64 {
	if m == nil {
		return nil
	}
	out := make(map[core.RatingKey]float64, len(m.items))
	for k, e := range m.items {
		if e.score > 0 {
			out[k] = e.score
		}
	}
	return out
}

// buzzListLimit is how much of each trend list is read.
const buzzListLimit = 40

// buzz computes (once per build) every listed title's trend momentum.
func (b *builder) buzz() *buzzMap {
	if b.buzzM != nil {
		return b.buzzM
	}
	m := &buzzMap{items: map[core.RatingKey]buzzEntry{}}
	b.buzzM = m
	ranks := func(items []core.Title) map[core.RatingKey]float64 {
		out := make(map[core.RatingKey]float64, len(items))
		for i, t := range items {
			k := tasteKey(t.Key)
			if v := rankScore(i, len(items)); v > out[k] {
				out[k] = v
			}
		}
		return out
	}
	day, week := map[core.RatingKey]float64{}, map[core.RatingKey]float64{}
	for _, typ := range []core.MediaType{core.TypeMovie, core.TypeShow} {
		for k, v := range ranks(b.list("trending_day", typ, buzzListLimit)) {
			day[k] = v
		}
		for k, v := range ranks(b.list("trending_week", typ, buzzListLimit)) {
			week[k] = v
		}
	}
	trakt := map[string]map[core.RatingKey]float64{}
	if ts, ok := b.e.trakt.(TrendSource); ok && b.e.trakt != nil {
		for _, list := range []string{traktTrending, traktWatchedWeekly, traktPopular, traktAnticipated} {
			trakt[list] = map[core.RatingKey]float64{}
			for _, typ := range []core.MediaType{core.TypeMovie, core.TypeShow} {
				ctx, cancel := context.WithTimeout(b.ctx, b.e.opts.SourceTimeout)
				items, err := ts.PublicList(ctx, list, typ, buzzListLimit)
				cancel()
				if err != nil {
					b.e.log.Debug("personalize: trakt public list failed", "list", list, "type", typ, "err", err)
					continue
				}
				if len(items) > 0 {
					m.trakt = true
				}
				for k, v := range ranks(items) {
					trakt[list][k] = v
				}
			}
		}
	}
	keys := map[core.RatingKey]bool{}
	for k := range day {
		keys[k] = true
	}
	for k := range week {
		keys[k] = true
	}
	for _, l := range trakt {
		for k := range l {
			keys[k] = true
		}
	}
	for k := range keys {
		e := buzzEntry{day: day[k], week: week[k]}
		tm := 0.45*e.day + 0.35*e.week + 0.20*math.Max(0, e.day-e.week)
		e.score = tm
		if m.trakt {
			e.watching, e.weekly = trakt[traktTrending][k], trakt[traktWatchedWeekly][k]
			tr := 0.45*e.watching + 0.35*e.weekly + 0.10*trakt[traktPopular][k] + 0.10*trakt[traktAnticipated][k]
			e.score = 0.55*tm + 0.45*tr
			if tm > 0 && tr > 0 {
				e.score += 0.1
			}
		}
		e.score = clamp(e.score, 0, 1)
		m.items[k] = e
	}
	return m
}
