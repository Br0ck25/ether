package personalize

import (
	"context"
	"math"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Within-session live intent (roadmap item #32): a detail-page open that
// does not turn into playback is the viewer telling us, tonight, what they
// are in the mood for. Opens are kept in memory per viewer, decay with a
// ~45 minute half-life, and at assembly time apply a bounded boost (at
// most intentMaxBoost) to titles matching the browsed genres, decades and
// people. Nothing is persisted and the taste model itself is untouched, so
// the effect is immediate and gone with the session. Content restrictions
// and taste vetoes are checked by the callers as usual; the boost only
// ever lifts titles already eligible for a row.

const (
	// intentHalfLife is the decay half-life of one detail open.
	intentHalfLife = 45 * time.Minute
	// intentMaxAge drops opens whose decayed weight rounds to nothing.
	intentMaxAge = 4 * time.Hour
	// intentConvertWindow: playback of the same title starting within
	// this long of the open means the open was a conversion (already a
	// strong signal in the state history), not browsing intent.
	intentConvertWindow = 4 * time.Minute
	// intentMaxBoost bounds the assembly-time boost one title can gain.
	intentMaxBoost = 0.25
	// Bounds on the store: opens per viewer, viewers tracked.
	intentMaxOpens = 24
	intentMaxUsers = 512
)

// intentOpen is one detail-page open with the features it votes for.
type intentOpen struct {
	key    core.RatingKey // taste key (episodes fold onto their show)
	at     time.Time
	genres []string
	decade int
	people []int64 // directors, writers, top cast TMDB ids
}

type userIntent struct {
	opens   []intentOpen
	version uint64 // bumps on every change; part of the assembly memo key
}

// intentSnapshot is the decayed, bounded boost view of a viewer's opens at
// one moment: per-feature boosts already scaled into 0..intentMaxBoost.
type intentSnapshot struct {
	genre  map[string]float64
	person map[int64]float64
	decade map[int]float64
}

func (s *intentSnapshot) active() bool {
	return s != nil && len(s.genre)+len(s.person)+len(s.decade) > 0
}

// boostFor is the assembly-time boost of one title, in 0..intentMaxBoost:
// mostly genre agreement, then the strongest matching person, then decade.
func (s *intentSnapshot) boostFor(t *core.Title) float64 {
	if !s.active() || t == nil {
		return 0
	}
	b := 0.0
	if gs := normGenres(t.Genres); len(gs) > 0 {
		sum := 0.0
		for _, g := range gs {
			sum += s.genre[g]
		}
		b += 0.6 * sum / float64(len(gs))
	}
	best := 0.0
	see := func(p core.Person) {
		if v := s.person[p.TMDB]; v > best {
			best = v
		}
	}
	for _, p := range t.Directors {
		see(p)
	}
	for i, p := range t.Writers {
		if i < 3 {
			see(p)
		}
	}
	for i, p := range t.Cast {
		if i < 5 {
			see(p)
		}
	}
	b += 0.4 * best
	if t.Year > 0 {
		b += 0.2 * s.decade[t.Year/10*10]
	}
	return clamp(b, 0, intentMaxBoost)
}

// intentEnabled reports whether the owner left session intent on (default).
func (e *Engine) intentEnabled() bool {
	return e.opts.IntentEnabled == nil || e.opts.IntentEnabled()
}

// intentFeatures extracts the features an open of t votes for. Episodes and
// seasons vote with their show's features when those can be fetched cheaply
// from the catalogue; an episode record alone usually has none.
func (e *Engine) intentFeatures(ctx context.Context, t *core.Title) (genres []string, decade int, people []int64, ok bool) {
	src := t
	if (t.Type == core.TypeEpisode || t.Type == core.TypeSeason) && len(t.Genres) == 0 && len(t.Cast) == 0 {
		if show, err := e.catalog.Get(ctx, core.ShowKey(t.Key.TMDB())); err == nil && show != nil {
			src = show
		}
	}
	genres = normGenres(src.Genres)
	if src.Year > 0 {
		decade = src.Year / 10 * 10
	}
	add := func(p core.Person) {
		if p.TMDB != 0 {
			people = append(people, p.TMDB)
		}
	}
	for _, p := range src.Directors {
		add(p)
	}
	for i, p := range src.Writers {
		if i < 3 {
			add(p)
		}
	}
	for i, p := range src.Cast {
		if i < 5 {
			add(p)
		}
	}
	return genres, decade, people, len(genres) > 0 || len(people) > 0 || decade > 0
}

// RecordDetailOpen implements core.IntentTracker: one single-item detail
// open. Re-opening the same title refreshes its vote instead of stacking.
func (e *Engine) RecordDetailOpen(ctx context.Context, user *core.User, t *core.Title) {
	if user == nil || user.ID == 0 || t == nil || !e.intentEnabled() {
		return
	}
	genres, decade, people, ok := e.intentFeatures(ctx, t)
	if !ok {
		return
	}
	now := e.now()
	key := tasteKey(t.Key)
	hs := e.hookState()
	hs.mu.Lock()
	defer hs.mu.Unlock()
	ui := hs.intent[user.ID]
	if ui == nil {
		if len(hs.intent) >= intentMaxUsers {
			hs.pruneIntentLocked(now)
			if len(hs.intent) >= intentMaxUsers {
				return
			}
		}
		ui = &userIntent{}
		hs.intent[user.ID] = ui
	}
	ui.opens = pruneOpens(ui.opens, now)
	found := false
	for i := range ui.opens {
		if ui.opens[i].key == key {
			ui.opens[i] = intentOpen{key: key, at: now, genres: genres, decade: decade, people: people}
			found = true
		}
	}
	if !found {
		ui.opens = append(ui.opens, intentOpen{key: key, at: now, genres: genres, decade: decade, people: people})
		if len(ui.opens) > intentMaxOpens {
			ui.opens = ui.opens[len(ui.opens)-intentMaxOpens:]
		}
	}
	ui.version++
}

// NotePlayback implements core.IntentTracker: playback starting within
// intentConvertWindow of an open for the same title removes that open —
// it converted, and the play itself lands in the watch history.
func (e *Engine) NotePlayback(user *core.User, key core.RatingKey) {
	if user == nil || user.ID == 0 || !e.intentEnabled() {
		return
	}
	now := e.now()
	tk := tasteKey(key)
	hs := e.hookState()
	hs.mu.Lock()
	defer hs.mu.Unlock()
	ui := hs.intent[user.ID]
	if ui == nil {
		return
	}
	kept := ui.opens[:0:0]
	removed := false
	for _, o := range ui.opens {
		if o.key == tk && now.Sub(o.at) <= intentConvertWindow {
			removed = true
			continue
		}
		kept = append(kept, o)
	}
	if removed {
		ui.opens = kept
		ui.version++
	}
}

// intentFor returns the viewer's decayed snapshot and a memo version. The
// version mixes the change counter with a 5-minute time bucket while any
// open is live, so assembled rows also refresh as intent decays.
func (e *Engine) intentFor(userID int64, now time.Time) (*intentSnapshot, uint64) {
	if userID == 0 || !e.intentEnabled() {
		return nil, 0
	}
	hs := e.hookState()
	hs.mu.Lock()
	ui := hs.intent[userID]
	if ui == nil {
		hs.mu.Unlock()
		return nil, 0
	}
	ui.opens = pruneOpens(ui.opens, now)
	opens := append([]intentOpen(nil), ui.opens...)
	version := ui.version
	hs.mu.Unlock()
	if len(opens) == 0 {
		return nil, 0
	}
	gw, pw, dw := map[string]float64{}, map[int64]float64{}, map[int]float64{}
	for _, o := range opens {
		w := intentWeight(now.Sub(o.at))
		if w <= 0 {
			continue
		}
		for _, g := range o.genres {
			gw[g] += w
		}
		if o.decade > 0 {
			dw[o.decade] += w
		}
		for _, p := range o.people {
			pw[p] += w
		}
	}
	snap := &intentSnapshot{
		genre:  scaleBoosts(gw),
		person: scaleBoosts(pw),
		decade: scaleBoosts(dw),
	}
	if !snap.active() {
		return nil, 0
	}
	bucket := uint64(now.Unix() / 300)
	return snap, version<<32 | bucket&0xffffffff
}

// intentWeight is the decayed weight of an open of the given age.
func intentWeight(age time.Duration) float64 {
	if age < 0 {
		age = 0
	}
	if age > intentMaxAge {
		return 0
	}
	return math.Pow(0.5, float64(age)/float64(intentHalfLife))
}

// scaleBoosts maps accumulated decayed weights onto 0..intentMaxBoost: one
// fresh open is a clear but modest vote, repeated browsing saturates.
func scaleBoosts[K comparable](m map[K]float64) map[K]float64 {
	out := make(map[K]float64, len(m))
	for k, w := range m {
		if v := intentMaxBoost * w / (w + 1.0); v >= 0.01 {
			out[k] = round3(v)
		}
	}
	return out
}

func pruneOpens(opens []intentOpen, now time.Time) []intentOpen {
	kept := opens[:0:0]
	for _, o := range opens {
		if intentWeight(now.Sub(o.at)) > 0 {
			kept = append(kept, o)
		}
	}
	return kept
}

// pruneIntentLocked drops viewers whose opens have all decayed away.
func (hs *hookState) pruneIntentLocked(now time.Time) {
	for id, ui := range hs.intent {
		ui.opens = pruneOpens(ui.opens, now)
		if len(ui.opens) == 0 {
			delete(hs.intent, id)
		}
	}
}

// liftFor is the row-order multiplier of a row whose head items match the
// session intent: 1 + mean boost of its first few items (≤ 1.25).
func (s *intentSnapshot) liftFor(items []candItem) float64 {
	if !s.active() || len(items) == 0 {
		return 1
	}
	n := min(5, len(items))
	sum := 0.0
	for _, it := range items[:n] {
		sum += s.boostFor(&it.t)
	}
	return 1 + sum/float64(n)
}

// genreVetoed reports whether the taste vetoes the title (a genre at
// -0.6 or below, mirroring Taste.Fit): session intent never lifts those.
func genreVetoed(ts *Taste, t *core.Title) bool {
	if ts == nil {
		return false
	}
	for _, g := range normGenres(t.Genres) {
		if ts.Genre[g] <= -0.6 {
			return true
		}
	}
	return false
}

var _ core.IntentTracker = (*Engine)(nil)
