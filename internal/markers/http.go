package markers

import (
	"context"
	"encoding/json"
	"github.com/braedonsaunders/ether/internal/core"
	"net/http"
	"strconv"
	"strings"
)

type Coverage struct {
	ShowID          int64 `json:"showId"`
	CachedEpisodes  int   `json:"cachedEpisodes"`
	IntroEpisodes   int   `json:"introEpisodes"`
	CreditsEpisodes int   `json:"creditsEpisodes"`
	RecapEpisodes   int   `json:"recapEpisodes"`
}

// Coverage counts observed cached episodes; it does not imply coverage of the
// show's entire catalogue or that an official client displayed skip controls.
func (s *Service) Coverage(ctx context.Context, show int64) (Coverage, error) {
	out := Coverage{ShowID: show}
	if s.o.DB == nil {
		return out, nil
	}
	lo := int64(core.EpisodeKey(show, 0, 0))
	hi := int64(core.EpisodeKey(show+1, 0, 0))
	rows, err := s.o.DB.QueryContext(ctx, `SELECT rating_key,ranges FROM markers_cache WHERE rating_key>=? AND rating_key<? AND expires_at>?`, lo, hi, s.o.Now().Unix())
	if err != nil {
		return out, err
	}
	defer rows.Close()
	keys := map[int64]map[string]bool{}
	for rows.Next() {
		var key int64
		var raw string
		if err = rows.Scan(&key, &raw); err != nil {
			return out, err
		}
		var ms []Marker
		if err = json.Unmarshal([]byte(raw), &ms); err != nil {
			return out, err
		}
		if keys[key] == nil {
			keys[key] = map[string]bool{}
		}
		for _, m := range ms {
			keys[key][m.Type] = true
		}
	}
	for _, k := range keys {
		out.CachedEpisodes++
		if k["intro"] {
			out.IntroEpisodes++
		}
		if k["credits"] {
			out.CreditsEpisodes++
		}
		if k["recap"] {
			out.RecapEpisodes++
		}
	}
	return out, rows.Err()
}

// Handler serves GET /shows/{TMDBID}, behind the owner's admin authentication.
func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(405)
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 2 || parts[0] != "shows" {
			http.NotFound(w, r)
			return
		}
		show, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || show <= 0 {
			http.Error(w, "invalid show", 400)
			return
		}
		coverage, err := s.Coverage(r.Context(), show)
		if err != nil {
			http.Error(w, "storage unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(coverage)
	})
}
