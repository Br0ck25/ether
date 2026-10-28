package episodeorder

import (
	"encoding/json"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/tmdb"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strconv"
)

// Router is mounted behind owner authentication. Viewer zero is the owner
// default; a positive viewer is an explicit override, including aired order.
func (s *Service) Router() http.Handler {
	r := chi.NewRouter()
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	params := func(w http.ResponseWriter, r *http.Request) (int64, core.RatingKey, bool) {
		u, e := strconv.ParseInt(chi.URLParam(r, "user"), 10, 64)
		k, e2 := core.ParseRatingKey(chi.URLParam(r, "show"))
		if e != nil || e2 != nil || u < 0 || k.Kind() != core.KindShow {
			http.Error(w, "Invalid show or viewer", 400)
			return 0, 0, false
		}
		return u, k, true
	}
	r.Get("/shows/{show}/groups", func(w http.ResponseWriter, r *http.Request) {
		k, e := core.ParseRatingKey(chi.URLParam(r, "show"))
		if e != nil || k.Kind() != core.KindShow {
			http.Error(w, "Invalid show", 400)
			return
		}
		groups, e := s.source.EpisodeGroups(r.Context(), k.TMDB())
		if e != nil {
			http.Error(w, "Episode orders unavailable", 502)
			return
		}
		if groups == nil {
			groups = []tmdb.EpisodeGroupSummary{}
		}
		reply(w, groups)
	})
	r.Get("/shows/{show}/users/{user}", func(w http.ResponseWriter, r *http.Request) {
		u, k, ok := params(w, r)
		if !ok {
			return
		}
		p, e := s.Preference(r.Context(), u, k)
		if e != nil {
			http.Error(w, "Preference unavailable", 500)
			return
		}
		reply(w, p)
	})
	r.Put("/shows/{show}/users/{user}", func(w http.ResponseWriter, r *http.Request) {
		u, k, ok := params(w, r)
		if !ok {
			return
		}
		var p Preference
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&p) != nil {
			http.Error(w, "Invalid preference", 400)
			return
		}
		if e := s.Save(r.Context(), u, k, p); e != nil {
			http.Error(w, "Could not save episode order; check the selected group and specials policy", 400)
			return
		}
		reply(w, p)
	})
	r.Delete("/shows/{show}/users/{user}", func(w http.ResponseWriter, r *http.Request) {
		u, k, ok := params(w, r)
		if !ok {
			return
		}
		if s.Reset(r.Context(), u, k) != nil {
			http.Error(w, "Could not reset preference", 500)
			return
		}
		p, e := s.Preference(r.Context(), u, k)
		if e != nil {
			http.Error(w, "Preference unavailable", 500)
			return
		}
		reply(w, p)
	})
	r.Get("/shows/{show}/users/{user}/preview", func(w http.ResponseWriter, r *http.Request) {
		u, k, ok := params(w, r)
		if !ok {
			return
		}
		items, e := s.Grandchildren(r.Context(), &core.User{ID: u}, k)
		if e != nil {
			http.Error(w, "Episode preview unavailable", 502)
			return
		}
		type ep struct {
			Key              string `json:"key"`
			Title            string `json:"title"`
			CanonicalSeason  int    `json:"canonicalSeason"`
			CanonicalEpisode int    `json:"canonicalEpisode"`
			Group            string `json:"group"`
			Index            int    `json:"index"`
		}
		out := []ep{}
		for _, t := range items {
			_, n, name, yes := s.Position(r.Context(), &core.User{ID: u}, t.Key)
			if !yes {
				n = t.Episode
				name = "Season " + strconv.Itoa(t.Season)
			}
			out = append(out, ep{t.Key.String(), t.Title, t.Season, t.Episode, name, n})
		}
		reply(w, out)
	})
	return r
}
