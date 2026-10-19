package personalize

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Quality guards. TMDB ratings are noisy for titles few people have
// rated (a new release at 9.1 from 470 votes, a regional film at 8.8 from
// 400), so everything that selects on rating uses a vote floor and a
// Bayesian weighted rating, and every discovery row skips obscure titles.

// Base vote floors: the votes a title needs before its rating can carry
// a "top rated"-style row on a full TMDB catalogue. Smaller catalogues
// scale them down (builder.voteFloor).
const (
	baseFloorMovie = 1000
	baseFloorShow  = 300
	// priorRating is the weighted rating's prior mean (TMDB's average).
	priorRating = 6.4
)

// priorVotes is how many votes of priorRating a title's own rating is
// blended with: the base floor of its type.
func priorVotes(t core.MediaType) float64 {
	if t == core.TypeShow || t == core.TypeSeason || t == core.TypeEpisode {
		return baseFloorShow
	}
	return baseFloorMovie
}

// weightedRating is the Bayesian (IMDb-style) rating:
// (v·R + m·C) / (v + m), with m the type's base floor and C = 6.4.
func weightedRating(t *core.Title) float64 {
	if t.Rating <= 0 {
		return priorRating
	}
	v := float64(t.VoteCount)
	m := priorVotes(t.Type)
	return (v*t.Rating + m*priorRating) / (v + m)
}

// quality maps the weighted rating onto 0..1 (5.5 → 0, 8.5 → 1): an 8.2
// from 20,000 votes beats a 9.1 from 470.
func quality(t *core.Title) float64 {
	if t.Rating <= 0 {
		return 0.3
	}
	return clamp((weightedRating(t)-5.5)/3, 0, 1)
}

// floors are the catalogue-scaled quality thresholds of one build.
type floors struct {
	votes     map[core.MediaType]float64
	recentPop map[core.MediaType]float64
}

// recentDays is how long a release counts as new for notability.
const recentDays = 120

// floors computes (once per build) the vote floor per type, scaled to the
// catalogue: half the median vote count of its 200 most popular titles,
// capped at the base floor and at least a tenth of it, and the popularity
// a recent release needs to count as notable (the median of recent
// releases).
func (b *builder) floors() *floors {
	if b.fl != nil {
		return b.fl
	}
	f := &floors{votes: map[core.MediaType]float64{}, recentPop: map[core.MediaType]float64{}}
	for _, typ := range []core.MediaType{core.TypeMovie, core.TypeShow} {
		base := priorVotes(typ)
		pop := b.browse(core.BrowseQuery{Filter: core.Filter{Type: typ}, Sort: core.SortPopularity, Desc: true, Limit: 200})
		var votes []float64
		for _, t := range pop {
			votes = append(votes, float64(t.VoteCount))
		}
		floor := base
		if len(votes) >= 20 {
			sort.Float64s(votes)
			floor = clamp(votes[len(votes)/2]*0.5, base/10, base)
		}
		f.votes[typ] = floor
		var recent []float64
		for _, t := range b.browse(core.BrowseQuery{Filter: core.Filter{Type: typ}, Sort: core.SortReleased, Desc: true, Limit: 400}) {
			if !t.Released.IsZero() && !t.Released.After(b.now) && b.now.Sub(t.Released) <= recentDays*24*time.Hour {
				recent = append(recent, t.Popularity)
			}
		}
		rp := 15.0
		if len(recent) >= 20 {
			sort.Float64s(recent)
			rp = recent[len(recent)*3/4]
		}
		f.recentPop[typ] = rp
	}
	b.fl = f
	return f
}

func baseType(t core.MediaType) core.MediaType {
	if t == core.TypeSeason || t == core.TypeEpisode {
		return core.TypeShow
	}
	return t
}

// voteFloor is the votes a title of this type needs for rating-driven
// rows.
func (b *builder) voteFloor(t core.MediaType) float64 {
	if v, ok := b.floors().votes[baseType(t)]; ok {
		return v
	}
	return priorVotes(t)
}

// acclaimed reports whether a title's rating is trustworthy and high: at
// least frac of the vote floor and a weighted rating of at least min.
// Titles released in the last year need three times the votes: early
// ratings come from fans and run high.
func (b *builder) acclaimed(t *core.Title, frac, min float64) bool {
	need := frac * b.voteFloor(t.Type)
	if !t.Released.IsZero() && b.now.Sub(t.Released) < 365*24*time.Hour {
		need *= 3
	}
	return float64(t.VoteCount) >= need && weightedRating(t) >= min
}

// notable reports whether a title is known enough to recommend at all:
// the vote floor; a fifth of it with current popularity; or a recent
// release with at least minRecentVotes votes that is as popular as the
// top quarter of recent releases. Titles that enough people rated poorly
// are never notable.
func (b *builder) notable(t *core.Title) bool {
	// Panned: rated under 6 by enough people, or a weak weighted rating
	// on plenty of votes.
	if t.VoteCount >= 20 && t.Rating > 0 && t.Rating < 6.0 || t.VoteCount >= 200 && weightedRating(t) < 6.0 {
		return false
	}
	floor := b.voteFloor(t.Type)
	switch v := float64(t.VoteCount); {
	case v >= floor:
		return true
	case v >= floor/5 && t.Popularity >= minNotablePopularity:
		return true // fewer votes, but people are still watching it
	}
	if t.VoteCount < minRecentVotes || t.Released.IsZero() || t.Released.After(b.now) || b.now.Sub(t.Released) > recentDays*24*time.Hour {
		return false
	}
	return t.Popularity >= b.floors().recentPop[baseType(t.Type)]
}

// minNotablePopularity is the TMDB popularity a moderately voted title
// needs to stay recommendable (a quarter of the vote floor alone is not
// enough: a series nobody watches any more is not a pick).
const minNotablePopularity = 10

// minRecentVotes: even a hyped release needs a handful of ratings.
const minRecentVotes = 25

// hydrate replaces titles whose details matter (runtime; a series' episode
// count) but are unknown with full catalogue records, within the build's
// budget. Details are cached by the catalogue, so later builds are local.
func (b *builder) hydrate(items []core.Title, need func(*core.Title) bool) []core.Title {
	out := make([]core.Title, 0, len(items))
	for _, t := range items {
		if need(&t) && b.hydrateLeft > 0 && b.ctx.Err() == nil {
			b.hydrateLeft--
			ctx, cancel := context.WithTimeout(b.ctx, b.e.opts.SourceTimeout)
			full, err := b.e.catalog.Get(ctx, t.Key)
			cancel()
			if err == nil && full != nil {
				t = *full
			}
		}
		out = append(out, t)
	}
	return out
}

// hydrateBudget bounds catalogue detail fetches per build.
const hydrateBudget = 80

// Runtime and commitment limits for "Short on Time".
const (
	shortMovieMinutes   = 100
	shortEpisodeMinutes = 35
	// shortSeriesEpisodes: a series you can finish soon.
	shortSeriesEpisodes = 30
	// sitcomEpisodes: self-contained comedy episodes are fine in any
	// order, up to a long-running sitcom; never a 1,000-episode saga.
	sitcomEpisodes = 250
)

// dailyGenres are formats that never fit "start and finish now".
var dailyGenres = map[string]bool{"News": true, "Talk": true, "Reality": true, "Soap": true}

// isShort reports whether a title can be started and finished now: a film
// of at most 100 minutes, or a series with short episodes that is also a
// short commitment (30 episodes or fewer) or a sitcom of self-contained
// episodes. Series with unknown episode counts never qualify.
func isShort(t *core.Title) bool {
	m := minutes(t)
	if m <= 0 {
		return false
	}
	switch t.Type {
	case core.TypeMovie:
		return m <= shortMovieMinutes
	case core.TypeShow:
		if m > shortEpisodeMinutes || t.LeafCount <= 0 {
			return false
		}
		comedy, animated := false, false
		for _, g := range t.Genres {
			if dailyGenres[g] {
				return false
			}
			switch {
			case g == "Comedy":
				comedy = true
			case g == "Animation" || strings.Contains(g, "Anime"):
				animated = true
			}
		}
		if t.LeafCount <= shortSeriesEpisodes {
			return true
		}
		return comedy && !animated && t.LeafCount <= sitcomEpisodes
	}
	return false
}

func needsShortDetails(t *core.Title) bool {
	return minutes(t) <= 0 || (t.Type == core.TypeShow && t.LeafCount <= 0)
}

// showcaseClusters group broad genres so a new viewer's showcase rows
// probe different tastes (one row per cluster).
var showcaseClusters = [][]string{
	{"Action", "Science Fiction", "Thriller", "Adventure", "Fantasy"},
	{"Comedy", "Animation", "Family", "Romance"},
	{"Drama", "Crime", "Mystery", "Documentary", "Horror", "History"},
}

// genrePlural names a showcase row ("Acclaimed Comedies").
var genrePlural = map[string]string{
	"Action": "Action", "Science Fiction": "Sci-Fi", "Thriller": "Thrillers", "Adventure": "Adventures",
	"Fantasy": "Fantasy", "Comedy": "Comedies", "Animation": "Animation", "Family": "Family Favourites",
	"Romance": "Romances", "Drama": "Dramas", "Crime": "Crime", "Mystery": "Mysteries",
	"Documentary": "Documentaries", "Horror": "Horror", "History": "History",
}
