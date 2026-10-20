package livetv

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type liveProber interface {
	ProbeLive(context.Context, string) (*core.MediaInfo, error)
}

type authenticatedLiveProber interface {
	ProbeLiveWithHeaders(context.Context, string, map[string]string) (*core.MediaInfo, error)
}

func flattenInputHeaders(headers http.Header) map[string]string {
	out := make(map[string]string, len(headers))
	for key, values := range headers {
		out[key] = strings.Join(values, ", ")
	}
	return out
}

type liveProbeEntry struct {
	info    core.MediaInfo
	expires time.Time
}

// A short cache reuses capability-independent codec analysis on channel switches.
// Errors are never cached; URL changes naturally invalidate signed/rotated feeds.
func (s *Service) probeLiveInput(ctx context.Context, input string) (*core.MediaInfo, error) {
	return s.probeLiveInputWithHeaders(ctx, input, nil)
}

func (s *Service) probeLiveInputWithHeaders(ctx context.Context, input string, headers http.Header) (*core.MediaInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := input
	if len(headers) != 0 {
		encoded, _ := json.Marshal(headers)
		key = stableID(input, string(encoded))
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
	if len(headers) != 0 {
		p, ok := s.o.Transcoder.(authenticatedLiveProber)
		if !ok {
			return nil, fmt.Errorf("transcoder does not support authenticated live inputs")
		}
		info, err = p.ProbeLiveWithHeaders(ctx, input, flattenInputHeaders(headers))
	} else if p, ok := s.o.Transcoder.(liveProber); ok {
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
