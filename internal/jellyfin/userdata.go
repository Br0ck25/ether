package jellyfin

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/braedonsaunders/ether/internal/core"
)

// ---------------------------------------------------------------------------
// Played, favourite, rating

// userKey resolves the {id} of a state route to a playable or listable
// title the viewer can see, writing the error itself when there is none.
func (s *Server) userTitle(w http.ResponseWriter, r *http.Request) (*session, *core.Title, bool) {
	sess := sessionFrom(r.Context())
	if !s.isSelf(sess, chi.URLParam(r, "uid")) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, nil, false
	}
	key, ok := parseItemID(chi.URLParam(r, "id"))
	if !ok {
		http.NotFound(w, r)
		return nil, nil, false
	}
	t, err := s.d.Catalog.Get(r.Context(), key)
	if err != nil || !s.visibleOne(r.Context(), sess.User, t) {
		s.writeErr(w, core.ErrNotFound)
		return nil, nil, false
	}
	return sess, t, true
}

func (s *Server) writeUserData(w http.ResponseWriter, r *http.Request, sess *session, t *core.Title) {
	st, _ := s.d.State.Get(r.Context(), sess.User.ID, t.Key)
	fav := s.favorites(r.Context(), sess.User)[t.Key]
	writeJSON(w, http.StatusOK, s.userData(st, t.Key, t, fav))
}

func (s *Server) handleGetUserData(w http.ResponseWriter, r *http.Request) {
	if sess, t, ok := s.userTitle(w, r); ok {
		s.writeUserData(w, r, sess, t)
	}
}

// markShow applies a played mark to every aired episode of a show or season,
// as Jellyfin does.
func (s *Server) setPlayed(ctx context.Context, sess *session, t *core.Title, played bool) error {
	apply := func(k core.RatingKey) error {
		if played {
			return s.d.State.Scrobble(ctx, sess.User.ID, k)
		}
		return s.d.State.Unscrobble(ctx, sess.User.ID, k)
	}
	switch t.Type {
	case core.TypeShow, core.TypeSeason:
		var eps []core.Title
		var err error
		if t.Type == core.TypeShow {
			eps, err = s.d.Catalog.Grandchildren(ctx, t.Key)
		} else {
			eps, err = s.d.Catalog.Children(ctx, t.Key)
		}
		if err != nil {
			return err
		}
		for _, e := range eps {
			if e.Type != core.TypeEpisode || (played && !e.IsReleased(s.now())) {
				continue
			}
			if err := apply(e.Key); err != nil {
				return err
			}
		}
		return nil
	}
	return apply(t.Key)
}

func (s *Server) handleMarkPlayed(w http.ResponseWriter, r *http.Request) {
	sess, t, ok := s.userTitle(w, r)
	if !ok {
		return
	}
	if err := s.setPlayed(r.Context(), sess, t, true); err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeUserData(w, r, sess, t)
}

func (s *Server) handleMarkUnplayed(w http.ResponseWriter, r *http.Request) {
	sess, t, ok := s.userTitle(w, r)
	if !ok {
		return
	}
	if err := s.setPlayed(r.Context(), sess, t, false); err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeUserData(w, r, sess, t)
}

func (s *Server) handleRate(w http.ResponseWriter, r *http.Request) {
	sess, t, ok := s.userTitle(w, r)
	if !ok {
		return
	}
	rating := 10.0
	if v, set := queryParams(r).bool("likes"); set && !v {
		rating = 2
	}
	if err := s.d.State.Rate(r.Context(), sess.User.ID, t.Key, rating); err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeUserData(w, r, sess, t)
}

func (s *Server) handleUnrate(w http.ResponseWriter, r *http.Request) {
	sess, t, ok := s.userTitle(w, r)
	if !ok {
		return
	}
	if err := s.d.State.Rate(r.Context(), sess.User.ID, t.Key, 0); err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeUserData(w, r, sess, t)
}

// favoritesPlaylist finds the viewer's Favorites playlist.
func (s *Server) favoritesPlaylist(ctx context.Context, u *core.User) *core.Playlist {
	lists, err := s.d.Playlists.List(ctx, u.ID)
	if err != nil {
		return nil
	}
	for i := range lists {
		if lists[i].Title == favoritesList {
			l := lists[i]
			if len(l.Items) == 0 {
				if full, err := s.d.Playlists.Get(ctx, u.ID, l.Key); err == nil && full != nil {
					l = *full
				}
			}
			return &l
		}
	}
	return nil
}

func (s *Server) handleFavorite(w http.ResponseWriter, r *http.Request) {
	sess, t, ok := s.userTitle(w, r)
	if !ok {
		return
	}
	if s.d.Playlists == nil {
		http.Error(w, "favourites are not available", http.StatusNotImplemented)
		return
	}
	ctx := r.Context()
	var err error
	if pl := s.favoritesPlaylist(ctx, sess.User); pl == nil {
		_, err = s.d.Playlists.Create(ctx, sess.User.ID, favoritesList, []core.RatingKey{t.Key})
	} else if !contains(pl.Items, t.Key) {
		err = s.d.Playlists.Add(ctx, sess.User.ID, pl.Key, []core.RatingKey{t.Key})
	}
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.writeUserData(w, r, sess, t)
}

func (s *Server) handleUnfavorite(w http.ResponseWriter, r *http.Request) {
	sess, t, ok := s.userTitle(w, r)
	if !ok {
		return
	}
	if s.d.Playlists != nil {
		ctx := r.Context()
		if pl := s.favoritesPlaylist(ctx, sess.User); pl != nil {
			for i, k := range pl.Items {
				if k == t.Key {
					if err := s.d.Playlists.Remove(ctx, sess.User.ID, pl.Key, i); err != nil {
						s.writeErr(w, err)
						return
					}
					break
				}
			}
		}
	}
	s.writeUserData(w, r, sess, t)
}

func contains(ks []core.RatingKey, k core.RatingKey) bool {
	for _, v := range ks {
		if v == k {
			return true
		}
	}
	return false
}

func (s *Server) writeFavorites(w http.ResponseWriter, r *http.Request, sess *session, q itemQuery) {
	ctx := r.Context()
	var ts []core.Title
	if s.d.Playlists != nil {
		if pl := s.favoritesPlaylist(ctx, sess.User); pl != nil {
			for _, k := range pl.Items {
				if t, err := s.d.Catalog.Get(ctx, k); err == nil && s.visibleOne(ctx, sess.User, t) {
					if q.wants(strings.ToLower(itemType(t.Type))) {
						ts = append(ts, *t)
					}
				}
			}
		}
	}
	s.writeItems(w, sess, r, pageOf(ts, q.start, q.limit), len(ts), q.start)
}

// ---------------------------------------------------------------------------
// Artwork

// Apps ask for a width; TMDB serves fixed sizes.
var (
	posterSizes   = []int{92, 154, 185, 342, 500, 780}
	backdropSizes = []int{300, 780, 1280}
)

func pickSize(want int, sizes []int) string {
	if want <= 0 {
		return "w" + strconv.Itoa(sizes[len(sizes)-2])
	}
	for _, s := range sizes {
		if s >= want {
			return "w" + strconv.Itoa(s)
		}
	}
	return "original"
}

// handleImage redirects to the artwork's TMDB URL. Apps often request art
// without a token (in an <img> tag), so a session is optional: without one
// only titles any viewer could see are served.
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess := s.optionalSession(r)
	var user *core.User
	if sess != nil {
		user = sess.User
	}
	key, ok := parseItemID(chi.URLParam(r, "id"))
	if !ok || isView(key) {
		http.NotFound(w, r)
		return
	}
	p := queryParams(r)
	width := firstPositive(p.int("maxwidth", 0), p.int("fillwidth", 0), p.int("width", 0))
	kind := strings.ToLower(chi.URLParam(r, "type"))

	switch key.Kind() {
	case core.KindCollection, core.KindSmart:
		s.collectionImage(w, r, user, key, kind, width)
		return
	}
	t, err := s.d.Catalog.Get(ctx, key)
	if err != nil || !s.titleAllowed(user, t) || (user != nil && !s.canSee(user, t.Type)) {
		http.NotFound(w, r)
		return
	}
	path, sizes := "", posterSizes
	switch kind {
	case "primary":
		path = t.PosterPath
		if t.Type == core.TypeEpisode {
			path, sizes = firstNonEmpty(t.StillPath, t.PosterPath), backdropSizes
		}
	case "backdrop", "thumb", "screenshot":
		path, sizes = t.BackdropPath, backdropSizes
		if path == "" && t.ShowKey != 0 {
			if show, err := s.d.Catalog.Get(ctx, t.ShowKey); err == nil {
				path = show.BackdropPath
			}
		}
	case "logo":
		path, sizes = t.LogoPath, backdropSizes
	}
	if path == "" && t.ShowKey != 0 && kind == "primary" {
		if show, err := s.d.Catalog.Get(ctx, t.ShowKey); err == nil {
			path = show.PosterPath
		}
	}
	if path == "" || s.d.ImageURL == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.Redirect(w, r, s.d.ImageURL(path, pickSize(width, sizes)), http.StatusFound)
}

func (s *Server) collectionImage(w http.ResponseWriter, r *http.Request, user *core.User, key core.RatingKey, kind string, width int) {
	ctx := r.Context()
	if user == nil {
		// Franchise collections are public TMDB data; viewer-made ones need a session.
		if key.Kind() != core.KindCollection {
			http.NotFound(w, r)
			return
		}
		owner, err := s.d.Auth.Owner(ctx)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		user = owner
	}
	if src, ok := s.d.Collections.(core.CollectionArtworkSource); ok && kind == "primary" {
		if data, ct, found, err := src.CollectionArtwork(ctx, user, key, "poster", width); err == nil && found {
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write(data)
			return
		}
	}
	c, err := s.d.Collections.Get(ctx, user, key)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path, sizes := c.PosterPath, posterSizes
	if kind == "backdrop" || kind == "thumb" {
		path, sizes = c.BackdropPath, backdropSizes
	}
	if path == "" || s.d.ImageURL == nil {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, s.d.ImageURL(path, pickSize(width, sizes)), http.StatusFound)
}

func firstPositive(ns ...int) int {
	for _, n := range ns {
		if n > 0 {
			return n
		}
	}
	return 0
}
