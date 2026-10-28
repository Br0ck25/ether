package stremio

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/newznab"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

const (
	movieCategory = 2000
	tvCategory    = 5000
	// maxResults bounds the streams converted from one answer; addons
	// answer popular titles with a few hundred, best first.
	maxResults = 250
	// maxTrackers bounds the trackers carried into a magnet. Debrid
	// services resolve cached torrents by hash; trackers only help the
	// rare uncached add.
	maxTrackers = 8
	// manifestTTL is how long a fetched manifest's served types are reused.
	manifestTTL = time.Hour
)

// Backend is a newznab.Backend for one Stremio addon: the indexer Client
// keeps budgets, the result cache, rate-limit pauses and events around it.
type Backend struct {
	base   *url.URL
	client *http.Client

	mu         sync.Mutex
	manifest   *Manifest
	manifestAt time.Time
	now        func() time.Time
}

var _ newznab.Backend = (*Backend)(nil)

// Service builds and keeps one Backend per addon URL, so a backend's
// manifest survives between searches.
type Service struct {
	client *http.Client

	mu       sync.Mutex
	backends map[string]*Backend
}

// NewService returns a Service. client nil uses a default client with a
// 20 s timeout.
func NewService(client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &Service{client: client, backends: map[string]*Backend{}}
}

// Backend returns the backend for an addon indexer (newznab.Options.Backend).
func (s *Service) Backend(cfg config.Indexer) (newznab.Backend, error) {
	base, err := BaseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("indexer %q: %w", cfg.Name, err)
	}
	key := base.String()
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.backends[key]
	if b == nil {
		b = &Backend{base: base, client: s.client, now: time.Now}
		s.backends[key] = b
	}
	return b, nil
}

// Test checks that the addon at cfg.URL can be searched.
func (s *Service) Test(ctx context.Context, cfg config.Indexer) error {
	_, err := FetchManifest(ctx, s.client, cfg.URL)
	return err
}

// Search implements newznab.Backend. Movies are looked up by IMDb id and
// episodes by IMDb id, season and episode; a season-level request (Episode
// 0) asks for the season's first episode and keeps whole-season packs.
// Titles without an IMDb id, and show-level requests, have no answer.
func (b *Backend) Search(ctx context.Context, req core.SearchRequest, reserve func(context.Context, string) error) ([]core.Release, error) {
	imdb := imdbID(req.IDs.IMDB)
	if imdb == "" {
		return nil, nil
	}
	var kind, id string
	packs := false
	switch req.Type {
	case core.TypeMovie:
		kind, id = "movie", imdb
	case core.TypeEpisode, core.TypeSeason:
		if req.Season <= 0 {
			return nil, nil
		}
		ep := req.Episode
		if ep <= 0 {
			ep, packs = 1, true
		}
		kind, id = "series", fmt.Sprintf("%s:%d:%d", imdb, req.Season, ep)
	default:
		return nil, nil
	}
	if !b.serves(ctx, kind) {
		return nil, nil
	}
	if err := reserve(ctx, "hit"); err != nil {
		return nil, err
	}
	var answer struct {
		Streams []stream `json:"streams"`
	}
	if err := getJSON(ctx, b.client, b.base, "/stream/"+kind+"/"+url.PathEscape(id)+".json", maxStreams, &answer); err != nil {
		var he *httpError
		if errors.As(err, &he) && (he.status == http.StatusTooManyRequests || he.status == http.StatusServiceUnavailable && he.retry != "") {
			return nil, &newznab.RateLimitError{RetryAfter: he.retry, Err: err}
		}
		return nil, err
	}
	return convert(answer.Streams, req, packs), nil
}

// serves reports whether the addon answers kind, from a manifest fetched
// at most hourly. An unreadable manifest does not block the search: the
// stream request itself is the authority.
func (b *Backend) serves(ctx context.Context, kind string) bool {
	b.mu.Lock()
	m, at := b.manifest, b.manifestAt
	b.mu.Unlock()
	if m == nil || b.now().Sub(at) > manifestTTL {
		var fetched Manifest
		mctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := getJSON(mctx, b.client, b.base, "/manifest.json", maxManifest, &fetched)
		cancel()
		if err != nil {
			return true
		}
		b.mu.Lock()
		b.manifest, b.manifestAt, m = &fetched, b.now(), &fetched
		b.mu.Unlock()
	}
	return m.serves(kind)
}

// FetchNZB implements newznab.Backend: addon results are torrents.
func (b *Backend) FetchNZB(_ context.Context, r core.Release, _ func(context.Context, string) error) ([]byte, error) {
	return nil, fmt.Errorf("stremio: %q is a torrent release; it streams through a debrid provider", r.Name)
}

// ResolveTorrent implements newznab.Backend. Every addon result already
// carries its info hash and magnet.
func (b *Backend) ResolveTorrent(_ context.Context, r core.Release, _ func(context.Context, string) error) (core.Release, error) {
	if r.InfoHash == "" {
		return r, fmt.Errorf("stremio: %q has no info hash: %w", r.Name, core.ErrReleaseRejected)
	}
	return r, nil
}

// stream is the part of a Stremio stream object Ether reads.
type stream struct {
	Name          string   `json:"name"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	InfoHash      string   `json:"infoHash"`
	FileIdx       *int     `json:"fileIdx"`
	URL           string   `json:"url"`
	Sources       []string `json:"sources"`
	BehaviorHints struct {
		Filename  string `json:"filename"`
		VideoSize int64  `json:"videoSize"`
	} `json:"behaviorHints"`
}

// convert turns addon streams into torrent releases, best first as the
// addon ordered them. Streams without a BitTorrent info hash (direct
// links, Usenet) are skipped.
func convert(streams []stream, req core.SearchRequest, packs bool) []core.Release {
	out := make([]core.Release, 0, min(len(streams), maxResults))
	seen := make(map[string]bool, len(streams))
	for i := range streams {
		if len(out) == maxResults {
			break
		}
		s := &streams[i]
		hash := newznab.NormalizeInfoHash(s.InfoHash)
		if hash == "" {
			hash = hashFromURL(s.URL)
		}
		if hash == "" {
			continue
		}
		text := s.Description
		if text == "" {
			text = s.Title
		}
		torrent, file := names(text, s.BehaviorHints.Filename)
		var name string
		if packs {
			if !isSeasonPack(torrent, req.Season) {
				continue
			}
			name = torrent
		} else if name = pickName(req, file, torrent); name == "" {
			continue
		}
		guid := "btih:" + hash
		switch {
		case packs:
		case s.FileIdx != nil && *s.FileIdx >= 0:
			guid += "/" + strconv.Itoa(*s.FileIdx)
		case req.Type != core.TypeMovie:
			guid += fmt.Sprintf("/s%de%d", req.Season, req.Episode)
		}
		if seen[guid] {
			continue
		}
		seen[guid] = true
		size := s.BehaviorHints.VideoSize
		if size <= 0 {
			size = parseSize(text)
		}
		rel := core.Release{
			GUID:      guid,
			Name:      name,
			Size:      size,
			Seeders:   parseSeeders(text),
			Info:      describe(name, file, torrent),
			Protocol:  core.ProtocolTorrent,
			InfoHash:  hash,
			MagnetURL: newznab.MagnetFor(hash, firstNonEmpty(torrent, name), trackers(s.Sources)),
			Category:  movieCategory,
		}
		if req.Type != core.TypeMovie {
			rel.Category = tvCategory
		}
		rel.DebridCached, rel.DebridUncached = debridHints(s.Name)
		out = append(out, rel)
	}
	return out
}

// imdbID normalises an IMDb id to "tt" + digits, or "".
func imdbID(id string) string {
	id = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(id)), "tt")
	if id == "" || len(id) > 10 {
		return ""
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return "tt" + id
}

// hashFromURL finds a 40-hex info hash in a playback link: addons
// configured with a debrid key answer with links to their own resolver
// that carry the hash as a path segment or query value.
func hashFromURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if len(seg) == 40 {
			if h := newznab.NormalizeInfoHash(seg); h != "" {
				return h
			}
		}
	}
	for _, vs := range u.Query() {
		for _, v := range vs {
			if len(v) == 40 {
				if h := newznab.NormalizeInfoHash(v); h != "" {
					return h
				}
			}
		}
	}
	return ""
}

// videoExt reports whether a file name ends in a video container.
func videoExt(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".mkv", ".mp4", ".m4v", ".avi", ".ts", ".m2ts", ".mov", ".wmv", ".webm", ".mpg", ".mpeg":
		return true
	}
	return false
}

// names splits a stream's text into the torrent name and the video file's
// name (without directories or extension). Addons put the torrent name on
// the first line and, for a file inside a pack, the file's path on the
// next; statistics lines (seeders, size, source, languages) follow.
func names(text, filename string) (torrent, file string) {
	for _, line := range strings.Split(text, "\n") {
		if isStatsLine(line) {
			continue
		}
		if line = trimDecoration(line); line == "" {
			continue
		}
		if torrent == "" {
			torrent = line
			continue
		}
		if file == "" && videoExt(line) {
			file = line
		}
	}
	if filename != "" {
		file = filename
	}
	if file != "" {
		file = path.Base(strings.ReplaceAll(file, "\\", "/"))
		if videoExt(file) {
			file = strings.TrimSuffix(file, path.Ext(file))
		}
	}
	if torrent != "" && videoExt(torrent) {
		if file == "" {
			file = strings.TrimSuffix(path.Base(torrent), path.Ext(torrent))
		}
		torrent = strings.TrimSuffix(torrent, path.Ext(torrent))
	}
	return torrent, file
}

// trimDecoration removes the emoji and symbols addons lead lines with
// ("📂 Name", "📄 file.mkv").
func trimDecoration(line string) string {
	line = strings.TrimSpace(line)
	return strings.TrimSpace(strings.TrimLeftFunc(line, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '[' && r != '('
	}))
}

// isStatsLine reports an addon's statistics or flags line.
func isStatsLine(line string) bool {
	for _, marker := range []string{"👤", "💾", "⚙️", "⚙", "🔎", "🌐", "🌎", "🔗", "📦", "🇬🇧"} {
		if strings.Contains(line, marker) {
			return true
		}
	}
	// A line of only flags or separators.
	return strings.IndexFunc(line, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0
}

// pickName chooses the name that describes the video best: the file's own
// name when it identifies the title (and episode), else the torrent's. A
// file in a pack names its episode and its own quality; a foreign-language
// torrent name often names neither.
func pickName(req core.SearchRequest, file, torrent string) string {
	score := func(name string) int {
		if name == "" {
			return -1
		}
		info := releaseparse.Parse(name)
		n := 0
		if releaseparse.TitleMatches(info, req.Title) || (req.OriginalTitle != "" && releaseparse.TitleMatches(info, req.OriginalTitle)) {
			n += 4
		}
		if req.Type != core.TypeMovie && req.Episode > 0 && releaseparse.EpisodeMatches(info, req.Season, req.Episode) {
			n += 2
		}
		if info.Height > 0 {
			n++
		}
		return n
	}
	fs, ts := score(file), score(torrent)
	switch {
	case fs < 0 && ts < 0:
		return ""
	case fs >= ts:
		return file
	default:
		return torrent
	}
}

// describe parses name and fills the quality facts it lacks from the other
// of the stream's names: a pack's episode file often carries only the
// episode, its torrent name the resolution, source, codecs and group.
func describe(name, file, torrent string) core.ReleaseInfo {
	info := releaseparse.Parse(name)
	other := torrent
	if name == torrent {
		other = file
	}
	if other == "" || other == name {
		return info
	}
	o := releaseparse.Parse(other)
	if info.Height == 0 {
		info.Height = o.Height
	}
	if info.Source == core.SourceUnknown {
		info.Source = o.Source
	}
	if info.VideoCodec == "" {
		info.VideoCodec = o.VideoCodec
	}
	if info.AudioCodec == "" {
		info.AudioCodec, info.Channels = o.AudioCodec, o.Channels
	}
	if info.Channels == "" {
		info.Channels = o.Channels
	}
	if info.Year == 0 {
		info.Year = o.Year
	}
	if info.Group == "" {
		info.Group = o.Group
	}
	if info.Edition == "" {
		info.Edition = o.Edition
	}
	if len(info.Languages) == 0 {
		info.Languages = o.Languages
	}
	info.HDR10 = info.HDR10 || o.HDR10
	info.DolbyVision = info.DolbyVision || o.DolbyVision
	info.Atmos = info.Atmos || o.Atmos
	info.Proper = info.Proper || o.Proper
	info.Repack = info.Repack || o.Repack
	return info
}

// isSeasonPack reports a torrent name covering the whole of season.
func isSeasonPack(name string, season int) bool {
	if name == "" {
		return false
	}
	info := releaseparse.Parse(name)
	if len(info.Episodes) > 0 {
		return false
	}
	if info.FullSeason {
		return true
	}
	for _, s := range info.Seasons {
		if s == season {
			return true
		}
	}
	return false
}

var (
	seedersRe = regexp.MustCompile(`👤\s*([\d,.]+)`)
	sizeRe    = regexp.MustCompile(`(?i)💾\s*([\d.,]+)\s*([KMGT]?)i?B\b`)
)

func parseSeeders(text string) int {
	m := seedersRe.FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.NewReplacer(",", "", ".", "").Replace(m[1]))
	return n
}

func parseSize(text string) int64 {
	m := sizeRe.FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
	if err != nil || v <= 0 {
		return 0
	}
	shift := map[string]uint{"": 0, "K": 10, "M": 20, "G": 30, "T": 40}[strings.ToUpper(m[2])]
	return int64(v * float64(uint64(1)<<shift))
}

// trackers returns the announce URLs of a stream's "tracker:" sources.
func trackers(sources []string) []string {
	var out []string
	for _, s := range sources {
		if tr, ok := strings.CutPrefix(s, "tracker:"); ok && tr != "" {
			out = append(out, tr)
			if len(out) == maxTrackers {
				break
			}
		}
	}
	return out
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// hintRe matches a debrid service tag with its availability marker:
// "[RD+]" and "[RD download]", "[TB⚡]" and "[TB⬇️]", "RD ⏳".
var hintRe = regexp.MustCompile(`(?i)\b(RD|TB|AD|PM)\s*(\+|⚡|download|⬇|⏳)`)

var hintKinds = map[string]string{
	"RD": config.DebridRealDebrid,
	"TB": config.DebridTorBox,
	"AD": config.DebridAllDebrid,
	"PM": config.DebridPremiumize,
}

// debridHints reads the cached markers an addon configured with a debrid
// key puts in a stream's name.
func debridHints(text string) (cached, uncached []string) {
	for _, m := range hintRe.FindAllStringSubmatch(text, -1) {
		kind := hintKinds[strings.ToUpper(m[1])]
		switch m[2] {
		case "+", "⚡":
			if !contains(cached, kind) {
				cached = append(cached, kind)
			}
		default:
			if !contains(uncached, kind) {
				uncached = append(uncached, kind)
			}
		}
	}
	return cached, uncached
}
