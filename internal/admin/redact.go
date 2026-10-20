package admin

import "github.com/braedonsaunders/ether/internal/config"

func redact(s string) string {
	if s == "" {
		return ""
	}
	return RedactedSecret
}

func isRedacted(s string) bool { return s == RedactedSecret }

// RedactConfig returns a copy of cfg with every secret replaced by
// RedactedSecret ("••••"). Empty secrets stay empty so the UI can tell
// "unset" from "set". The input is not modified.
func RedactConfig(cfg config.Config) config.Config {
	out := cfg
	out.Markers.APIKey = redact(out.Markers.APIKey)
	out.Plex.Token = redact(out.Plex.Token)
	out.Catalog.TMDBKey = redact(out.Catalog.TMDBKey)
	out.Trakt.ClientSecret = redact(out.Trakt.ClientSecret)
	out.OpenSubtitles.APIKey = redact(out.OpenSubtitles.APIKey)
	out.OpenSubtitles.Password = redact(out.OpenSubtitles.Password)
	out.LLM.APIKey = redact(out.LLM.APIKey)
	out.Database = out.Database.Redacted()
	if cfg.Usenet.Providers != nil {
		out.Usenet.Providers = make([]config.UsenetProvider, len(cfg.Usenet.Providers))
		for i, p := range cfg.Usenet.Providers {
			p.Password = redact(p.Password)
			out.Usenet.Providers[i] = p
		}
	}
	if cfg.Indexers != nil {
		out.Indexers = make([]config.Indexer, len(cfg.Indexers))
		for i, ix := range cfg.Indexers {
			ix.APIKey = redact(ix.APIKey)
			out.Indexers[i] = ix
		}
	}
	if cfg.Debrid.Providers != nil {
		out.Debrid.Providers = make([]config.DebridProvider, len(cfg.Debrid.Providers))
		for i, p := range cfg.Debrid.Providers {
			p.APIKey = redact(p.APIKey)
			out.Debrid.Providers[i] = p
		}
	}
	return out
}

// preserve returns next, unless it is the redaction placeholder, in which
// case the stored value prev is kept.
func preserve(next, prev string) string {
	if isRedacted(next) {
		return prev
	}
	return next
}

// UnredactConfig restores secrets in next that the client sent back as the
// redaction placeholder, using prev (the currently stored config).
// List entries are matched by ID first, then by position, so reordering a
// provider list in the UI does not swap passwords. A placeholder with no
// match becomes empty (validation then decides whether that is acceptable).
func UnredactConfig(next, prev config.Config) config.Config {
	out := next
	out.Markers.APIKey = preserve(out.Markers.APIKey, prev.Markers.APIKey)
	out.Plex.Token = preserve(out.Plex.Token, prev.Plex.Token)
	out.Catalog.TMDBKey = preserve(out.Catalog.TMDBKey, prev.Catalog.TMDBKey)
	out.Trakt.ClientSecret = preserve(out.Trakt.ClientSecret, prev.Trakt.ClientSecret)
	out.OpenSubtitles.APIKey = preserve(out.OpenSubtitles.APIKey, prev.OpenSubtitles.APIKey)
	out.OpenSubtitles.Password = preserve(out.OpenSubtitles.Password, prev.OpenSubtitles.Password)
	out.LLM.APIKey = preserve(out.LLM.APIKey, prev.LLM.APIKey)
	out.Usenet.Providers = unredactProviders(next.Usenet.Providers, prev.Usenet.Providers)
	out.Indexers = unredactIndexers(next.Indexers, prev.Indexers)
	out.Debrid.Providers = unredactDebrid(next.Debrid.Providers, prev.Debrid.Providers)
	return out
}

func unredactDebrid(next, prev []config.DebridProvider) []config.DebridProvider {
	if next == nil {
		return nil
	}
	byID := make(map[string]string, len(prev))
	for _, p := range prev {
		if p.ID != "" {
			byID[p.ID] = p.APIKey
		}
	}
	out := make([]config.DebridProvider, len(next))
	for i, p := range next {
		if isRedacted(p.APIKey) {
			switch {
			case p.ID != "" && byID[p.ID] != "":
				p.APIKey = byID[p.ID]
			case i < len(prev):
				p.APIKey = prev[i].APIKey
			default:
				p.APIKey = ""
			}
		}
		out[i] = p
	}
	return out
}

// unredactDebridProvider restores one debrid provider's key (test calls).
func unredactDebridProvider(p config.DebridProvider, prev config.Config) config.DebridProvider {
	if !isRedacted(p.APIKey) {
		return p
	}
	for _, old := range prev.Debrid.Providers {
		if (p.ID != "" && old.ID == p.ID) || (p.ID == "" && old.Kind == p.Kind) {
			p.APIKey = old.APIKey
			return p
		}
	}
	p.APIKey = ""
	return p
}

func unredactProviders(next, prev []config.UsenetProvider) []config.UsenetProvider {
	if next == nil {
		return nil
	}
	byID := make(map[string]string, len(prev))
	for _, p := range prev {
		if p.ID != "" {
			byID[p.ID] = p.Password
		}
	}
	out := make([]config.UsenetProvider, len(next))
	for i, p := range next {
		if isRedacted(p.Password) {
			switch {
			case p.ID != "" && byID[p.ID] != "":
				p.Password = byID[p.ID]
			case i < len(prev):
				p.Password = prev[i].Password
			default:
				p.Password = ""
			}
		}
		out[i] = p
	}
	return out
}

func unredactIndexers(next, prev []config.Indexer) []config.Indexer {
	if next == nil {
		return nil
	}
	byID := make(map[string]string, len(prev))
	for _, ix := range prev {
		if ix.ID != "" {
			byID[ix.ID] = ix.APIKey
		}
	}
	out := make([]config.Indexer, len(next))
	for i, ix := range next {
		if isRedacted(ix.APIKey) {
			switch {
			case ix.ID != "" && byID[ix.ID] != "":
				ix.APIKey = byID[ix.ID]
			case i < len(prev):
				ix.APIKey = prev[i].APIKey
			default:
				ix.APIKey = ""
			}
		}
		out[i] = ix
	}
	return out
}

// unredactProvider restores one provider's password (wizard test calls).
func unredactProvider(p config.UsenetProvider, prev config.Config) config.UsenetProvider {
	if !isRedacted(p.Password) {
		return p
	}
	for _, old := range prev.Usenet.Providers {
		if (p.ID != "" && old.ID == p.ID) || (p.ID == "" && old.Host == p.Host && old.Username == p.Username) {
			p.Password = old.Password
			return p
		}
	}
	p.Password = ""
	return p
}

func unredactIndexer(ix config.Indexer, prev config.Config) config.Indexer {
	if !isRedacted(ix.APIKey) {
		return ix
	}
	for _, old := range prev.Indexers {
		if (ix.ID != "" && old.ID == ix.ID) || (ix.ID == "" && old.URL == ix.URL) {
			ix.APIKey = old.APIKey
			return ix
		}
	}
	ix.APIKey = ""
	return ix
}
