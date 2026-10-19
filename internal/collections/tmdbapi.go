package collections

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

// TMDB is the slice of the TMDB client the collection engine needs: one
// generic, rate-limited, cached GET. *tmdb.Client satisfies it.
type TMDB interface {
	GetJSON(ctx context.Context, path string, q url.Values, out any) error
}

var _ TMDB = (*tmdb.Client)(nil)

// tmdbItem is the union of TMDB list/discover/credit item fields.
type tmdbItem struct {
	ID               int64    `json:"id"`
	MediaType        string   `json:"media_type"`
	Title            string   `json:"title"`
	Name             string   `json:"name"`
	OriginalTitle    string   `json:"original_title"`
	OriginalName     string   `json:"original_name"`
	Overview         string   `json:"overview"`
	PosterPath       string   `json:"poster_path"`
	BackdropPath     string   `json:"backdrop_path"`
	ReleaseDate      string   `json:"release_date"`
	FirstAirDate     string   `json:"first_air_date"`
	GenreIDs         []int    `json:"genre_ids"`
	OriginalLanguage string   `json:"original_language"`
	OriginCountry    []string `json:"origin_country"`
	Popularity       float64  `json:"popularity"`
	VoteAverage      float64  `json:"vote_average"`
	VoteCount        int      `json:"vote_count"`
	Adult            bool     `json:"adult"`
	// Person credits.
	Character  string `json:"character"`
	Job        string `json:"job"`
	Department string `json:"department"`
}

type tmdbPage struct {
	Page         int        `json:"page"`
	Results      []tmdbItem `json:"results"`
	TotalPages   int        `json:"total_pages"`
	TotalResults int        `json:"total_results"`
}

// tmdbAPI wraps TMDB with typed helpers, a genre map and conversion to
// catalogue titles.
type tmdbAPI struct {
	c   TMDB
	now func() time.Time

	mu     sync.Mutex
	genres map[string]map[int]string // "movie"/"tv" -> id -> name
}

func newTMDBAPI(c TMDB, now func() time.Time) *tmdbAPI {
	return &tmdbAPI{c: c, now: now, genres: map[string]map[int]string{}}
}

var errNoTMDB = errors.New("TMDB is not configured")

func (a *tmdbAPI) get(ctx context.Context, path string, q url.Values, out any) error {
	if a == nil || a.c == nil {
		return errNoTMDB
	}
	return a.c.GetJSON(ctx, path, q, out)
}

func (a *tmdbAPI) genreMap(ctx context.Context, kind string) map[int]string {
	a.mu.Lock()
	m, ok := a.genres[kind]
	a.mu.Unlock()
	if ok {
		return m
	}
	var out struct {
		Genres []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"genres"`
	}
	m = map[int]string{}
	if err := a.get(ctx, "/genre/"+kind+"/list", nil, &out); err != nil {
		return m // not cached: retried next time
	}
	for _, g := range out.Genres {
		m[g.ID] = g.Name
	}
	a.mu.Lock()
	a.genres[kind] = m
	a.mu.Unlock()
	return m
}

// genreID resolves a genre name (case-insensitive) for discover queries.
func (a *tmdbAPI) genreID(ctx context.Context, kind, name string) (int, bool) {
	for id, n := range a.genreMap(ctx, kind) {
		if strings.EqualFold(n, name) {
			return id, true
		}
	}
	// TMDB names a few TV genres differently.
	alias := map[string]string{"science fiction": "Sci-Fi & Fantasy", "fantasy": "Sci-Fi & Fantasy",
		"action": "Action & Adventure", "adventure": "Action & Adventure", "war": "War & Politics"}
	if kind == "tv" {
		if a2, ok := alias[strings.ToLower(name)]; ok {
			return a.genreID(ctx, kind, a2)
		}
	}
	return 0, false
}

// toTitle converts a TMDB item to a catalogue title. kind is "movie" or
// "tv" when the item carries no media_type.
func (a *tmdbAPI) toTitle(ctx context.Context, it tmdbItem, kind string) (core.Title, bool) {
	mt := it.MediaType
	if mt == "" {
		mt = kind
	}
	if it.ID <= 0 || it.Adult {
		return core.Title{}, false
	}
	var t core.Title
	switch mt {
	case "movie":
		rel := tmdb.ParseDate(it.ReleaseDate)
		t = core.Title{Key: core.MovieKey(it.ID), Type: core.TypeMovie, IDs: core.IDs{TMDB: it.ID},
			Title: firstNonEmpty(it.Title, it.Name), OriginalTitle: firstNonEmpty(it.OriginalTitle, it.OriginalName), Released: rel}
	case "tv":
		rel := tmdb.ParseDate(it.FirstAirDate)
		t = core.Title{Key: core.ShowKey(it.ID), Type: core.TypeShow, IDs: core.IDs{TMDB: it.ID},
			Title: firstNonEmpty(it.Name, it.Title), OriginalTitle: firstNonEmpty(it.OriginalName, it.OriginalTitle), Released: rel,
			Countries: it.OriginCountry}
	default:
		return core.Title{}, false
	}
	if t.Title == "" {
		return core.Title{}, false
	}
	t.SortTitle = sortKey(t.Title)
	t.Summary = it.Overview
	t.Language = it.OriginalLanguage
	t.Rating, t.VoteCount, t.Popularity = it.VoteAverage, it.VoteCount, it.Popularity
	t.PosterPath, t.BackdropPath = it.PosterPath, it.BackdropPath
	if !t.Released.IsZero() {
		t.Year = t.Released.Year()
	}
	if len(it.GenreIDs) > 0 {
		gm := a.genreMap(ctx, mt)
		t.Genres = []string{}
		for _, id := range it.GenreIDs {
			if n, ok := gm[id]; ok {
				t.Genres = append(t.Genres, n)
			}
		}
		if len(t.Genres) == 0 {
			t.Genres = nil // unknown names: keep what the mirror has
		}
	}
	return t, true
}

// discover pages /discover/{movie|tv} until want titles or maxPages.
func (a *tmdbAPI) discover(ctx context.Context, kind string, params url.Values, want, maxPages int) ([]core.Title, int, error) {
	if maxPages <= 0 {
		maxPages = 5
	}
	var out []core.Title
	total := 0
	for page := 1; page <= maxPages; page++ {
		q := url.Values{}
		for k, v := range params {
			q[k] = append([]string(nil), v...)
		}
		if q.Get("include_adult") == "" {
			q.Set("include_adult", "false")
		}
		q.Set("page", strconv.Itoa(page))
		var res tmdbPage
		if err := a.get(ctx, "/discover/"+kind, q, &res); err != nil {
			if page == 1 {
				return nil, 0, err
			}
			break
		}
		total = res.TotalResults
		for _, it := range res.Results {
			if t, ok := a.toTitle(ctx, it, kind); ok {
				out = append(out, t)
			}
		}
		if page >= res.TotalPages || (want > 0 && len(out) >= want) {
			break
		}
	}
	return out, total, nil
}

// personCredits lists a person's movies and shows, optionally by role.
func (a *tmdbAPI) personCredits(ctx context.Context, id int64, role string) ([]core.Title, string, error) {
	var res struct {
		Cast []tmdbItem `json:"cast"`
		Crew []tmdbItem `json:"crew"`
	}
	if err := a.get(ctx, fmt.Sprintf("/person/%d/combined_credits", id), nil, &res); err != nil {
		return nil, "", err
	}
	var items []tmdbItem
	switch role {
	case "cast":
		items = res.Cast
	case "director", "writer", "crew":
		for _, c := range res.Crew {
			job := strings.ToLower(c.Job)
			dept := strings.ToLower(c.Department)
			switch role {
			case "director":
				if job == "director" {
					items = append(items, c)
				}
			case "writer":
				if dept == "writing" {
					items = append(items, c)
				}
			default:
				items = append(items, c)
			}
		}
	default:
		items = append(items, res.Cast...)
		for _, c := range res.Crew {
			if strings.EqualFold(c.Job, "director") || strings.EqualFold(c.Department, "writing") {
				items = append(items, c)
			}
		}
	}
	seen := map[core.RatingKey]bool{}
	var out []core.Title
	for _, it := range items {
		// Talk shows and award ceremonies list nearly everyone; skip TV
		// self-appearances, which dominate actors' TV credits.
		if it.Job == "" && it.MediaType == "tv" {
			if ch := strings.ToLower(it.Character); ch == "" || strings.Contains(ch, "self") {
				continue
			}
		}
		t, ok := a.toTitle(ctx, it, "")
		if !ok || seen[t.Key] {
			continue
		}
		seen[t.Key] = true
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Popularity > out[j].Popularity })
	var name struct {
		Name string `json:"name"`
	}
	_ = a.get(ctx, fmt.Sprintf("/person/%d", id), nil, &name)
	return out, name.Name, nil
}

// entityName fetches the display name of a keyword, company, network,
// person or collection by id.
func (a *tmdbAPI) entityName(ctx context.Context, kind string, id int64) (string, error) {
	path := map[string]string{"keyword": "/keyword/%d", "company": "/company/%d", "network": "/network/%d",
		"person": "/person/%d", "collection": "/collection/%d"}[kind]
	if path == "" {
		return "", fmt.Errorf("unknown entity kind %q", kind)
	}
	var out struct {
		Name string `json:"name"`
	}
	if err := a.get(ctx, fmt.Sprintf(path, id), nil, &out); err != nil {
		return "", err
	}
	return out.Name, nil
}

// search finds TMDB entities by name.
func (a *tmdbAPI) search(ctx context.Context, kind, q string) ([]core.LookupResult, error) {
	path := map[string]string{"person": "/search/person", "keyword": "/search/keyword", "company": "/search/company",
		"collection": "/search/collection"}[kind]
	if path == "" {
		return nil, fmt.Errorf("unknown lookup kind %q", kind)
	}
	var res struct {
		Results []struct {
			ID                 int64      `json:"id"`
			Name               string     `json:"name"`
			ProfilePath        string     `json:"profile_path"`
			LogoPath           string     `json:"logo_path"`
			PosterPath         string     `json:"poster_path"`
			OriginCountry      string     `json:"origin_country"`
			KnownForDepartment string     `json:"known_for_department"`
			Popularity         float64    `json:"popularity"`
			KnownFor           []tmdbItem `json:"known_for"`
		} `json:"results"`
	}
	if err := a.get(ctx, path, url.Values{"query": {q}, "include_adult": {"false"}}, &res); err != nil {
		return nil, err
	}
	out := make([]core.LookupResult, 0, len(res.Results))
	for _, r := range res.Results {
		lr := core.LookupResult{ID: r.ID, Name: r.Name}
		switch kind {
		case "person":
			lr.ImagePath = r.ProfilePath
			var known []string
			for _, k := range r.KnownFor {
				if n := firstNonEmpty(k.Title, k.Name); n != "" && len(known) < 3 {
					known = append(known, n)
				}
			}
			lr.Subtitle = strings.TrimSpace(strings.Trim(r.KnownForDepartment+" · "+strings.Join(known, ", "), " ·"))
		case "company":
			lr.ImagePath = r.LogoPath
			lr.Subtitle = r.OriginCountry
		case "collection":
			lr.ImagePath = r.PosterPath
		}
		out = append(out, lr)
	}
	return out, nil
}

// watchProvider is a streaming service from /watch/providers.
type watchProvider struct {
	ID       int64  `json:"provider_id"`
	Name     string `json:"provider_name"`
	Logo     string `json:"logo_path"`
	Priority int    `json:"display_priority"`
}

// watchProviders lists the services available in a region (movie and TV
// lists merged).
func (a *tmdbAPI) watchProviders(ctx context.Context, region string) ([]watchProvider, error) {
	seen := map[int64]bool{}
	var out []watchProvider
	var firstErr error
	for _, kind := range []string{"movie", "tv"} {
		var res struct {
			Results []watchProvider `json:"results"`
		}
		q := url.Values{}
		if region != "" {
			q.Set("watch_region", region)
		}
		if err := a.get(ctx, "/watch/providers/"+kind, q, &res); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, p := range res.Results {
			if !seen[p.ID] {
				seen[p.ID] = true
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out, nil
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}
