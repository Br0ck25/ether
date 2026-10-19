package config

import (
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Defaults returns a fresh config for a new install.
func Defaults(dataDir string) Config {
	return Config{
		Markers: Markers{TheIntroDB: true, DetectCached: true},
		Extras:  Extras{LocalTrailers: true, PrewarmBytes: 48 << 20, ResolverTimeout: Duration(20 * time.Second)},
		Server: Server{
			Port:               32400,
			BindAddress:        "0.0.0.0",
			FriendlyName:       "Ether",
			MachineIdentifier:  randomID(20),
			LogLevel:           "info",
			DefaultStreamLimit: 2,
		},
		Plex: Plex{
			ClientIdentifier:  randomID(16),
			UseUPnP:           true,
			SecureConnections: "preferred",
		},
		Cache: Cache{
			Dir:                 filepath.Join(dataDir, "cache"),
			BudgetBytes:         50 << 30,
			PrefetchNextEpisode: true,
			PrefetchBytes:       64 << 20,
			KeepInProgressWarm:  true,
			NextEpisodeAt:       0.8,
		},
		Catalog: Catalog{
			Language:        "en-US",
			Region:          "US",
			MinPopularity:   5,
			MinVotes:        100,
			RefreshInterval: Duration(24 * time.Hour),
			ShowUnreleased:  false,
		},
		Resolver: Resolver{
			HealthSamples:      12,
			SearchCacheTTL:     Duration(6 * time.Hour),
			UnavailableCooloff: Duration(24 * time.Hour),
			MaxCandidates:      3,
		},
		Quality: DefaultQuality(),
		Transcode: Transcode{
			HWAccel:              "auto",
			TempDir:              filepath.Join(dataDir, "transcode"),
			MaxTranscodes:        2,
			RemoteMaxBitrateKbps: 8000,
		},
		Downloads: Downloads{
			Enabled:              true,
			MaxConcurrent:        2,
			MaxConcurrentPerUser: 1,
			DiskBudgetBytes:      20 << 30,
			Expiry:               Duration(24 * time.Hour),
			MaxHeight:            1080,
		},
		Updates: Updates{Channel: "stable", AutoUpdate: false},
	}
}

func DefaultQuality() Quality {
	return Quality{
		Default: "1080p",
		Remote:  "1080p",
		Profiles: []core.QualityProfile{
			{
				Name:             "4K",
				MaxHeight:        2160,
				MinHeight:        1080,
				AllowHDR:         true,
				AllowDolbyVision: true,
				PreferredSources: []core.Source{core.SourceRemux, core.SourceBluRay, core.SourceWEBDL, core.SourceWEBRip},
				PreferredCodecs:  []string{"hevc", "av1", "h264"},
				PreferredAudio:   []string{"truehd", "eac3", "dtshd", "ac3", "aac"},
				MinSizeMBPerMin:  15,
				MaxSizeMBPerMin:  600,
			},
			{
				Name:             "1080p",
				MaxHeight:        1080,
				MinHeight:        720,
				PreferredSources: []core.Source{core.SourceWEBDL, core.SourceBluRay, core.SourceWEBRip, core.SourceHDTV},
				PreferredCodecs:  []string{"h264", "hevc"},
				PreferredAudio:   []string{"eac3", "ac3", "aac"},
				MinSizeMBPerMin:  5,
				MaxSizeMBPerMin:  150,
			},
			{
				Name:             "720p",
				MaxHeight:        720,
				MinHeight:        480,
				PreferredSources: []core.Source{core.SourceWEBDL, core.SourceWEBRip, core.SourceBluRay, core.SourceHDTV},
				PreferredCodecs:  []string{"h264"},
				PreferredAudio:   []string{"aac", "ac3", "eac3"},
				MinSizeMBPerMin:  2,
				MaxSizeMBPerMin:  60,
			},
		},
		Rules: []QualityRule{
			{Product: "Apple TV", Profile: "4K"},
		},
	}
}
