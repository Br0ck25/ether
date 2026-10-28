package livetv

import (
	"context"
	"encoding/json"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type liveProber interface {
	ProbeLive(context.Context, string) (*core.MediaInfo, error)
}

type liveProbeEntry struct {
	info    core.MediaInfo
	expires time.Time
}

// A short cache reuses capability-independent codec analysis on channel switches.
// Errors are never cached; URL changes naturally invalidate signed/rotated feeds.
func (s *Service) probeLiveInput(ctx context.Context, input string) (*core.MediaInfo, error) {
	return s.probeLiveCached(ctx, input, input)
}

// probeSessionInput probes the session's input through its relay grant. The
// cache stays keyed by the upstream URL and headers, not the per-tune token.
func (s *Service) probeSessionInput(ctx context.Context, ss *session) (*core.MediaInfo, error) {
	input, err := s.relayInput(ss)
	if err != nil {
		return nil, err
	}
	key := ss.inputURL
	if len(ss.inputHeaders) != 0 {
		encoded, _ := json.Marshal(ss.inputHeaders)
		key = stableID(ss.inputURL, string(encoded))
	}
	return s.probeLiveCached(ctx, key, input)
}

func (s *Service) probeLiveCached(ctx context.Context, key, input string) (*core.MediaInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.probeMu.Lock()
	entry, found := s.probeCache[key]
	s.probeMu.Unlock()
	if found && s.o.Now().Before(entry.expires) {
		info := entry.info
		return &info, nil
	}
	var info *core.MediaInfo
	var err error
	if p, ok := s.o.Transcoder.(liveProber); ok {
		info, err = p.ProbeLive(ctx, input)
	} else {
		info, err = s.o.Transcoder.Probe(ctx, input)
	}
	if err != nil || info == nil || info.Video() == nil {
		return info, err
	}
	s.probeMu.Lock()
	if s.probeCache == nil {
		s.probeCache = map[string]liveProbeEntry{}
	}
	if len(s.probeCache) >= 256 {
		for key, e := range s.probeCache {
			if !s.o.Now().Before(e.expires) {
				delete(s.probeCache, key)
			}
		}
		if len(s.probeCache) >= 256 {
			var oldest string
			var expires time.Time
			for key, e := range s.probeCache {
				if oldest == "" || e.expires.Before(expires) {
					oldest, expires = key, e.expires
				}
			}
			delete(s.probeCache, oldest)
		}
	}
	s.probeCache[key] = liveProbeEntry{info: *info, expires: s.o.Now().Add(2 * time.Minute)}
	s.probeMu.Unlock()
	return info, nil
}
