// Package extras lists and resolves trailers and other clips attached to
// catalogue titles (Plex "Extras" and Cinema Trailers).
//
// TMDB only gives YouTube/Vimeo keys, which official Plex apps cannot
// play, so an extra is only advertised when a playable source exists: an
// owner-configured yt-dlp-compatible resolver command that turns a video
// page into a direct media URL, or a trailer file in the owner's local
// libraries. Ether ships no resolver binary and no content.
package extras

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

// Source kinds of an Extra.
const (
	SourceCommand = "command" // TMDB video resolved by the owner's command
	SourceLocal   = "local"   // trailer file in a local library
)

// MaxExtras caps how many extras one title advertises.
const MaxExtras = 10

// Extra is one playable clip attached to a title.
type Extra struct {
	Key         core.RatingKey // KindExtra
	ParentKey   core.RatingKey
	ParentTitle string
	Title       string
	Subtype     string // Plex subtype: trailer, teaser, clip, featurette, behindTheScenes
	ExtraType   int    // Plex numeric extra type (1 = trailer)
	Site        string // YouTube, Vimeo (command source)
	SiteKey     string // site-specific video id
	Language    string // ISO 639-1
	Official    bool
	PublishedAt time.Time
	Size        int // vertical resolution when known
	Duration    time.Duration
	Source      string // SourceCommand or SourceLocal

	identity  string // registry identity (collision detection)
	localPath string // SourceLocal only
}

// IsTrailer reports whether the extra can serve as a Cinema Trailer.
func (e Extra) IsTrailer() bool { return e.Subtype == "trailer" || e.Subtype == "teaser" }

// Resolved is a playable location for an extra.
type Resolved struct {
	URL       string // direct media URL (command source)
	LocalPath string // local file path (local source)
	Headers   map[string]string
	Expires   time.Time // zero for local files
	Container string
}

// Input returns the ffmpeg input (URL or local path).
func (r Resolved) Input() string {
	if r.LocalPath != "" {
		return r.LocalPath
	}
	return r.URL
}

// VideoProvider is the TMDB videos surface the service needs
// (*tmdb.Client satisfies it).
type VideoProvider interface {
	MovieVideos(ctx context.Context, id int64, lang string) ([]tmdb.Video, error)
	TVVideos(ctx context.Context, id int64, lang string) ([]tmdb.Video, error)
	SeasonVideos(ctx context.Context, tvID int64, season int, lang string) ([]tmdb.Video, error)
	EpisodeVideos(ctx context.Context, tvID int64, season, episode int, lang string) ([]tmdb.Video, error)
}

// LocalFile is one indexed local media file.
type LocalFile struct {
	Path      string
	Key       core.RatingKey // matched title, 0 when unmatched
	Size      int64
	Duration  time.Duration
	Container string
}

// LocalLister lists local library files (adapted from localmedia).
type LocalLister interface {
	LocalFiles(ctx context.Context) ([]LocalFile, error)
}

// CommandRunner runs the resolver command and returns its stdout. Tests
// substitute a fake; the default is exec without a shell.
type CommandRunner func(ctx context.Context, name string, args []string) ([]byte, error)

// Options are the extras service dependencies.
type Options struct {
	Videos   VideoProvider
	Local    LocalLister
	Config   func() config.Extras
	Logger   *slog.Logger
	Now      func() time.Time
	CacheTTL time.Duration // list cache; default 1h
	Runner   CommandRunner
	LookPath func(string) (string, error)
}

type listEntry struct {
	extras  []Extra
	expires time.Time
}

// Service lists, registers and resolves extras. Safe for concurrent use.
type Service struct {
	videos VideoProvider
	local  LocalLister
	cfgFn  func() config.Extras
	log    *slog.Logger
	now    func() time.Time
	ttl    time.Duration
	runner CommandRunner
	look   func(string) (string, error)

	mu       sync.Mutex
	registry map[core.RatingKey]Extra
	lists    map[string]listEntry
	resolved map[string]Resolved
}

const (
	maxListEntries     = 2048
	maxRegistryEntries = 8192
	maxResolvedEntries = 2048
)

// New builds the extras service.
func New(o Options) *Service {
	if o.Config == nil {
		o.Config = func() config.Extras { return config.Extras{} }
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.CacheTTL <= 0 {
		o.CacheTTL = time.Hour
	}
	if o.Runner == nil {
		o.Runner = execRunner
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	return &Service{
		videos: o.Videos, local: o.Local, cfgFn: o.Config,
		log: o.Logger.With("component", "extras"), now: o.Now,
		ttl: o.CacheTTL, runner: o.Runner, look: o.LookPath,
		registry: map[core.RatingKey]Extra{}, lists: map[string]listEntry{},
		resolved: map[string]Resolved{},
	}
}

func (s *Service) cfg() config.Extras { return s.cfgFn() }

// CommandAvailable reports whether the resolver command source can run.
func (s *Service) CommandAvailable() bool {
	c := s.cfg()
	if strings.TrimSpace(c.ResolverCommand) == "" {
		return false
	}
	_, err := s.look(c.ResolverCommand)
	return err == nil
}

// LocalAvailable reports whether local trailer files can be a source.
func (s *Service) LocalAvailable() bool {
	return s.cfg().LocalTrailers && s.local != nil
}

// Enabled reports whether any playable extras can be advertised.
func (s *Service) Enabled() bool {
	return s.cfg().Enabled && (s.CommandAvailable() || s.LocalAvailable())
}

// DisabledReason explains why extras are off, for a startup log line.
// Empty when enabled.
func (s *Service) DisabledReason() string {
	c := s.cfg()
	switch {
	case !c.Enabled:
		return "extras disabled in config (extras.enabled is false)"
	case s.Enabled():
		return ""
	case strings.TrimSpace(c.ResolverCommand) == "" && !s.LocalAvailable():
		return "extras enabled but no playable source: set extras.resolverCommand to a yt-dlp-compatible tool or enable local trailers"
	default:
		return fmt.Sprintf("extras resolver command %q not found or not executable", c.ResolverCommand)
	}
}

// PrewarmBytes is the configured trailer pre-buffer size (32–64 MiB).
func (s *Service) PrewarmBytes() int64 { return s.cfg().PrewarmBytesOr() }

// ISO639 reduces a BCP-47 tag to its ISO 639-1 language.
func ISO639(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if i := strings.IndexAny(lang, "-_"); i > 0 {
		lang = lang[:i]
	}
	return lang
}

// List returns the playable extras of a title, best first: official
// trailers in the viewer's language ahead of everything else. It returns
// nothing when extras are disabled or no source can play them.
func (s *Service) List(ctx context.Context, parent *core.Title, lang string) ([]Extra, error) {
	if parent == nil || !s.cfg().Enabled {
		return nil, nil
	}
	iso := ISO639(lang)
	cacheKey := parent.Key.String() + "|" + iso
	s.mu.Lock()
	if e, ok := s.lists[cacheKey]; ok && s.now().Before(e.expires) {
		out := append([]Extra(nil), e.extras...)
		for i := range out {
			out[i] = s.registerLocked(out[i])
		}
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()

	var out []Extra
	if s.CommandAvailable() && s.videos != nil {
		vs, err := s.fetchVideos(ctx, parent, lang)
		if err != nil {
			s.log.Debug("tmdb videos", "key", parent.Key, "err", err)
		}
		out = append(out, videosToExtras(parent, vs)...)
	}
	if s.LocalAvailable() {
		files, err := s.local.LocalFiles(ctx)
		if err != nil {
			s.log.Debug("local trailers", "key", parent.Key, "err", err)
		} else {
			out = append(out, localExtras(parent, files)...)
		}
	}
	out = dedupeExtras(out)
	sortExtras(out, iso)
	if len(out) > MaxExtras {
		out = out[:MaxExtras]
	}
	s.mu.Lock()
	s.pruneLocked()
	for i := range out {
		out[i] = s.registerLocked(out[i])
	}
	if len(s.lists) >= maxListEntries {
		for k := range s.lists {
			delete(s.lists, k)
			break
		}
	}
	s.lists[cacheKey] = listEntry{extras: append([]Extra(nil), out...), expires: s.now().Add(s.ttl)}
	s.mu.Unlock()
	return out, nil
}

func (s *Service) fetchVideos(ctx context.Context, parent *core.Title, lang string) ([]tmdb.Video, error) {
	tmdbID := parent.IDs.TMDB
	if tmdbID == 0 {
		tmdbID = parent.Key.TMDB()
	}
	if parent.Type == core.TypeEpisode || parent.Type == core.TypeSeason {
		if parent.ShowIDs.TMDB > 0 {
			tmdbID = parent.ShowIDs.TMDB
		} else {
			tmdbID = parent.Key.TMDB()
		}
	}
	if tmdbID <= 0 {
		return nil, nil
	}
	switch parent.Type {
	case core.TypeMovie:
		return s.videos.MovieVideos(ctx, tmdbID, lang)
	case core.TypeShow:
		return s.videos.TVVideos(ctx, tmdbID, lang)
	case core.TypeSeason:
		return s.videos.SeasonVideos(ctx, tmdbID, parent.Season, lang)
	case core.TypeEpisode:
		return s.videos.EpisodeVideos(ctx, tmdbID, parent.Season, parent.Episode, lang)
	}
	return nil, nil
}

// Lookup returns a registered extra by rating key.
func (s *Service) Lookup(key core.RatingKey) (Extra, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.registry[key]
	return e, ok
}

// registerLocked assigns the deterministic KindExtra key for an extra.
// The payload is a hash of the extra's identity, so the same extra keeps
// the same key across listings and restarts; hash collisions salt.
func (s *Service) registerLocked(e Extra) Extra {
	for salt := 0; ; salt++ {
		id := e.identity
		if salt > 0 {
			id = fmt.Sprintf("%s#%d", id, salt)
		}
		h := fnv.New64a()
		_, _ = h.Write([]byte(id))
		key := core.ExtraKey(int64(h.Sum64()%999_999_999_999_999) + 1)
		if prev, ok := s.registry[key]; ok && prev.identity != e.identity {
			continue
		}
		e.Key = key
		if len(s.registry) >= maxRegistryEntries {
			for k := range s.registry {
				delete(s.registry, k)
				break
			}
		}
		s.registry[key] = e
		return e
	}
}

// pruneLocked removes expired cache records. The hard caps below also bound
// entries whose keys are never revisited and therefore never expire lazily.
func (s *Service) pruneLocked() {
	now := s.now()
	for k, v := range s.lists {
		if !now.Before(v.expires) {
			delete(s.lists, k)
		}
	}
	for k, v := range s.resolved {
		if !v.Expires.IsZero() && !now.Before(v.Expires) {
			delete(s.resolved, k)
		}
	}
}

// Resolve turns an extra into a playable input, caching command results
// until the returned URL expires.
func (s *Service) Resolve(ctx context.Context, e Extra) (Resolved, error) {
	switch e.Source {
	case SourceLocal:
		return s.resolveLocal(ctx, e)
	case SourceCommand:
		return s.resolveCommand(ctx, e)
	}
	return Resolved{}, fmt.Errorf("extras: unknown source %q", e.Source)
}

func (s *Service) resolveLocal(ctx context.Context, e Extra) (Resolved, error) {
	if !s.LocalAvailable() || e.localPath == "" {
		return Resolved{}, core.ErrNotFound
	}
	files, err := s.local.LocalFiles(ctx)
	if err != nil {
		return Resolved{}, err
	}
	for _, f := range files {
		if f.Path != e.localPath {
			continue
		}
		st, err := filepathAbsStat(f.Path)
		if err != nil || !st {
			return Resolved{}, core.ErrNotFound
		}
		ct := f.Container
		if ct == "" {
			ct = strings.TrimPrefix(strings.ToLower(filepath.Ext(f.Path)), ".")
		}
		return Resolved{LocalPath: f.Path, Container: ct}, nil
	}
	return Resolved{}, core.ErrNotFound
}

func (s *Service) resolveCommand(ctx context.Context, e Extra) (Resolved, error) {
	if !s.CommandAvailable() {
		return Resolved{}, fmt.Errorf("extras: resolver command unavailable")
	}
	cacheKey := strings.ToLower(e.Site) + ":" + e.SiteKey
	s.mu.Lock()
	if r, ok := s.resolved[cacheKey]; ok && (r.Expires.IsZero() || s.now().Before(r.Expires)) {
		s.mu.Unlock()
		return r, nil
	}
	s.mu.Unlock()
	r, err := runResolver(ctx, s.cfg(), e, s.runner, s.now)
	if err != nil {
		return Resolved{}, err
	}
	s.mu.Lock()
	s.pruneLocked()
	s.resolved[cacheKey] = r
	if len(s.resolved) > maxResolvedEntries {
		for k := range s.resolved {
			if k != cacheKey {
				delete(s.resolved, k)
				break
			}
		}
	}
	s.mu.Unlock()
	return r, nil
}

// subtypeFor maps a TMDB video type to the Plex subtype and numeric
// extra type. Unknown types are treated as clips.
func subtypeFor(tmdbType string) (string, int) {
	switch tmdbType {
	case tmdb.VideoTrailer:
		return "trailer", 1
	case tmdb.VideoTeaser:
		return "teaser", 1
	case tmdb.VideoBehindTheScenes, tmdb.VideoBloopers:
		return "behindTheScenes", 3
	case tmdb.VideoFeaturette:
		return "featurette", 6
	default:
		return "clip", 4
	}
}

func videosToExtras(parent *core.Title, vs []tmdb.Video) []Extra {
	var out []Extra
	for _, v := range vs {
		if !ValidateSiteKey(v.Site, v.Key) {
			continue
		}
		sub, num := subtypeFor(v.Type)
		title := strings.TrimSpace(v.Name)
		if title == "" {
			title = parent.Title + " " + v.Type
		}
		out = append(out, Extra{
			ParentKey: parent.Key, ParentTitle: parent.Title, Title: title,
			Subtype: sub, ExtraType: num, Site: v.Site, SiteKey: v.Key,
			Language: ISO639(v.ISO639), Official: v.Official,
			PublishedAt: v.Published(), Size: v.Size, Source: SourceCommand,
			identity: fmt.Sprintf("%d|c|%s|%s", int64(parent.Key), strings.ToLower(v.Site), v.Key),
		})
	}
	return out
}

func dedupeExtras(in []Extra) []Extra {
	seen := map[string]int{}
	out := in[:0:0]
	for _, e := range in {
		k := e.identity
		if i, ok := seen[k]; ok {
			// Keep the better copy (official, then larger).
			if (e.Official && !out[i].Official) || e.Size > out[i].Size {
				out[i] = e
			}
			continue
		}
		seen[k] = len(out)
		out = append(out, e)
	}
	return out
}

func typeRank(sub string) int {
	switch sub {
	case "trailer":
		return 0
	case "teaser":
		return 1
	case "clip":
		return 2
	case "featurette":
		return 3
	case "behindTheScenes":
		return 4
	}
	return 5
}

func langRank(e Extra, iso string) int {
	switch {
	case iso != "" && e.Language == iso:
		return 0
	case e.Language == "en":
		return 1
	case e.Language == "":
		return 2
	}
	return 3
}

// sortExtras orders by type, official/local first, viewer language,
// resolution, then newest. Local (owner-provided) trailers rank with
// official ones.
func sortExtras(es []Extra, iso string) {
	sort.SliceStable(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if ra, rb := typeRank(a.Subtype), typeRank(b.Subtype); ra != rb {
			return ra < rb
		}
		oa, ob := a.Official || a.Source == SourceLocal, b.Official || b.Source == SourceLocal
		if oa != ob {
			return oa
		}
		if la, lb := langRank(a, iso), langRank(b, iso); la != lb {
			return la < lb
		}
		if a.Size != b.Size {
			return a.Size > b.Size
		}
		if !a.PublishedAt.Equal(b.PublishedAt) {
			return a.PublishedAt.After(b.PublishedAt)
		}
		return a.Title < b.Title
	})
}
