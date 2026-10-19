// Package markers supplies release-duration-specific skip ranges. Remote
// lookups are opt-in and cached; no submissions are ever sent.
package markers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	store.Register("markers", 1, `CREATE TABLE markers_cache(rating_key INTEGER NOT NULL,runtime_ms INTEGER NOT NULL,ranges TEXT NOT NULL,expires_at INTEGER NOT NULL,PRIMARY KEY(rating_key,runtime_ms));`)
}

type Marker struct {
	Type    string `json:"type"`
	StartMS int64  `json:"startTimeOffset"`
	EndMS   int64  `json:"endTimeOffset"`
	Final   bool   `json:"final"`
	Source  string `json:"source"`
}
type Options struct {
	Config func() (bool, string) // current TheIntroDB enabled state and optional end-user key

	Logger     *slog.Logger
	Detector   DetectOptions
	DB         *sql.DB
	Enabled    bool
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	TTL        time.Duration
	Now        func() time.Time
}
type Service struct {
	o       Options
	mu      sync.Mutex
	jobs    chan detectJob
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	closed  bool
	pending map[core.RatingKey]bool
}

func New(o Options) *Service {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.BaseURL == "" {
		o.BaseURL = "https://api.theintrodb.org/v4"
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 2 * time.Second}
	}
	if o.TTL <= 0 {
		o.TTL = 7 * 24 * time.Hour
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}
}
func valid(m Marker, d time.Duration) bool {
	return (m.Type == "intro" || m.Type == "credits" || m.Type == "recap") && m.StartMS >= 0 && m.EndMS > m.StartMS && m.EndMS <= d.Milliseconds()
}

// FromChapters uses explicit chapter labels only; generic Chapter 01 is not evidence.
func FromChapters(info core.MediaInfo) []Marker {
	var out []Marker
	for _, c := range info.Chapters {
		label := strings.ToLower(strings.TrimSpace(c.Title))
		kind := ""
		switch label {
		case "intro", "opening", "opening credits", "opening titles":
			kind = "intro"
		case "credits", "end credits", "closing credits", "ending":
			kind = "credits"
		case "recap", "previously on":
			kind = "recap"
		}
		m := Marker{Type: kind, StartMS: c.Start.Milliseconds(), EndMS: c.End.Milliseconds(), Final: kind == "credits" && c.End >= info.Duration-time.Second, Source: "chapter"}
		if valid(m, info.Duration) {
			out = append(out, m)
		}
	}
	return out
}
func (s *Service) cached(ctx context.Context, key core.RatingKey, d time.Duration) ([]Marker, bool, error) {
	if s.o.DB == nil {
		return nil, false, nil
	}
	var raw string
	var exp int64
	err := s.o.DB.QueryRowContext(ctx, `SELECT ranges,expires_at FROM markers_cache WHERE rating_key=? AND runtime_ms=?`, int64(key), d.Milliseconds()).Scan(&raw, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if exp <= s.o.Now().Unix() {
		return nil, false, nil
	}
	var out []Marker
	err = json.Unmarshal([]byte(raw), &out)
	return out, err == nil, err
}
func (s *Service) Put(ctx context.Context, key core.RatingKey, d time.Duration, ranges []Marker) error {
	if s.o.DB == nil {
		return errors.New("marker cache requires database")
	}
	for _, m := range ranges {
		if !valid(m, d) {
			return errors.New("invalid marker range")
		}
	}
	b, err := json.Marshal(ranges)
	if err != nil {
		return err
	}
	_, err = s.o.DB.ExecContext(ctx, `INSERT INTO markers_cache VALUES(?,?,?,?) ON CONFLICT(rating_key,runtime_ms) DO UPDATE SET ranges=excluded.ranges,expires_at=excluded.expires_at`, int64(key), d.Milliseconds(), string(b), s.o.Now().Add(s.o.TTL).Unix())
	return err
}
func (s *Service) Markers(ctx context.Context, key core.RatingKey, info core.MediaInfo) ([]Marker, error) {
	if info.Estimated {
		return nil, nil
	}
	out := FromChapters(info)
	cached, ok, err := s.cached(ctx, key, info.Duration)
	if err != nil {
		return out, err
	}
	if ok {
		return merge(out, cached), nil
	}
	enabled, apiKey := s.o.Enabled, s.o.APIKey
	if s.o.Config != nil {
		enabled, apiKey = s.o.Config()
	}
	if !enabled || info.Duration <= 0 || (key.Kind() != core.KindEpisode && key.Kind() != core.KindMovie) {
		return out, nil
	}
	remote, err := s.fetch(ctx, key, info.Duration, apiKey)
	if err != nil {
		return out, err
	}
	if s.o.DB != nil {
		if err = s.Put(ctx, key, info.Duration, remote); err != nil {
			return merge(out, remote), err
		}
	}
	return merge(out, remote), nil
}
func merge(a, b []Marker) []Marker {
	// Existing sources win for each kind, while retaining every range from
	// the chosen source (TheIntroDB may supply several intros/credit ranges).
	blocked := map[string]bool{}
	for _, m := range a {
		blocked[m.Type] = true
	}
	for _, m := range b {
		if !blocked[m.Type] {
			a = append(a, m)
		}
	}
	return a
}
func (s *Service) fetch(ctx context.Context, key core.RatingKey, d time.Duration, apiKey string) ([]Marker, error) {
	q := url.Values{"tmdb_id": {strconv.FormatInt(key.TMDB(), 10)}, "duration_ms": {strconv.FormatInt(d.Milliseconds(), 10)}, "merge_unknown": {"false"}}
	if key.Kind() == core.KindEpisode {
		q.Set("season", strconv.Itoa(key.Season()))
		q.Set("episode", strconv.Itoa(key.Episode()))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.o.BaseURL, "/")+"/media?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Ether/1")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := s.o.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, nil
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("theintrodb: HTTP %d", resp.StatusCode)
	}
	var body map[string]json.RawMessage
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, err
	}
	var out []Marker
	for _, kind := range []string{"intro", "credits", "recap"} {
		raw := body[kind]
		if len(raw) == 0 {
			continue
		}
		var ranges []struct {
			Start *int64 `json:"start_ms"`
			End   *int64 `json:"end_ms"`
		}
		if err = json.Unmarshal(raw, &ranges); err != nil {
			return nil, err
		}
		for _, r := range ranges {
			start, end := int64(0), d.Milliseconds()
			if r.Start != nil {
				start = *r.Start
			}
			if r.End != nil {
				end = *r.End
			}
			m := Marker{Type: kind, StartMS: start, EndMS: end, Final: kind == "credits" && end == d.Milliseconds(), Source: "theintrodb"}
			if valid(m, d) {
				out = append(out, m)
			}
		}
	}
	return out, nil
}
