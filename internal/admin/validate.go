package admin

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/ether-media/ether/internal/config"
)

// FieldError is one validation problem, addressed by dotted field path.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

type validator struct{ errs []FieldError }

func (v *validator) add(field, format string, args ...any) {
	v.errs = append(v.errs, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
}
func (v *validator) port(field string, n int, allowZero bool) {
	if allowZero && n == 0 {
		return
	}
	if n < 1 || n > 65535 {
		v.add(field, "must be a port between 1 and 65535")
	}
}
func (v *validator) httpURL(field, s string, required bool) {
	if s == "" {
		if required {
			v.add(field, "is required")
		}
		return
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		v.add(field, "must be an http(s) URL")
	}
}
func (v *validator) oneOf(field, s string, allowed ...string) {
	for _, a := range allowed {
		if s == a {
			return
		}
	}
	v.add(field, "must be one of: %s", strings.Join(allowed, ", "))
}

// ValidateConfig checks a candidate config for the problems the owner can
// fix in the UI. It is intentionally permissive about empty optional
// sections (an unconfigured install must remain saveable mid-wizard) and
// strict about structurally broken values.
func ValidateConfig(cfg config.Config) []FieldError {
	v := &validator{}

	v.port("server.port", cfg.Server.Port, false)
	if strings.TrimSpace(cfg.Server.FriendlyName) == "" {
		v.add("server.friendlyName", "is required")
	}
	if cfg.Server.LogLevel != "" {
		v.oneOf("server.logLevel", cfg.Server.LogLevel, "debug", "info", "warn", "error")
	}
	if cfg.Server.DefaultStreamLimit < 0 {
		v.add("server.defaultStreamLimit", "must be >= 0")
	}
	if cfg.Server.MaxStreams < 0 {
		v.add("server.maxStreams", "must be >= 0")
	}

	v.port("plex.publicPort", cfg.Plex.PublicPort, true)
	if cfg.Plex.SecureConnections != "" {
		v.oneOf("plex.secureConnections", cfg.Plex.SecureConnections, "required", "preferred", "disabled")
	}

	seenProv := map[string]bool{}
	for i, p := range cfg.Usenet.Providers {
		f := fmt.Sprintf("usenet.providers[%d]", i)
		if strings.TrimSpace(p.Host) == "" {
			v.add(f+".host", "is required")
		}
		v.port(f+".port", p.Port, false)
		if p.Connections < 0 || p.Connections > 500 {
			v.add(f+".connections", "must be between 0 and 500")
		}
		if p.ID != "" {
			if seenProv[p.ID] {
				v.add(f+".id", "duplicate provider id %q", p.ID)
			}
			seenProv[p.ID] = true
		}
	}

	seenIx := map[string]bool{}
	for i, ix := range cfg.Indexers {
		f := fmt.Sprintf("indexers[%d]", i)
		v.httpURL(f+".url", ix.URL, true)
		if ix.DailyLimit < 0 {
			v.add(f+".dailyLimit", "must be >= 0")
		}
		if ix.DailyGrabs < 0 {
			v.add(f+".dailyGrabs", "must be >= 0")
		}
		if ix.ID != "" {
			if seenIx[ix.ID] {
				v.add(f+".id", "duplicate indexer id %q", ix.ID)
			}
			seenIx[ix.ID] = true
		}
	}

	if strings.TrimSpace(cfg.Cache.Dir) == "" {
		v.add("cache.dir", "is required")
	}
	if cfg.Cache.BudgetBytes < 0 {
		v.add("cache.budgetBytes", "must be >= 0")
	}
	if cfg.Cache.PrefetchBytes < 0 {
		v.add("cache.prefetchBytes", "must be >= 0")
	}
	if cfg.Cache.NextEpisodeAt < 0 || cfg.Cache.NextEpisodeAt > 1 {
		v.add("cache.nextEpisodeAt", "must be between 0 and 1")
	}

	if cfg.Catalog.MinPopularity < 0 {
		v.add("catalog.minPopularity", "must be >= 0")
	}
	if cfg.Catalog.MinVotes < 0 {
		v.add("catalog.minVotes", "must be >= 0")
	}

	if cfg.Resolver.HealthSamples < 0 {
		v.add("resolver.healthSamples", "must be >= 0")
	}
	if cfg.Resolver.MaxCandidates < 0 {
		v.add("resolver.maxCandidates", "must be >= 0")
	}

	profiles := map[string]bool{}
	for i, p := range cfg.Quality.Profiles {
		f := fmt.Sprintf("quality.profiles[%d]", i)
		if strings.TrimSpace(p.Name) == "" {
			v.add(f+".name", "is required")
			continue
		}
		if profiles[p.Name] {
			v.add(f+".name", "duplicate profile name %q", p.Name)
		}
		profiles[p.Name] = true
		if p.MinHeight < 0 || p.MaxHeight < 0 || (p.MaxHeight > 0 && p.MinHeight > p.MaxHeight) {
			v.add(f+".minHeight", "must be <= maxHeight")
		}
		if p.MinSizeMBPerMin < 0 || p.MaxSizeMBPerMin < 0 ||
			(p.MaxSizeMBPerMin > 0 && p.MinSizeMBPerMin > p.MaxSizeMBPerMin) {
			v.add(f+".minSizeMbPerMin", "must be <= maxSizeMbPerMin")
		}
	}
	if cfg.Quality.Default != "" && len(profiles) > 0 && !profiles[cfg.Quality.Default] {
		v.add("quality.default", "names a profile that does not exist")
	}
	if cfg.Quality.Remote != "" && len(profiles) > 0 && !profiles[cfg.Quality.Remote] {
		v.add("quality.remote", "names a profile that does not exist")
	}
	for i, r := range cfg.Quality.Rules {
		if r.Profile == "" {
			v.add(fmt.Sprintf("quality.rules[%d].profile", i), "is required")
		} else if len(profiles) > 0 && !profiles[r.Profile] {
			v.add(fmt.Sprintf("quality.rules[%d].profile", i), "names a profile that does not exist")
		}
	}

	if cfg.Transcode.HWAccel != "" {
		v.oneOf("transcode.hwAccel", cfg.Transcode.HWAccel, "auto", "none", "videotoolbox", "nvenc", "qsv", "vaapi")
	}
	if cfg.Transcode.MaxTranscodes < 0 {
		v.add("transcode.maxTranscodes", "must be >= 0")
	}
	if cfg.Transcode.RemoteMaxBitrateKbps < 0 {
		v.add("transcode.remoteMaxBitrateKbps", "must be >= 0")
	}

	if cfg.LLM.Enabled {
		v.httpURL("llm.baseUrl", cfg.LLM.BaseURL, true)
		if strings.TrimSpace(cfg.LLM.Model) == "" {
			v.add("llm.model", "is required when the LLM is enabled")
		}
	} else if cfg.LLM.BaseURL != "" {
		v.httpURL("llm.baseUrl", cfg.LLM.BaseURL, false)
	}
	if cfg.Trakt.Enabled && strings.TrimSpace(cfg.Trakt.ClientID) == "" {
		v.add("trakt.clientId", "is required when Trakt is enabled")
	}
	if cfg.Updates.Channel != "" {
		v.oneOf("updates.channel", cfg.Updates.Channel, "stable", "beta")
	}
	if cfg.Updates.FeedURL != "" {
		v.httpURL("updates.feedUrl", cfg.Updates.FeedURL, false)
	}
	return v.errs
}
