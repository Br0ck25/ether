package debrid

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/newznab"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

// TorBox Search API (https://search-api.torbox.app): TorBox's own torrent
// search, authenticated with the account's API key. Results carry a
// "cached" flag answering for TorBox itself, so an owner with a TorBox
// account needs no torrent indexers, and every result's availability is
// known before ranking.
//
// Endpoint used (the same one TorBox's published Prowlarr definition uses):
//
//	GET /torrents/{source}:{id}?check_cache=true[&season=S&episode=E]
//	    -> {success, data: {torrents: [{hash, raw_title, size, magnet,
//	        cached, age, last_known_seeders, last_known_peers}], total_torrents}}
//
// {source} is imdb, tmdb or tvdb. Field details beyond those TorBox's
// definition reads are decoded leniently.
const (
	torBoxSearchBase    = "https://search-api.torbox.app"
	torBoxSearchName    = "TorBox"
	torBoxSearchTimeout = 10 * time.Second
)

type tbSearchTorrent struct {
	Hash     string          `json:"hash"`
	RawTitle string          `json:"raw_title"`
	Title    string          `json:"title"`
	Size     json.RawMessage `json:"size"`
	Magnet   string          `json:"magnet"`
	Cached   json.RawMessage `json:"cached"`
	Age      json.RawMessage `json:"age"`
	Seeders  json.RawMessage `json:"last_known_seeders"`
	Peers    json.RawMessage `json:"last_known_peers"`
}

type tbSearchData struct {
	Torrents []tbSearchTorrent `json:"torrents"`
}

// searchEntry is the TorBox account whose search is used: the first
// enabled TorBox provider (by priority) that has not turned search off.
func (s *Service) searchEntry() *entry {
	for _, e := range s.active() {
		if e.cfg.Kind == "torbox" && !e.cfg.DisableSearch && e.cfg.APIKey != "" {
			return e
		}
	}
	return nil
}

// SearchesTorBox reports whether TorBox search is in use.
func (s *Service) SearchesTorBox() bool { return s.searchEntry() != nil }

// searchTorBox queries TorBox's search for a title. ok is false when no
// TorBox account searches or the request has no usable id.
func (s *Service) searchTorBox(ctx context.Context, req core.SearchRequest) (rels []core.Release, cached map[string]bool, ok bool, err error) {
	e := s.searchEntry()
	if e == nil {
		return nil, nil, false, nil
	}
	ref := torBoxSearchRef(req)
	if ref == "" {
		return nil, nil, false, nil
	}
	q := url.Values{"check_cache": {"true"}}
	if req.Type == core.TypeEpisode {
		if req.Season > 0 {
			q.Set("season", strconv.Itoa(req.Season))
		}
		if req.Episode > 0 {
			q.Set("episode", strconv.Itoa(req.Episode))
		}
	}
	base := s.opts.TorBoxSearchURL
	if base == "" {
		base = torBoxSearchBase
	}
	u := strings.TrimRight(base, "/") + "/torrents/" + url.PathEscape(ref) + "?" + q.Encode()

	ctx, cancel := context.WithTimeout(ctx, torBoxSearchTimeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, true, err
	}
	hreq.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)
	hreq.Header.Set("Accept", "application/json")
	client := s.opts.ProviderOptions.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: torBoxSearchTimeout}
	}
	started := s.now()
	resp, err := client.Do(hreq)
	if err != nil {
		s.noteSearch(false, 0, s.now().Sub(started))
		return nil, nil, true, fmt.Errorf("torbox search: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		s.noteSearch(false, 0, s.now().Sub(started))
		return nil, nil, true, fmt.Errorf("torbox search: %w", err)
	}
	var env tbEnvelope
	if derr := json.Unmarshal(body, &env); derr != nil || resp.StatusCode/100 != 2 || !env.Success {
		s.noteSearch(false, 0, s.now().Sub(started))
		msg := env.Error
		if env.Detail != "" {
			msg = env.Detail
		}
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		// "Could not find metadata" is TorBox's empty answer.
		if strings.Contains(strings.ToLower(msg), "could not find") {
			return nil, map[string]bool{}, true, nil
		}
		return nil, nil, true, fmt.Errorf("torbox search: HTTP %d: %s", resp.StatusCode, msg)
	}
	var data tbSearchData
	if len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, &data); err != nil {
			s.noteSearch(false, 0, s.now().Sub(started))
			return nil, nil, true, fmt.Errorf("torbox search: decode: %w", err)
		}
	}
	now := s.now()
	cached = map[string]bool{}
	for _, t := range data.Torrents {
		r, isCached, keep := torBoxRelease(t, now)
		if !keep {
			continue
		}
		cached[r.InfoHash] = isCached
		if isCached {
			s.setKnown(ctx, e.cfg.ID, r.InfoHash, "", true)
		}
		rels = append(rels, r)
	}
	s.noteSearch(true, len(rels), s.now().Sub(started))
	return rels, cached, true, nil
}

// torBoxSearchRef is the "{source}:{id}" TorBox looks titles up by:
// IMDb first (what TorBox's own definition uses), then TVDB for shows and
// TMDB otherwise.
func torBoxSearchRef(req core.SearchRequest) string {
	switch {
	case req.IDs.IMDB != "":
		return "imdb:" + req.IDs.IMDB
	case req.Type == core.TypeEpisode && req.IDs.TVDB > 0:
		return "tvdb:" + strconv.FormatInt(int64(req.IDs.TVDB), 10)
	case req.IDs.TMDB > 0:
		return "tmdb:" + strconv.FormatInt(int64(req.IDs.TMDB), 10)
	}
	return ""
}

func torBoxRelease(t tbSearchTorrent, now time.Time) (core.Release, bool, bool) {
	name := strings.TrimSpace(t.RawTitle)
	if name == "" {
		name = strings.TrimSpace(t.Title)
	}
	hash := newznab.NormalizeInfoHash(t.Hash)
	if hash == "" {
		hash = newznab.InfoHashFromMagnet(t.Magnet)
	}
	if name == "" || hash == "" {
		return core.Release{}, false, false
	}
	magnet := t.Magnet
	if magnet == "" {
		magnet = "magnet:?xt=urn:btih:" + hash
	}
	sum := sha1.Sum([]byte(torBoxSearchName + "|" + hash))
	r := core.Release{
		ID:        hex.EncodeToString(sum[:]),
		GUID:      hash,
		Indexer:   torBoxSearchName,
		Name:      name,
		Size:      int64(jsonNumber(t.Size)),
		Protocol:  core.ProtocolTorrent,
		InfoHash:  hash,
		MagnetURL: magnet,
		Seeders:   int(jsonNumber(t.Seeders)),
		Peers:     int(jsonNumber(t.Peers)),
		Info:      releaseparse.Parse(name),
	}
	if age := torBoxAge(t.Age); age > 0 {
		r.PubDate = now.Add(-age)
	}
	return r, jsonBool(t.Cached), true
}

// jsonNumber reads a number or numeric string; 0 when absent or invalid.
func jsonNumber(raw json.RawMessage) float64 {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "null" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || f < 0 {
		return 0
	}
	return f
}

func jsonBool(raw json.RawMessage) bool {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(string(raw)), `"`)) {
	case "true", "1", "yes":
		return true
	}
	return false
}

// torBoxAge reads "age" as days (a number, as TorBox's definition treats
// it) or a short duration string such as "5d", "3 days", "12h" or "2w".
func torBoxAge(raw json.RawMessage) time.Duration {
	s := strings.ToLower(strings.Trim(strings.TrimSpace(string(raw)), `"`))
	if s == "" || s == "null" {
		return 0
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
		return time.Duration(f * float64(24*time.Hour))
	}
	i := strings.IndexFunc(s, func(r rune) bool { return !unicode.IsDigit(r) && r != '.' })
	if i <= 0 {
		return 0
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil || n <= 0 {
		return 0
	}
	unit := strings.TrimSpace(s[i:])
	switch {
	case strings.HasPrefix(unit, "min"):
		return time.Duration(n * float64(time.Minute))
	case strings.HasPrefix(unit, "h"):
		return time.Duration(n * float64(time.Hour))
	case strings.HasPrefix(unit, "d"):
		return time.Duration(n * float64(24*time.Hour))
	case strings.HasPrefix(unit, "w"):
		return time.Duration(n * float64(7*24*time.Hour))
	case strings.HasPrefix(unit, "mo") || unit == "m":
		return time.Duration(n * float64(30*24*time.Hour))
	case strings.HasPrefix(unit, "y"):
		return time.Duration(n * float64(365*24*time.Hour))
	}
	return 0
}

// SearchStats are TorBox search counters for the admin status.
type SearchStats struct {
	Enabled   bool    `json:"enabled"`
	Searches  int64   `json:"searches"`
	Failures  int64   `json:"failures"`
	Results   int64   `json:"results"`
	AverageMs float64 `json:"averageMs"`
}

func (s *Service) noteSearch(ok bool, results int, d time.Duration) {
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	s.search.Searches++
	if !ok {
		s.search.Failures++
		return
	}
	s.search.Results += int64(results)
	s.search.AverageMs = ewma(s.search.AverageMs, ms(d), s.search.Searches == 1)
}

// SearchStatus returns TorBox search counters.
func (s *Service) SearchStatus() SearchStats {
	s.searchMu.Lock()
	out := s.search
	s.searchMu.Unlock()
	out.Enabled = s.SearchesTorBox()
	return out
}
