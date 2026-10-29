package jellyfin

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/braedonsaunders/ether/internal/core"
)

const (
	defaultPage = 100
	maxPage     = 500
	// favoritesList is the per-viewer playlist that backs Jellyfin's
	// favourite hearts, so favourites also show up in Plex.
	favoritesList = "Favorites"
)

// ---------------------------------------------------------------------------
// Converting titles

func (s *Server) favorites(ctx context.Context, u *core.User) map[core.RatingKey]bool {
	out := map[core.RatingKey]bool{}
	if s.d.Playlists == nil {
		return out
	}
	lists, err := s.d.Playlists.List(ctx, u.ID)
	if err != nil {
		return out
	}
	for _, l := range lists {
		if l.Title != favoritesList {
			continue
		}
		items := l.Items
		if len(items) == 0 {
			if full, err := s.d.Playlists.Get(ctx, u.ID, l.Key); err == nil && full != nil {
				items = full.Items
			}
		}
		for _, k := range items {
			out[k] = true
		}
	}
	return out
}

// items converts titles for the viewer, reading their watch state in one batch.
func (s *Server) items(ctx context.Context, sess *session, ts []core.Title, detail bool) []item {
	keys := make([]core.RatingKey, len(ts))
	for i := range ts {
		keys[i] = ts[i].Key
	}
	st, _ := s.d.State.GetMany(ctx, sess.User.ID, keys)
	favs := s.favorites(ctx, sess.User)
	out := make([]item, 0, len(ts))
	for i := range ts {
		out = append(out, s.itemFor(&ts[i], st[ts[i].Key], favs[ts[i].Key], detail))
	}
	return out
}

func (s *Server) writeItems(w http.ResponseWriter, sess *session, r *http.Request, ts []core.Title, total, start int) {
	if total < len(ts) {
		total = len(ts)
	}
	writeJSON(w, http.StatusOK, itemsResult{Items: s.items(r.Context(), sess, ts, false), TotalRecordCount: total, StartIndex: start})
}

// ---------------------------------------------------------------------------
// Libraries

func (s *Server) userViews(sess *session) []item {
	var out []item
	for _, v := range views {
		switch v.key {
		case viewMovies:
			if !s.canSee(sess.User, core.TypeMovie) {
				continue
			}
		case viewShows:
			if !s.canSee(sess.User, core.TypeShow) {
				continue
			}
		}
		out = append(out, s.viewItem(v))
	}
	return out
}

func (s *Server) handleViews(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if !s.isSelf(sess, chi.URLParam(r, "uid")) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	v := s.userViews(sess)
	writeJSON(w, http.StatusOK, itemsResult{Items: v, TotalRecordCount: len(v)})
}

// ---------------------------------------------------------------------------
// Browsing

type itemQuery struct {
	p      params
	parent core.RatingKey
	types  map[string]bool
	start  int
	limit  int
}

func (s *Server) parseQuery(r *http.Request) itemQuery {
	p := queryParams(r)
	q := itemQuery{p: p, types: map[string]bool{}, start: max(p.int("startindex", 0), 0)}
	for _, t := range p.list("includeitemtypes") {
		q.types[strings.ToLower(t)] = true
	}
	for _, t := range p.list("excludeitemtypes") {
		delete(q.types, strings.ToLower(t))
	}
	if k, ok := parseItemID(p.get("parentid")); ok {
		q.parent = k
	}
	q.limit = p.int("limit", defaultPage)
	if q.limit <= 0 {
		q.limit = defaultPage
	}
	q.limit = min(q.limit, maxPage)
	return q
}

func (q itemQuery) wants(kinds ...string) bool {
	if len(q.types) == 0 {
		return true
	}
	for _, k := range kinds {
		if q.types[k] {
			return true
		}
	}
	return false
}

func sortFor(p params) (core.SortOrder, bool) {
	by := strings.ToLower(strings.Split(p.get("sortby"), ",")[0])
	desc := strings.HasPrefix(strings.ToLower(p.get("sortorder")), "desc")
	switch by {
	case "datecreated", "datelastcontentadded":
		return core.SortAdded, desc
	case "premieredate", "productionyear", "dateaired":
		return core.SortReleased, desc
	case "communityrating", "criticrating":
		return core.SortRating, desc
	case "random", "playcount":
		return core.SortPopularity, true
	case "":
		return core.SortTitle, desc
	}
	return core.SortTitle, desc
}

func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if !s.isSelf(sess, chi.URLParam(r, "uid")) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	q := s.parseQuery(r)
	p := q.p

	// Explicit ids.
	if ids := p.list("ids"); len(ids) > 0 {
		var ts []core.Title
		for _, id := range ids {
			if k, ok := parseItemID(id); ok {
				if t, err := s.d.Catalog.Get(ctx, k); err == nil && s.visibleOne(ctx, sess.User, t) {
					ts = append(ts, *t)
				}
			}
		}
		s.writeItems(w, sess, r, ts, len(ts), 0)
		return
	}
	if fav, set := p.bool("isfavorite"); set && fav {
		s.writeFavorites(w, r, sess, q)
		return
	}
	for _, f := range p.list("filters") {
		switch strings.ToLower(f) {
		case "isfavorite":
			s.writeFavorites(w, r, sess, q)
			return
		case "isresumable":
			s.handleResume(w, r)
			return
		}
	}

	if q.parent != 0 {
		s.writeChildren(w, r, sess, q)
		return
	}
	// No parent: the whole library, searched or browsed.
	if term := strings.TrimSpace(firstNonEmpty(p.get("searchterm"), p.get("namestartswith"))); term != "" && p.has("searchterm") {
		s.writeSearch(w, r, sess, q, term)
		return
	}
	s.writeBrowse(w, r, sess, q, "")
}

func (q itemQuery) mediaType() core.MediaType {
	movies, shows := q.wants("movie", "video"), q.wants("series")
	switch {
	case movies && !shows:
		return core.TypeMovie
	case shows && !movies:
		return core.TypeShow
	}
	return ""
}

func (s *Server) writeBrowse(w http.ResponseWriter, r *http.Request, sess *session, q itemQuery, force core.MediaType) {
	ctx := r.Context()
	p := q.p
	t := q.mediaType()
	if force != "" {
		t = force
	}
	if (t == core.TypeMovie && !s.canSee(sess.User, core.TypeMovie)) || (t == core.TypeShow && !s.canSee(sess.User, core.TypeShow)) {
		writeJSON(w, http.StatusOK, itemsResult{Items: []item{}})
		return
	}
	// Only seasons or episodes were asked for without a parent: those are
	// reached through their series.
	if len(q.types) > 0 && !q.wants("movie", "video", "series") {
		writeJSON(w, http.StatusOK, itemsResult{Items: []item{}})
		return
	}
	sortBy, desc := sortFor(p)
	f := core.Filter{Type: t, Genres: p.list("genres"), MaxContentRating: sess.User.MaxContentRating}
	if years := p.list("years"); len(years) > 0 {
		lo, hi := 9999, 0
		for _, y := range years {
			n := 0
			for _, c := range y {
				if c >= '0' && c <= '9' {
					n = n*10 + int(c-'0')
				}
			}
			lo, hi = min(lo, n), max(hi, n)
		}
		f.YearFrom, f.YearTo = lo, hi
	}
	if v := p.int("mincommunityrating", 0); v > 0 {
		f.MinRating = float64(v)
	}
	f.Query = strings.TrimSpace(p.get("namestartswith"))
	page, err := s.d.Catalog.Browse(ctx, core.BrowseQuery{Filter: f, Sort: sortBy, Desc: desc, Offset: q.start, Limit: q.limit})
	if err != nil {
		s.writeErr(w, err)
		return
	}
	items := s.visible(ctx, sess.User, page.Items)
	items = s.playedFilter(ctx, sess, p, items)
	s.writeItems(w, sess, r, items, page.Total, q.start)
}

// playedFilter applies IsPlayed / Filters=IsUnplayed to a fetched page.
func (s *Server) playedFilter(ctx context.Context, sess *session, p params, ts []core.Title) []core.Title {
	want, set := p.bool("isplayed")
	for _, f := range p.list("filters") {
		switch strings.ToLower(f) {
		case "isplayed":
			want, set = true, true
		case "isunplayed":
			want, set = false, true
		}
	}
	if !set {
		return ts
	}
	keys := make([]core.RatingKey, len(ts))
	for i := range ts {
		keys[i] = ts[i].Key
	}
	st, _ := s.d.State.GetMany(ctx, sess.User.ID, keys)
	out := ts[:0:0]
	for _, t := range ts {
		if played := st[t.Key].ViewCount > 0; played == want {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) writeSearch(w http.ResponseWriter, r *http.Request, sess *session, q itemQuery, term string) {
	ctx := r.Context()
	var types []core.MediaType
	switch q.mediaType() {
	case core.TypeMovie:
		types = []core.MediaType{core.TypeMovie}
	case core.TypeShow:
		types = []core.MediaType{core.TypeShow}
	default:
		types = []core.MediaType{core.TypeMovie, core.TypeShow}
	}
	found, err := s.d.Catalog.Search(ctx, term, types, q.start+q.limit)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	found = s.visible(ctx, sess.User, found)
	total := len(found)
	if q.start >= len(found) {
		found = nil
	} else {
		found = found[q.start:min(len(found), q.start+q.limit)]
	}
	s.writeItems(w, sess, r, found, total, q.start)
}

func (s *Server) handleSearchHints(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	p := queryParams(r)
	term := strings.TrimSpace(firstNonEmpty(p.get("searchterm"), p.get("term")))
	limit := p.int("limit", 20)
	found, err := s.d.Catalog.Search(r.Context(), term, []core.MediaType{core.TypeMovie, core.TypeShow}, limit)
	if err != nil || term == "" {
		writeJSON(w, http.StatusOK, map[string]any{"SearchHints": []any{}, "TotalRecordCount": 0})
		return
	}
	found = s.visible(r.Context(), sess.User, found)
	hints := make([]map[string]any, 0, len(found))
	for _, t := range found {
		h := map[string]any{"ItemId": itemID(t.Key), "Id": itemID(t.Key), "Name": t.Title, "ProductionYear": t.Year,
			"Type": itemType(t.Type), "IsFolder": t.Type == core.TypeShow, "MatchedTerm": term, "RunTimeTicks": ticks(t.Runtime)}
		if t.PosterPath != "" {
			h["PrimaryImageTag"] = imageTag(t.PosterPath)
		}
		hints = append(hints, h)
	}
	writeJSON(w, http.StatusOK, map[string]any{"SearchHints": hints, "TotalRecordCount": len(hints)})
}

// writeChildren lists what is inside a library, collection, series or season.
func (s *Server) writeChildren(w http.ResponseWriter, r *http.Request, sess *session, q itemQuery) {
	ctx := r.Context()
	switch q.parent {
	case viewMovies:
		s.writeBrowse(w, r, sess, q, core.TypeMovie)
		return
	case viewShows:
		s.writeBrowse(w, r, sess, q, core.TypeShow)
		return
	case viewBoxes:
		s.writeCollections(w, r, sess, q)
		return
	}
	switch q.parent.Kind() {
	case core.KindCollection, core.KindSmart:
		page, err := s.d.Collections.Items(ctx, sess.User, q.parent, q.start, q.limit)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		s.writeItems(w, sess, r, s.visible(ctx, sess.User, page.Items), page.Total, q.start)
	case core.KindShow, core.KindSeason:
		parent, err := s.d.Catalog.Get(ctx, q.parent)
		if err != nil || !s.visibleOne(ctx, sess.User, parent) {
			s.writeErr(w, core.ErrNotFound)
			return
		}
		var kids []core.Title
		if q.parent.Kind() == core.KindShow && q.wants("episode") && !q.wants("season") {
			kids, err = s.d.Catalog.Grandchildren(ctx, q.parent)
		} else {
			kids, err = s.d.Catalog.Children(ctx, q.parent)
		}
		if err != nil {
			s.writeErr(w, err)
			return
		}
		kids = s.releasedChildren(s.visible(ctx, sess.User, kids))
		s.writeItems(w, sess, r, pageOf(kids, q.start, q.limit), len(kids), q.start)
	default:
		writeJSON(w, http.StatusOK, itemsResult{Items: []item{}})
	}
}

// releasedChildren hides episodes that have not aired. Seasons stay, so a
// show's page lists every season, and specials (season 0) sort first like
// Jellyfin.
func (s *Server) releasedChildren(ts []core.Title) []core.Title {
	now := s.now()
	out := make([]core.Title, 0, len(ts))
	for _, t := range ts {
		if t.Type == core.TypeEpisode && !t.IsReleased(now) {
			continue
		}
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Season != out[j].Season {
			return out[i].Season < out[j].Season
		}
		return out[i].Episode < out[j].Episode
	})
	return out
}

func pageOf[T any](xs []T, start, limit int) []T {
	if start >= len(xs) {
		return nil
	}
	return xs[start:min(len(xs), start+limit)]
}

func (s *Server) writeCollections(w http.ResponseWriter, r *http.Request, sess *session, q itemQuery) {
	if s.d.Collections == nil {
		writeJSON(w, http.StatusOK, itemsResult{Items: []item{}})
		return
	}
	cols, err := s.d.Collections.List(r.Context(), sess.User, "")
	if err != nil {
		s.writeErr(w, err)
		return
	}
	var out []item
	for _, c := range cols {
		if c.ChildCount == 0 || !s.canSee(sess.User, firstType(c.Type)) {
			continue
		}
		out = append(out, s.boxSet(c))
	}
	total := len(out)
	writeJSON(w, http.StatusOK, itemsResult{Items: pageOf(out, q.start, q.limit), TotalRecordCount: total, StartIndex: q.start})
}

func firstType(t core.MediaType) core.MediaType {
	if t == "" {
		return core.TypeMovie
	}
	return t
}

func (s *Server) boxSet(c core.Collection) item {
	it := item{
		Name: c.Title, ServerID: s.serverID, ID: itemID(c.Key), Type: "BoxSet", IsFolder: true,
		ParentID: itemID(viewBoxes), Overview: c.Summary, SortName: c.Title, ChildCount: c.ChildCount,
		RecursiveItemCount: c.ChildCount, ImageTags: map[string]string{}, LocationType: "FileSystem",
		DateCreated: dateOrNil(c.UpdatedAt), PrimaryImageAspectRatio: 2.0 / 3.0,
	}
	it.ImageTags["Primary"] = imageTag(c.Key.String() + c.UpdatedAt.String())
	if c.BackdropPath != "" {
		it.BackdropImageTags = []string{imageTag(c.BackdropPath)}
	}
	return it
}

// ---------------------------------------------------------------------------
// One item

func (s *Server) handleItem(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if !s.isSelf(sess, chi.URLParam(r, "uid")) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	if it, ok := s.liveItem(ctx, sess, chi.URLParam(r, "id")); ok {
		writeJSON(w, http.StatusOK, it)
		return
	}
	key, ok := parseItemID(chi.URLParam(r, "id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if isView(key) {
		for _, v := range views {
			if v.key == key {
				writeJSON(w, http.StatusOK, s.viewItem(v))
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	switch key.Kind() {
	case core.KindCollection, core.KindSmart:
		c, err := s.d.Collections.Get(ctx, sess.User, key)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.boxSet(*c))
		return
	}
	t, err := s.d.Catalog.Get(ctx, key)
	if err != nil || !s.visibleOne(ctx, sess.User, t) {
		s.writeErr(w, core.ErrNotFound)
		return
	}
	it := s.items(ctx, sess, []core.Title{*t}, true)[0]
	if t.Type == core.TypeMovie || t.Type == core.TypeEpisode {
		src := s.estimatedSource(ctx, sess, t)
		it.MediaSources = []mediaSource{src}
		it.MediaStreams = src.MediaStreams
		it.Chapters = src.Chapters
	}
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) handleAncestors(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	key, ok := parseItemID(chi.URLParam(r, "id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	var out []item
	switch key.Kind() {
	case core.KindMovie:
		out = append(out, s.viewItem(views[0]))
	case core.KindShow:
		out = append(out, s.viewItem(views[1]))
	case core.KindSeason, core.KindEpisode:
		if p, err := s.d.Catalog.Get(r.Context(), key.Parent()); err == nil {
			out = append(out, s.items(r.Context(), sess, []core.Title{*p}, false)...)
		}
		out = append(out, s.viewItem(views[1]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSimilar(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	key, ok := parseItemID(chi.URLParam(r, "id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	limit := queryParams(r).int("limit", 12)
	sim, err := s.d.Catalog.Similar(r.Context(), key, limit*2)
	if err != nil {
		writeJSON(w, http.StatusOK, itemsResult{Items: []item{}})
		return
	}
	sim = s.visible(r.Context(), sess.User, sim)
	if len(sim) > limit {
		sim = sim[:limit]
	}
	s.writeItems(w, sess, r, sim, len(sim), 0)
}

// ---------------------------------------------------------------------------
// Home rows

func (s *Server) handleLatest(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	ctx := r.Context()
	q := s.parseQuery(r)
	t := q.mediaType()
	switch q.parent {
	case viewMovies:
		t = core.TypeMovie
	case viewShows:
		t = core.TypeShow
	}
	limit := q.p.int("limit", 20)
	page, err := s.d.Catalog.Browse(ctx, core.BrowseQuery{Filter: core.Filter{Type: t, MaxContentRating: sess.User.MaxContentRating},
		Sort: core.SortAdded, Desc: true, Limit: min(limit*3, maxPage)})
	if err != nil {
		s.writeErr(w, err)
		return
	}
	ts := s.visible(ctx, sess.User, page.Items)
	if len(ts) > limit {
		ts = ts[:limit]
	}
	writeJSON(w, http.StatusOK, s.items(ctx, sess, ts, false))
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	ctx := r.Context()
	q := s.parseQuery(r)
	prog, err := s.d.State.InProgress(ctx, sess.User.ID, 100)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	var ts []core.Title
	for _, st := range prog {
		if k := st.Key.Kind(); k != core.KindMovie && k != core.KindEpisode {
			continue
		}
		t, err := s.d.Catalog.Get(ctx, st.Key)
		if err != nil || !s.visibleOne(ctx, sess.User, t) {
			continue
		}
		if q.mediaType() == core.TypeMovie && t.Type != core.TypeMovie {
			continue
		}
		ts = append(ts, *t)
	}
	total := len(ts)
	s.writeItems(w, sess, r, pageOf(ts, q.start, q.limit), total, q.start)
}

// nextUpWindow bounds how much history Next Up reads.
const nextUpWindow = 200

// handleNextUp lists, for each show the viewer has been watching, the
// episode after the one they last finished.
func (s *Server) handleNextUp(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	ctx := r.Context()
	q := s.parseQuery(r)
	hist, err := s.d.State.History(ctx, sess.User.ID, time.Time{}, nextUpWindow)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	seen := map[core.RatingKey]bool{}
	var ts []core.Title
	for _, h := range hist {
		if h.Key.Kind() != core.KindEpisode || !h.Completed {
			continue
		}
		show := core.ShowKey(h.Key.TMDB())
		if seen[show] {
			continue
		}
		seen[show] = true
		if q.parent != 0 && q.parent != show {
			continue
		}
		next, err := s.d.Catalog.NextEpisode(ctx, h.Key)
		if err != nil || next == nil || !next.IsReleased(s.now()) || !s.visibleOne(ctx, sess.User, next) {
			continue
		}
		if st, _ := s.d.State.Get(ctx, sess.User.ID, next.Key); st.ViewCount > 0 {
			continue
		}
		ts = append(ts, *next)
		if len(ts) >= q.limit {
			break
		}
	}
	s.writeItems(w, sess, r, ts, len(ts), 0)
}

// ---------------------------------------------------------------------------
// Shows

func (s *Server) handleSeasons(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	ctx := r.Context()
	key, ok := parseItemID(chi.URLParam(r, "id"))
	if !ok || key.Kind() != core.KindShow {
		http.NotFound(w, r)
		return
	}
	show, err := s.d.Catalog.Get(ctx, key)
	if err != nil || !s.visibleOne(ctx, sess.User, show) {
		s.writeErr(w, core.ErrNotFound)
		return
	}
	kids, err := s.d.Catalog.Children(ctx, key)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	kids = s.releasedChildren(s.visible(ctx, sess.User, kids))
	s.writeItems(w, sess, r, kids, len(kids), 0)
}

func (s *Server) handleEpisodes(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	ctx := r.Context()
	p := queryParams(r)
	key, ok := parseItemID(chi.URLParam(r, "id"))
	if !ok || key.Kind() != core.KindShow {
		http.NotFound(w, r)
		return
	}
	show, err := s.d.Catalog.Get(ctx, key)
	if err != nil || !s.visibleOne(ctx, sess.User, show) {
		s.writeErr(w, core.ErrNotFound)
		return
	}
	var kids []core.Title
	if sid, ok := parseItemID(p.get("seasonid")); ok && sid.Kind() == core.KindSeason {
		kids, err = s.d.Catalog.Children(ctx, sid)
	} else if n := p.int("season", -1); n >= 0 {
		kids, err = s.d.Catalog.Children(ctx, core.SeasonKey(key.TMDB(), n))
	} else {
		kids, err = s.d.Catalog.Grandchildren(ctx, key)
	}
	if err != nil {
		s.writeErr(w, err)
		return
	}
	kids = s.releasedChildren(s.visible(ctx, sess.User, kids))
	start := max(p.int("startindex", 0), 0)
	s.writeItems(w, sess, r, pageOf(kids, start, p.int("limit", maxPage)), len(kids), start)
}

// ---------------------------------------------------------------------------
// Genres and filters

func (s *Server) facetType(p params) core.MediaType {
	switch {
	case strings.Contains(strings.ToLower(p.get("includeitemtypes")), "series") && !strings.Contains(strings.ToLower(p.get("includeitemtypes")), "movie"):
		return core.TypeShow
	}
	if k, ok := parseItemID(p.get("parentid")); ok && k == viewShows {
		return core.TypeShow
	}
	return core.TypeMovie
}

func (s *Server) handleGenres(w http.ResponseWriter, r *http.Request) {
	p := queryParams(r)
	vals, err := s.d.Catalog.Facet(r.Context(), s.facetType(p), "genre")
	if err != nil {
		writeJSON(w, http.StatusOK, itemsResult{Items: []item{}})
		return
	}
	out := make([]item, 0, len(vals))
	for _, v := range vals {
		out = append(out, item{Name: v.Title, ServerID: s.serverID, ID: genreID(v.Title), Type: "Genre", IsFolder: true,
			ImageTags: map[string]string{}, ChildCount: v.Count})
	}
	writeJSON(w, http.StatusOK, itemsResult{Items: out, TotalRecordCount: len(out)})
}

func (s *Server) handleFilters(w http.ResponseWriter, r *http.Request) {
	p := queryParams(r)
	t := s.facetType(p)
	genres, _ := s.d.Catalog.Facet(r.Context(), t, "genre")
	years, _ := s.d.Catalog.Facet(r.Context(), t, "year")
	ratings, _ := s.d.Catalog.Facet(r.Context(), t, "contentRating")
	var yv []int
	for _, y := range years {
		n := 0
		for _, c := range y.Key {
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
			}
		}
		if n > 0 {
			yv = append(yv, n)
		}
	}
	names := func(fs []core.FacetValue) []string {
		out := make([]string, 0, len(fs))
		for _, f := range fs {
			out = append(out, f.Title)
		}
		return out
	}
	writeJSON(w, http.StatusOK, map[string]any{"Genres": names(genres), "Tags": []string{}, "OfficialRatings": names(ratings), "Years": yv})
}

func (s *Server) handleFilters2(w http.ResponseWriter, r *http.Request) {
	p := queryParams(r)
	t := s.facetType(p)
	genres, _ := s.d.Catalog.Facet(r.Context(), t, "genre")
	gi := make([]nameID, 0, len(genres))
	for _, g := range genres {
		gi = append(gi, nameID{Name: g.Title, ID: genreID(g.Title)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"Genres": gi, "Tags": []string{}})
}
