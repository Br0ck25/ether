// Package config is Ether's owner-editable settings, stored as JSON in the
// data directory. The web UI edits it through internal/admin; components get
// a snapshot at construction and subscribe for changes.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type Config struct {
	Server        Server        `json:"server"`
	Plex          Plex          `json:"plex"`
	Usenet        Usenet        `json:"usenet"`
	Indexers      []Indexer     `json:"indexers"`
	Cache         Cache         `json:"cache"`
	Catalog       Catalog       `json:"catalog"`
	Resolver      Resolver      `json:"resolver"`
	Quality       Quality       `json:"quality"`
	Transcode     Transcode     `json:"transcode"`
	Trakt         Trakt         `json:"trakt"`
	OpenSubtitles OpenSubtitles `json:"openSubtitles"`
	LLM           LLM           `json:"llm"`
	Updates       Updates       `json:"updates"`
	Spike         Spike         `json:"spike"`
	Downloads     Downloads     `json:"downloads"`
	Markers       Markers       `json:"markers"`
	Extras        Extras        `json:"extras"`
	SetupComplete bool          `json:"setupComplete"`
}

type Server struct {
	// Port for the Plex API and web UI. PMS uses 32400.
	Port         int    `json:"port"`
	BindAddress  string `json:"bindAddress"`
	FriendlyName string `json:"friendlyName"`
	// MachineIdentifier is generated on first run and never changes.
	MachineIdentifier string `json:"machineIdentifier"`
	LogLevel          string `json:"logLevel"`
	// DefaultStreamLimit applies to users without their own limit.
	DefaultStreamLimit int `json:"defaultStreamLimit"`
	MaxStreams         int `json:"maxStreams"` // server-wide cap, 0 = derived from Usenet connections
}

type Plex struct {
	// DisableCleanHome opts out of automatic account source cleanup.
	DisableCleanHome           bool   `json:"disableCleanHome"`
	CompatibilityVersion       string `json:"compatibilityVersion,omitempty"`
	LatestCompatibilityVersion string `json:"latestCompatibilityVersion,omitempty"`
	DisableVersionTracking     bool   `json:"disableVersionTracking"`
	// Token is the owner's plex.tv token from sign-in; the server token.
	Token            string `json:"token"`
	ClientIdentifier string `json:"clientIdentifier"` // X-Plex-Client-Identifier Ether uses towards plex.tv
	OwnerPlexID      int64  `json:"ownerPlexId"`
	OwnerUsername    string `json:"ownerUsername"`
	Claimed          bool   `json:"claimed"`
	// Remote access
	RemoteAccess bool     `json:"remoteAccess"`
	UseUPnP      bool     `json:"useUpnp"`
	PublicPort   int      `json:"publicPort"` // manual port forward; 0 = same as Server.Port
	PublicHost   string   `json:"publicHost"` // override detected WAN IP
	CustomURLs   []string `json:"customUrls"`
	// TLS certificate for *.plex.direct (or a self-signed fallback).
	CertFile          string `json:"certFile"`
	KeyFile           string `json:"keyFile"`
	CertHash          string `json:"certHash"`          // plex.direct subdomain hash
	SecureConnections string `json:"secureConnections"` // "required", "preferred", "disabled"
}

type Usenet struct {
	Providers []UsenetProvider `json:"providers"`
}

type UsenetProvider struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	TLS         bool   `json:"tls"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Connections int    `json:"connections"`
	// Priority: lower is tried first. Block accounts set Backfill.
	Priority int  `json:"priority"`
	Backfill bool `json:"backfill"` // only used for articles missing elsewhere
	Enabled  bool `json:"enabled"`
}

type Indexer struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"` // base URL; /api is appended
	APIKey     string `json:"apiKey"`
	DailyLimit int    `json:"dailyLimit"` // API hits per day, 0 = unlimited
	DailyGrabs int    `json:"dailyGrabs"` // NZB downloads per day, 0 = unlimited
	Priority   int    `json:"priority"`
	Enabled    bool   `json:"enabled"`
	// Categories override; empty uses Newznab defaults (2000 movies, 5000 TV).
	MovieCategories []int `json:"movieCategories"`
	TVCategories    []int `json:"tvCategories"`
}

type Cache struct {
	Dir         string `json:"dir"`
	BudgetBytes int64  `json:"budgetBytes"`
	// Prefetch
	PrefetchNextEpisode bool    `json:"prefetchNextEpisode"`
	PrefetchBytes       int64   `json:"prefetchBytes"` // opening bytes pulled ahead
	KeepInProgressWarm  bool    `json:"keepInProgressWarm"`
	NextEpisodeAt       float64 `json:"nextEpisodeAt"` // fraction of current episode, e.g. 0.8
}

type Catalog struct {
	TMDBKey         string   `json:"tmdbKey"`  // owner override; empty uses the bundled key
	Language        string   `json:"language"` // TMDB language, e.g. "en-US"
	Region          string   `json:"region"`
	MinPopularity   float64  `json:"minPopularity"`
	MinVotes        int      `json:"minVotes"`
	IncludeAdult    bool     `json:"includeAdult"`
	RefreshInterval Duration `json:"refreshInterval"`
	ShowUnreleased  bool     `json:"showUnreleased"` // Legacy field; release dates are always enforced.
}

type Resolver struct {
	HealthRace           int                 `json:"healthRace"`      // 0 = ranked checks; >0 = first healthy of bounded concurrent candidates
	OptimisticStart      bool                `json:"optimisticStart"` // return top NZB before background health check
	CustomFormats        []core.CustomFormat `json:"customFormats,omitempty"`
	CustomFormatScores   map[string]int      `json:"customFormatScores,omitempty"`
	MinCustomFormatScore *int                `json:"minCustomFormatScore,omitempty"`
	AllowCam             bool                `json:"allowCam"` // owner setting, off by default
	HealthSamples        int                 `json:"healthSamples"`
	SearchCacheTTL       Duration            `json:"searchCacheTtl"`
	UnavailableCooloff   Duration            `json:"unavailableCooloff"` // hide titles with no release this long
	MaxCandidates        int                 `json:"maxCandidates"`
	BlockedGroups        []string            `json:"blockedGroups"`
	BlockedTerms         []string            `json:"blockedTerms"`
}

type Quality struct {
	// Profiles by name. Rules pick a profile per client.
	Profiles []core.QualityProfile `json:"profiles"`
	Rules    []QualityRule         `json:"rules"`
	Default  string                `json:"default"`
	Remote   string                `json:"remote"` // profile for remote clients, "" = same as default
}

// QualityRule selects a profile when all non-empty matchers match.
type QualityRule struct {
	Product  string `json:"product,omitempty"`  // substring of X-Plex-Product
	Platform string `json:"platform,omitempty"` // substring of X-Plex-Platform
	UserID   int64  `json:"userId,omitempty"`
	Profile  string `json:"profile"`
}

type Transcode struct {
	FFmpegPath           string `json:"ffmpegPath"` // empty = bundled, then PATH
	FFprobePath          string `json:"ffprobePath"`
	HWAccel              string `json:"hwAccel"` // "auto", "none", "videotoolbox", "nvenc", "qsv", "vaapi"
	VAAPIDevice          string `json:"vaapiDevice"`
	TempDir              string `json:"tempDir"`
	MaxTranscodes        int    `json:"maxTranscodes"`
	RemoteMaxBitrateKbps int    `json:"remoteMaxBitrateKbps"`
}

// Downloads controls Plex offline downloads (allowSync + the Download
// Queue API). A download resolves a compact mobile-native release and
// remuxes it (stream copy) to a temporary MP4; nothing is stored
// permanently. Enabled by default for the owner: it is bounded by the
// disk budget, the concurrency caps and the normal stream limits, and
// the owner can turn it off entirely here.
type Downloads struct {
	Enabled bool `json:"enabled"`
	// AllowFriends lets shared (non-owner) users download. Off by
	// default, matching PMS sharing where sync is a separate grant; an
	// AccessPolicy AllowDownloads decision overrides this per user.
	AllowFriends bool `json:"allowFriends"`
	// MaxConcurrent bounds simultaneous remuxes server-wide (default 2),
	// MaxConcurrentPerUser per viewer (default 1).
	MaxConcurrent        int `json:"maxConcurrent"`
	MaxConcurrentPerUser int `json:"maxConcurrentPerUser"`
	// DiskBudgetBytes bounds the temporary remuxed files on disk at any
	// time (default 20 GiB). 0 uses the default.
	DiskBudgetBytes int64 `json:"diskBudgetBytes"`
	// Expiry is how long a finished download stays fetchable before its
	// temp file is deleted (default 24h).
	Expiry Duration `json:"expiry"`
	// MaxHeight caps the download profile's resolution (default 1080).
	MaxHeight int `json:"maxHeight"`
}

type Trakt struct {
	Enabled      bool   `json:"enabled"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

type OpenSubtitles struct {
	Enabled  bool   `json:"enabled"`
	APIKey   string `json:"apiKey"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type LLM struct {
	Enabled bool   `json:"enabled"` // off by default
	BaseURL string `json:"baseUrl"` // OpenAI-compatible, e.g. http://localhost:11434/v1
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}

type Updates struct {
	Channel    string `json:"channel"` // "stable", "beta"
	AutoUpdate bool   `json:"autoUpdate"`
	FeedURL    string `json:"feedUrl"`
}

// Spike serves local files as a library so the Plex front end can be
// verified against official apps without Usenet.
type Spike struct {
	Enabled    bool     `json:"enabled"`
	LocalFiles []string `json:"localFiles"`
}

// Duration marshals as a Go duration string ("6h").
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		var n int64
		if err2 := json.Unmarshal(b, &n); err2 != nil {
			return err
		}
		*d = Duration(n)
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}
func (d Duration) D() time.Duration { return time.Duration(d) }

// DefaultDataDir is where Ether keeps config, database and cache.
func DefaultDataDir() string {
	if v := os.Getenv("ETHER_DATA_DIR"); v != "" {
		return v
	}
	switch runtime.GOOS {
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "Ether")
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "Ether")
		}
	case "linux":
		if os.Geteuid() == 0 {
			return "/var/lib/ether"
		}
		if v := os.Getenv("XDG_DATA_HOME"); v != "" {
			return filepath.Join(v, "ether")
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "ether")
}

// Store loads, saves and broadcasts the config.
type Store struct {
	path          string
	credentialKey []byte
	mu            sync.RWMutex
	cfg           Config
	subs          []func(Config)
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dataDir, "config.json"), cfg: Defaults(dataDir)}
	b, err := os.ReadFile(s.path)
	readErr := err
	key, keyErr := credentialKey(dataDir, b)
	if keyErr != nil {
		return nil, keyErr
	}
	s.credentialKey = key
	err = readErr
	switch {
	case err == nil:
		plain, err := transformCredentials(b, s.credentialKey, false)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(plain, &s.cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", s.path, err)
		}
		if !strings.Contains(string(b), sealedPrefix) {
			if err := s.save(); err != nil {
				return nil, err
			}
		}
	case os.IsNotExist(err):
		if err := s.save(); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return s, nil
}

func (s *Store) Path() string { return s.path }

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.cfg)
}

// Update applies fn to a copy of the config, saves it and notifies subscribers.
func (s *Store) Update(fn func(*Config) error) error {
	s.mu.Lock()
	next := clone(s.cfg)
	if err := fn(&next); err != nil {
		s.mu.Unlock()
		return err
	}
	prev := s.cfg
	s.cfg = next
	if err := s.save(); err != nil {
		s.cfg = prev
		s.mu.Unlock()
		return err
	}
	subs := append([]func(Config){}, s.subs...)
	s.mu.Unlock()
	for _, f := range subs {
		f(clone(next))
	}
	return nil
}

func (s *Store) Subscribe(fn func(Config)) {
	s.mu.Lock()
	s.subs = append(s.subs, fn)
	s.mu.Unlock()
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	b, err = transformCredentials(b, s.credentialKey, true)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
