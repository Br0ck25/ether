package config

// Debrid kinds (DebridProvider.Kind).
const (
	DebridRealDebrid = "realdebrid"
	DebridTorBox     = "torbox"
	DebridAllDebrid  = "alldebrid"
	DebridPremiumize = "premiumize"
	DebridDebridLink = "debridlink"
	DebridOffcloud   = "offcloud"
)

// DebridKinds lists the supported debrid services.
var DebridKinds = []string{DebridRealDebrid, DebridTorBox, DebridAllDebrid, DebridPremiumize, DebridDebridLink, DebridOffcloud}

// Debrid configures owner-supplied debrid accounts. Torrent releases from
// Torznab indexers are added to a debrid service that already holds them
// and streamed over HTTPS range requests; nothing is downloaded locally.
type Debrid struct {
	Providers []DebridProvider `json:"providers"`
	// CachedOnly accepts a torrent only when a provider can serve it
	// instantly. When false, an uncached torrent is left on the account to
	// download and the release is skipped for now; playback never waits.
	CachedOnly bool `json:"cachedOnly"`
	// RemoveUncached deletes torrents Ether added that turned out not to be
	// cached, keeping the owner's account clean (CachedOnly only).
	RemoveUncached bool `json:"removeUncached"`
	// ChunkBytes caps one HTTP range request (0 = 8 MiB). Requests start
	// small for a fast first byte and grow towards this size.
	ChunkBytes int64 `json:"chunkBytes,omitempty"`
	// ReadAheadChunks fixes the sequential read-ahead window in chunks
	// (0 = adaptive to measured throughput; negative disables read-ahead).
	ReadAheadChunks int `json:"readAheadChunks,omitempty"`
	// MaxConnections caps concurrent range requests per open file (0 = 4).
	MaxConnections int `json:"maxConnections,omitempty"`
}

// DebridProvider is one debrid account.
type DebridProvider struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // realdebrid, torbox, alldebrid, premiumize, debridlink, offcloud
	APIKey   string `json:"apiKey"`
	Priority int    `json:"priority"` // lower is tried first
	Enabled  bool   `json:"enabled"`
}

// EnabledProviders reports whether any provider is enabled with a key.
func (d Debrid) EnabledProviders() bool {
	for _, p := range d.Providers {
		if p.Enabled && p.APIKey != "" {
			return true
		}
	}
	return false
}
