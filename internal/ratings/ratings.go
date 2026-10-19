// Package ratings caches licensed, owner-keyed rating data. Values preserve
// their source scale; Plex's main rating fields alone use the 0–10 scale.
package ratings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const Redacted = "[configured]"

func init() {
	store.Register("ratings", 1, `CREATE TABLE ratings_settings(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL);CREATE TABLE ratings_cache(item_key INTEGER NOT NULL,provider TEXT NOT NULL,value TEXT NOT NULL,updated INTEGER NOT NULL,PRIMARY KEY(item_key,provider));`)
}

type Settings struct {
	Enabled    bool   `json:"enabled"`
	Provider   string `json:"provider"`
	APIKey     string `json:"apiKey"`
	Critic     string `json:"critic"`
	Audience   string `json:"audience"`
	CacheHours int    `json:"cacheHours"`
}
type Badge struct {
	Source string  `json:"source"`
	Value  float64 `json:"value"`
	Scale  float64 `json:"scale"`
	Votes  int64   `json:"votes,omitempty"`
}

func (b Badge) PlexValue() float64 {
	if b.Scale <= 0 {
		return 0
	}
	return math.Round(b.Value/b.Scale*100) / 10
}

// PlexImage returns only icons understood by official Plex clients. Other
// providers remain explicitly named poster badges; they never impersonate IMDb.
func (b Badge) PlexImage() string {
	switch b.Source {
	case "imdb":
		return "imdb://image.rating"
	case "tomatoes":
		if b.Value/b.Scale >= .6 {
			return "rottentomatoes://image.rating.ripe"
		}
		return "rottentomatoes://image.rating.rotten"
	case "popcorn":
		if b.Value/b.Scale >= .6 {
			return "rottentomatoes://image.rating.upright"
		}
		return "rottentomatoes://image.rating.spilled"
	}
	return ""
}

type Options struct {
	Client         *http.Client
	OMDbBaseURL    string
	MDBListBaseURL string
	Now            func() time.Time
}
type Service struct {
	db      *sql.DB
	o       Options
	mu      sync.Mutex
	queue   chan core.Title
	qmu     sync.Mutex
	queued  map[core.RatingKey]bool
	retryAt map[core.RatingKey]time.Time
}

func New(db *sql.DB, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if o.OMDbBaseURL == "" {
		o.OMDbBaseURL = "https://www.omdbapi.com/"
	}
	if o.MDBListBaseURL == "" {
		o.MDBListBaseURL = "https://api.mdblist.com"
	}
	return &Service{db: db, o: o, queue: make(chan core.Title, 128), queued: map[core.RatingKey]bool{}, retryAt: map[core.RatingKey]time.Time{}}
}
func (s *Service) settings(ctx context.Context) (Settings, error) {
	v := Settings{Provider: "omdb", Critic: "tomatoes", Audience: "imdb", CacheHours: 168}
	var raw string
	e := s.db.QueryRowContext(ctx, `SELECT value FROM ratings_settings WHERE id=1`).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return v, nil
	}
	if e != nil {
		return v, e
	}
	e = json.Unmarshal([]byte(raw), &v)
	return v, e
}
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	v, e := s.settings(ctx)
	if v.APIKey != "" {
		v.APIKey = Redacted
	}
	return v, e
}
func (s *Service) SaveSettings(ctx context.Context, v Settings) error {
	if v.Provider != "omdb" && v.Provider != "mdblist" {
		return errors.New("provider must be omdb or mdblist")
	}
	if v.CacheHours < 1 || v.CacheHours > 2160 {
		return errors.New("cache interval must be 1–2160 hours")
	}
	if v.APIKey == Redacted {
		old, e := s.settings(ctx)
		if e != nil {
			return e
		}
		v.APIKey = old.APIKey
	}
	for _, src := range []string{v.Critic, v.Audience} {
		if src != "" && src != "imdb" && src != "tomatoes" && src != "popcorn" {
			return errors.New("Plex rating slots support imdb, tomatoes or popcorn; other ratings are poster badges")
		}
	}
	if v.Enabled && strings.TrimSpace(v.APIKey) == "" {
		return errors.New("an owner API key is required")
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO ratings_settings(id,value) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value`, string(raw))
	return e
}

// Cached never performs network I/O; safe for metadata and hub rendering.
func (s *Service) Cached(ctx context.Context, t *core.Title) ([]Badge, error) {
	cfg, e := s.settings(ctx)
	if e != nil || !cfg.Enabled || t == nil {
		return nil, e
	}
	var raw string
	var at int64
	e = s.db.QueryRowContext(ctx, `SELECT value,updated FROM ratings_cache WHERE item_key=? AND provider=?`, t.Key, cfg.Provider).Scan(&raw, &at)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if s.o.Now().Sub(time.Unix(at, 0)) > time.Duration(cfg.CacheHours)*time.Hour {
		return nil, nil
	}
	var out []Badge
	e = json.Unmarshal([]byte(raw), &out)
	return out, e
}
func (s *Service) Refresh(ctx context.Context, t *core.Title) ([]Badge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cached, e := s.Cached(ctx, t); e != nil || cached != nil {
		return cached, e
	}
	cfg, e := s.settings(ctx)
	if e != nil || !cfg.Enabled || t == nil {
		return nil, e
	}
	base := s.o.OMDbBaseURL
	if cfg.Provider == "mdblist" {
		if t.IDs.TMDB <= 0 {
			return nil, nil
		}
		typ := "movie"
		if t.Type == core.TypeShow {
			typ = "show"
		} else if t.Type != core.TypeMovie {
			return nil, nil
		}
		base = strings.TrimRight(s.o.MDBListBaseURL, "/") + "/tmdb/" + typ + "/" + strconv.FormatInt(t.IDs.TMDB, 10) + "/"
	} else if t.IDs.IMDB == "" {
		return nil, nil
	}
	u, e := url.Parse(base)
	if e != nil {
		return nil, errors.New("rating provider URL invalid")
	}
	q := u.Query()
	q.Set("apikey", cfg.APIKey)
	if cfg.Provider == "omdb" {
		q.Set("i", t.IDs.IMDB)
	}
	u.RawQuery = q.Encode()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return nil, errors.New("rating request invalid")
	}
	resp, e := s.o.Client.Do(req)
	if e != nil {
		return nil, errors.New("rating provider unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("rating provider: HTTP %d", resp.StatusCode)
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if e != nil {
		return nil, errors.New("rating response unreadable")
	}
	var badges []Badge
	if cfg.Provider == "omdb" {
		badges, e = parseOMDb(data)
	} else {
		badges, e = parseMDBList(data)
	}
	if e != nil {
		return nil, e
	}
	if badges == nil {
		badges = []Badge{}
	}
	raw, e := json.Marshal(badges)
	if e != nil {
		return nil, e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO ratings_cache(item_key,provider,value,updated) VALUES(?,?,?,?) ON CONFLICT(item_key,provider) DO UPDATE SET value=excluded.value,updated=excluded.updated`, t.Key, cfg.Provider, string(raw), s.o.Now().Unix())
	return badges, e
}
func parseOMDb(data []byte) ([]Badge, error) {
	var response struct {
		Response string
		Ratings  []struct {
			Source string
			Value  string
		}
	}
	if json.Unmarshal(data, &response) != nil {
		return nil, errors.New("invalid OMDb response")
	}
	if response.Response == "False" {
		return nil, errors.New("OMDb lookup failed")
	}
	var out []Badge
	for _, r := range response.Ratings {
		source := ""
		scale := 100.
		switch r.Source {
		case "Internet Movie Database":
			source = "imdb"
			scale = 10
		case "Rotten Tomatoes":
			source = "tomatoes"
		case "Metacritic":
			source = "metacritic"
		default:
			continue
		}
		str := strings.TrimSuffix(strings.SplitN(r.Value, "/", 2)[0], "%")
		value, e := strconv.ParseFloat(str, 64)
		if e == nil && valid(value, scale) {
			out = append(out, Badge{Source: source, Value: value, Scale: scale})
		}
	}
	return out, nil
}
func valid(v, scale float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= scale }
func parseMDBList(data []byte) ([]Badge, error) {
	var response struct {
		Ratings []struct {
			Source string          `json:"source"`
			Value  json.RawMessage `json:"value"`
			Votes  int64           `json:"votes"`
		}
	}
	if json.Unmarshal(data, &response) != nil {
		return nil, errors.New("invalid MDBList response")
	}
	var out []Badge
	for _, r := range response.Ratings {
		scale := 100.
		switch r.Source {
		case "imdb":
			scale = 10
		case "letterboxd":
			scale = 5
		case "tomatoes", "popcorn", "metacritic":
		default:
			continue
		}
		value, e := strconv.ParseFloat(strings.Trim(string(r.Value), "\""), 64)
		if e == nil && valid(value, scale) {
			out = append(out, Badge{Source: r.Source, Value: value, Scale: scale, Votes: r.Votes})
		}
	}
	return out, nil
}

// Queue is bounded, deduplicated and nonblocking; Run owns the network worker.
func (s *Service) Queue(t core.Title) {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	if s.queued[t.Key] || s.o.Now().Before(s.retryAt[t.Key]) {
		return
	}
	select {
	case s.queue <- t:
		s.queued[t.Key] = true
	default:
	}
}
func (s *Service) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-s.queue:
			job, cancel := context.WithTimeout(ctx, 20*time.Second)
			_, err := s.Refresh(job, &t)
			cancel()
			s.qmu.Lock()
			delete(s.queued, t.Key)
			if err != nil {
				if len(s.retryAt) > 2048 {
					s.retryAt = map[core.RatingKey]time.Time{}
				}
				s.retryAt[t.Key] = s.o.Now().Add(15 * time.Minute)
			} else {
				delete(s.retryAt, t.Key)
			}
			s.qmu.Unlock()
		}
	}
}
