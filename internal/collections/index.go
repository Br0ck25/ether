package collections

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// entry is the compact, in-memory form of one browsable movie or show:
// everything rule evaluation, sorting and title cards need, so a preview
// over the whole catalogue never touches SQLite.
type entry struct {
	key       core.RatingKey
	typ       core.MediaType
	title     string
	sortTitle string
	year      int
	released  int64 // unix seconds, 0 = unknown
	added     int64
	runtime   int // minutes, 0 = unknown
	rating    float64
	votes     int
	pop       float64
	lang      string
	crating   string
	collID    int64
	poster    string
	backdrop  string
	detail    bool

	genres      []string // display names
	genresLC    []string
	countriesLC []string
	studiosLC   []string
	keywordsLC  []string
	cast        []int64 // sorted
	directors   []int64
	writers     []int64
}

func (e *entry) hasPerson(rf ref) bool {
	if rf.ID <= 0 {
		return false
	}
	in := func(xs []int64) bool {
		i := sort.Search(len(xs), func(i int) bool { return xs[i] >= rf.ID })
		return i < len(xs) && xs[i] == rf.ID
	}
	switch rf.Role {
	case "cast":
		return in(e.cast)
	case "director":
		return in(e.directors)
	case "writer":
		return in(e.writers)
	case "crew":
		return in(e.directors) || in(e.writers)
	}
	return in(e.cast) || in(e.directors) || in(e.writers)
}

// card converts an entry to a light core.Title for previews.
func (e *entry) card() core.Title {
	t := core.Title{
		Key: e.key, Type: e.typ, Title: e.title, Year: e.year, Rating: e.rating, VoteCount: e.votes,
		Popularity: e.pop, ContentRating: e.crating, PosterPath: e.poster, BackdropPath: e.backdrop,
		Runtime: time.Duration(e.runtime) * time.Minute, Language: e.lang, Genres: e.genres,
		CollectionID: e.collID,
	}
	if e.released != 0 {
		t.Released = time.Unix(e.released, 0).UTC()
	}
	switch e.typ {
	case core.TypeMovie:
		t.IDs.TMDB = e.key.TMDB()
	case core.TypeShow:
		t.IDs.TMDB = e.key.TMDB()
	}
	return t
}

// entryFromTitle builds an entry from a full title (TMDB results joined to
// the catalogue, or titles loaded from the mirror).
func entryFromTitle(t core.Title, in *interner) *entry {
	e := &entry{
		key: t.Key, typ: t.Type, title: t.Title, sortTitle: t.SortTitle, year: t.Year,
		runtime: int(t.Runtime / time.Minute), rating: t.Rating, votes: t.VoteCount, pop: t.Popularity,
		lang: in.lower(t.Language), crating: in.str(t.ContentRating), collID: t.CollectionID,
		poster: t.PosterPath, backdrop: t.BackdropPath,
	}
	if e.sortTitle == "" {
		e.sortTitle = sortKey(t.Title)
	}
	if e.year == 0 && !t.Released.IsZero() {
		e.year = t.Released.Year()
	}
	if !t.Released.IsZero() {
		e.released = t.Released.Unix()
	}
	for _, g := range t.Genres {
		e.genres = append(e.genres, in.str(g))
		e.genresLC = append(e.genresLC, in.lower(g))
	}
	for _, c := range t.Countries {
		e.countriesLC = append(e.countriesLC, in.lower(c))
	}
	for _, s := range t.Studios {
		e.studiosLC = append(e.studiosLC, in.lower(s))
	}
	for _, k := range t.Keywords {
		e.keywordsLC = append(e.keywordsLC, in.lower(k))
	}
	for _, p := range t.Cast {
		e.cast = append(e.cast, p.TMDB)
	}
	for _, p := range t.Directors {
		e.directors = append(e.directors, p.TMDB)
	}
	for _, p := range t.Writers {
		e.writers = append(e.writers, p.TMDB)
	}
	e.detail = len(t.Keywords) > 0 || len(t.Cast) > 0 || t.Runtime > 0
	e.finish()
	return e
}

func (e *entry) finish() {
	for _, xs := range [][]int64{e.cast, e.directors, e.writers} {
		sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	}
}

func sortKey(s string) string {
	l := strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(l, p) {
			return strings.TrimSpace(l[len(p):])
		}
	}
	return l
}

// interner shares the many repeated strings (genres, studios, languages).
type interner struct{ m map[string]string }

func newInterner() *interner { return &interner{m: map[string]string{}} }

func (in *interner) str(s string) string {
	if s == "" {
		return ""
	}
	if v, ok := in.m[s]; ok {
		return v
	}
	in.m[s] = s
	return s
}

func (in *interner) lower(s string) string { return in.str(strings.ToLower(strings.TrimSpace(s))) }

// index is a snapshot of the browsable catalogue, rebuilt in the background
// when the mirror changes. Readers get the current snapshot without
// blocking; the first load is synchronous.
type index struct {
	db       *sql.DB
	now      func() time.Time
	checkTTL time.Duration

	mu        sync.RWMutex
	snap      *snapshot
	checkedAt time.Time
	building  bool
	buildDone chan struct{}
	extra     map[core.RatingKey]*entry // joined since the last rebuild
	intern    *interner
}

type snapshot struct {
	entries map[core.RatingKey]*entry
	order   []*entry // popularity desc, stable
	print   fingerprint
	builtAt time.Time
}

type fingerprint struct {
	count             int
	maxFetch, maxAdd  int64
	credits, keywords int
}

func newIndex(db *sql.DB, now func() time.Time, checkTTL time.Duration) *index {
	return &index{db: db, now: now, checkTTL: checkTTL, extra: map[core.RatingKey]*entry{}, intern: newInterner()}
}

// get returns the current snapshot, building it on first use and
// refreshing it in the background when the mirror has changed.
func (ix *index) get(ctx context.Context) (*snapshot, error) {
	ix.mu.RLock()
	snap, checked, building := ix.snap, ix.checkedAt, ix.building
	ix.mu.RUnlock()
	if snap == nil {
		return ix.rebuild(ctx)
	}
	if !building && ix.now().Sub(checked) >= ix.checkTTL {
		ix.mu.Lock()
		if !ix.building && ix.now().Sub(ix.checkedAt) >= ix.checkTTL {
			ix.checkedAt = ix.now()
			ix.mu.Unlock()
			fp, err := ix.fingerprint(ctx)
			if err == nil && fp != snap.print {
				if ix.checkTTL <= 0 {
					return ix.rebuild(ctx) // tests: always fresh
				}
				go func() {
					bctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
					defer cancel()
					_, _ = ix.rebuild(bctx)
				}()
			}
		} else {
			ix.mu.Unlock()
		}
	}
	return ix.current(), nil
}

// current returns the snapshot (which already includes joined entries).
func (ix *index) current() *snapshot {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.snap
}

// add records titles joined to the browsable set since the last rebuild.
// The snapshot is copied on write so readers never see a partial update.
func (ix *index) add(ts []core.Title) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	var fresh []*entry
	for _, t := range ts {
		if t.Type != core.TypeMovie && t.Type != core.TypeShow {
			continue
		}
		e := entryFromTitle(t, ix.intern)
		ix.extra[t.Key] = e
		fresh = append(fresh, e)
	}
	if len(fresh) == 0 || ix.snap == nil {
		return
	}
	ix.snap = mergeEntries(ix.snap, fresh)
}

func mergeEntries(base *snapshot, add []*entry) *snapshot {
	out := &snapshot{entries: make(map[core.RatingKey]*entry, len(base.entries)+len(add)),
		order: make([]*entry, 0, len(base.order)+len(add)), print: base.print, builtAt: base.builtAt}
	for k, e := range base.entries {
		out.entries[k] = e
	}
	replaced := map[core.RatingKey]*entry{}
	for _, e := range add {
		if _, ok := out.entries[e.key]; ok {
			replaced[e.key] = e
		}
		out.entries[e.key] = e
	}
	for _, e := range base.order {
		if r, ok := replaced[e.key]; ok {
			out.order = append(out.order, r)
			continue
		}
		out.order = append(out.order, e)
	}
	for _, e := range add {
		if _, ok := replaced[e.key]; !ok {
			out.order = append(out.order, e)
		}
	}
	return out
}

// invalidate forces the next get to check the fingerprint.
func (ix *index) invalidate() {
	ix.mu.Lock()
	ix.checkedAt = time.Time{}
	ix.mu.Unlock()
}

func (ix *index) fingerprint(ctx context.Context) (fingerprint, error) {
	var fp fingerprint
	err := ix.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(fetched_at),0), COALESCE(MAX(added_at),0)
		FROM catalog_titles WHERE type IN ('movie','show') AND browsable=1`).Scan(&fp.count, &fp.maxFetch, &fp.maxAdd)
	return fp, err
}

func (ix *index) rebuild(ctx context.Context) (*snapshot, error) {
	ix.mu.Lock()
	if ix.building {
		done := ix.buildDone
		ix.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if s := ix.current(); s != nil {
			return s, nil
		}
		return ix.rebuild(ctx)
	}
	ix.building = true
	ix.buildDone = make(chan struct{})
	started := ix.now()
	ix.mu.Unlock()

	snap, err := ix.load(ctx)

	ix.mu.Lock()
	ix.building = false
	close(ix.buildDone)
	if err == nil {
		// Keep joined entries the new snapshot does not have yet.
		var keep []*entry
		for k, e := range ix.extra {
			if _, ok := snap.entries[k]; ok {
				delete(ix.extra, k)
				continue
			}
			keep = append(keep, e)
		}
		if len(keep) > 0 {
			snap = mergeEntries(snap, keep)
		}
		ix.snap = snap
		ix.checkedAt = started
	}
	ix.mu.Unlock()
	if err != nil {
		if s := ix.current(); s != nil {
			return s, nil
		}
		return nil, err
	}
	return ix.current(), nil
}

const browsableWhere = `t.type IN ('movie','show') AND t.browsable=1`

func (ix *index) load(ctx context.Context) (*snapshot, error) {
	fp, err := ix.fingerprint(ctx)
	if err != nil {
		return nil, err
	}
	in := newInterner()
	rows, err := ix.db.QueryContext(ctx, `SELECT t.key,t.type,t.title,t.sort_title,t.year,t.released_ts,t.added_at,
		t.runtime_sec,t.rating,t.vote_count,t.popularity,t.language,t.content_rating,t.collection_id,t.poster,t.backdrop,t.detail
		FROM catalog_titles t WHERE `+browsableWhere+` ORDER BY t.popularity DESC, t.key ASC`)
	if err != nil {
		return nil, err
	}
	snap := &snapshot{entries: make(map[core.RatingKey]*entry, fp.count), print: fp, builtAt: ix.now()}
	for rows.Next() {
		var e entry
		var key int64
		var typ, lang, crating string
		var runtimeSec int64
		var detail int
		if err := rows.Scan(&key, &typ, &e.title, &e.sortTitle, &e.year, &e.released, &e.added, &runtimeSec,
			&e.rating, &e.votes, &e.pop, &lang, &crating, &e.collID, &e.poster, &e.backdrop, &detail); err != nil {
			rows.Close()
			return nil, err
		}
		e.key = core.RatingKey(key)
		e.typ = core.MediaType(in.str(typ))
		e.lang = in.lower(lang)
		e.crating = in.str(crating)
		e.runtime = int(runtimeSec / 60)
		e.detail = detail != 0
		if e.sortTitle == "" {
			e.sortTitle = sortKey(e.title)
		}
		ep := &e
		snap.entries[ep.key] = ep
		snap.order = append(snap.order, ep)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	pairs := func(q string, apply func(e *entry, v string)) error {
		rows, err := ix.db.QueryContext(ctx, q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k int64
			var v string
			if err := rows.Scan(&k, &v); err != nil {
				return err
			}
			if e := snap.entries[core.RatingKey(k)]; e != nil {
				apply(e, v)
			}
		}
		return rows.Err()
	}
	if err := pairs(`SELECT g.key, g.genre FROM catalog_genres g JOIN catalog_titles t ON t.key=g.key WHERE `+browsableWhere+` ORDER BY g.key, g.genre`,
		func(e *entry, v string) {
			e.genres = append(e.genres, in.str(v))
			e.genresLC = append(e.genresLC, in.lower(v))
		}); err != nil {
		return nil, err
	}
	if err := pairs(`SELECT c.key, c.country FROM catalog_countries c JOIN catalog_titles t ON t.key=c.key WHERE `+browsableWhere,
		func(e *entry, v string) { e.countriesLC = append(e.countriesLC, in.lower(v)) }); err != nil {
		return nil, err
	}
	if err := pairs(`SELECT s.key, s.studio FROM catalog_studios s JOIN catalog_titles t ON t.key=s.key WHERE `+browsableWhere,
		func(e *entry, v string) { e.studiosLC = append(e.studiosLC, in.lower(v)) }); err != nil {
		return nil, err
	}
	if err := pairs(`SELECT k.key, k.keyword FROM catalog_keywords k JOIN catalog_titles t ON t.key=k.key WHERE `+browsableWhere,
		func(e *entry, v string) { e.keywordsLC = append(e.keywordsLC, in.lower(v)) }); err != nil {
		return nil, err
	}
	crows, err := ix.db.QueryContext(ctx, `SELECT c.key, c.person_tmdb, c.kind FROM catalog_credits c JOIN catalog_titles t ON t.key=c.key WHERE `+browsableWhere)
	if err != nil {
		return nil, err
	}
	for crows.Next() {
		var k, pid int64
		var kind string
		if err := crows.Scan(&k, &pid, &kind); err != nil {
			crows.Close()
			return nil, err
		}
		e := snap.entries[core.RatingKey(k)]
		if e == nil {
			continue
		}
		switch kind {
		case "director":
			e.directors = append(e.directors, pid)
		case "writer":
			e.writers = append(e.writers, pid)
		default:
			e.cast = append(e.cast, pid)
		}
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return nil, err
	}
	for _, e := range snap.order {
		e.finish()
		if len(e.keywordsLC) > 0 || len(e.cast) > 0 || e.runtime > 0 {
			e.detail = true
		}
	}
	return snap, nil
}

// lookupRows loads entries for keys that are not in the snapshot (titles
// in the mirror that are not browsable yet).
func (ix *index) lookupRows(ctx context.Context, keys []core.RatingKey) (map[core.RatingKey]*entry, error) {
	out := map[core.RatingKey]*entry{}
	if len(keys) == 0 {
		return out, nil
	}
	titles, err := loadTitles(ctx, ix.db, keys)
	if err != nil {
		return nil, err
	}
	in := newInterner()
	for _, t := range titles {
		out[t.Key] = entryFromTitle(t, in)
	}
	return out, nil
}
