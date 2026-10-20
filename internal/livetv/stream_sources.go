package livetv

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// StreamSource is one independently replaceable input to a channel. EPG,
// numbering, favourites and viewer policy belong to Channel, never to an input.
type StreamSource struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Enabled  bool   `json:"enabled"`
	Priority int    `json:"priority"` // smaller first; equal priorities retain list order
	// AuthSourceID names the source whose provider credentials this input
	// uses; empty means the channel's own source.
	AuthSourceID string `json:"authSourceId,omitempty"`
}

type StreamSourceStatus struct {
	ID          string    `json:"id"`
	State       string    `json:"state"`
	Failures    int       `json:"failures"`
	LastChecked time.Time `json:"lastChecked,omitempty"`
	LastPlayed  time.Time `json:"lastPlayed,omitempty"`
	RetryAfter  time.Time `json:"retryAfter,omitempty"`
	LastError   string    `json:"lastError,omitempty"`
}

type streamSourceHealth struct {
	StreamSourceStatus
}

func normalizeStreamSources(c *Channel) error {
	if len(c.StreamSources) == 0 {
		if !validHTTP(c.StreamURL) {
			return fmt.Errorf("IPTV needs an HTTP(S) stream source")
		}
		id := stableID(c.ID, "primary")
		if c.SourceID != "" {
			id = stableID(c.ID, "playlist")
		}
		c.StreamSources = []StreamSource{{ID: id, Name: "Primary stream", URL: c.StreamURL, Enabled: true}}
	}
	if len(c.StreamSources) > 16 {
		return fmt.Errorf("a channel supports at most 16 stream sources")
	}
	c.StreamSources = append([]StreamSource(nil), c.StreamSources...)
	ids, urls := map[string]bool{}, map[string]bool{}
	for i := range c.StreamSources {
		src := &c.StreamSources[i]
		src.URL = strings.TrimSpace(src.URL)
		src.Name = strings.TrimSpace(src.Name)
		if !validHTTP(src.URL) {
			return fmt.Errorf("stream source %d must use HTTP(S)", i+1)
		}
		if src.ID == "" {
			src.ID = stableID(c.ID, src.URL)
		}
		if len(src.ID) > 200 || len(src.Name) > 200 || src.Priority < 0 || ids[src.ID] || urls[src.URL] {
			return fmt.Errorf("stream sources need unique IDs and URLs, valid names and nonnegative priorities")
		}
		if src.Name == "" {
			src.Name = fmt.Sprintf("Stream %d", i+1)
		}
		ids[src.ID], urls[src.URL] = true, true
	}
	sort.SliceStable(c.StreamSources, func(i, j int) bool { return c.StreamSources[i].Priority < c.StreamSources[j].Priority })
	c.StreamURL = ""
	for i := range c.StreamSources {
		c.StreamSources[i].Priority = i
		if c.StreamURL == "" && c.StreamSources[i].Enabled {
			c.StreamURL = c.StreamSources[i].URL
		}
	}
	if c.Enabled && c.StreamURL == "" {
		return fmt.Errorf("an enabled IPTV channel needs an enabled stream source")
	}
	return nil
}

func mergeImportedStreams(imported, previous Channel) []StreamSource {
	if len(previous.StreamSources) == 0 {
		return imported.StreamSources
	}
	out := append([]StreamSource(nil), previous.StreamSources...)
	// Only the import-owned entry follows refreshed playlist URLs. Removing that
	// entry is an explicit owner override; refresh must not recreate it.
	for i := range out {
		if out[i].ID == stableID(imported.ID, "playlist") {
			out[i].URL = imported.StreamURL
		}
	}
	return out
}

func sourceHealthKey(channelID string, src StreamSource) string {
	// A replacement URL starts with fresh health even when its entry ID survives.
	return stableID(channelID, src.ID, src.URL)
}

func (s *Service) sourceStatus(channelID string, src StreamSource) StreamSourceStatus {
	s.sourceMu.Lock()
	h := s.sourceHealth[sourceHealthKey(channelID, src)]
	s.sourceMu.Unlock()
	h.ID = src.ID
	switch {
	case !src.Enabled:
		h.State = "disabled"
	case h.RetryAfter.After(s.o.Now()):
		h.State = "cooling_down"
	case h.LastChecked.IsZero():
		h.State = "unchecked"
	case h.LastError != "":
		h.State = "retry_ready"
	default:
		h.State = "available"
	}
	return h.StreamSourceStatus
}

func (s *Service) recordStreamFailure(ss *session, rctx context.Context, err error) {
	if rctx.Err() == nil {
		s.recordSourceHealth(ss.info.Channel.ID, ss.inputSource, err, false)
	}
}

// StreamSourceStatuses is owner-only health, without source URLs or upstream
// errors that might contain credentials. Transient failures never ban a source.
func (s *Service) StreamSourceStatuses(ctx context.Context, channelID string) ([]StreamSourceStatus, error) {
	c, err := s.Channel(ctx, channelID)
	if err != nil {
		return nil, err
	}
	if c.Kind != "iptv" {
		return []StreamSourceStatus{}, nil
	}
	if err := normalizeStreamSources(&c); err != nil {
		return nil, err
	}
	out := make([]StreamSourceStatus, 0, len(c.StreamSources))
	for _, src := range c.StreamSources {
		out = append(out, s.sourceStatus(c.ID, src))
	}
	return out, nil
}

func (s *Service) recordSourceHealth(channelID string, src StreamSource, err error, played bool) {
	if src.ID == "" || errors.Is(err, context.Canceled) {
		return
	}
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	if s.sourceHealth == nil {
		s.sourceHealth = map[string]streamSourceHealth{}
	}
	key := sourceHealthKey(channelID, src)
	h := s.sourceHealth[key]
	h.ID, h.LastChecked = src.ID, s.o.Now()
	if err != nil {
		h.Failures++
		cooldown := 15 * time.Second
		for i := 1; i < h.Failures && cooldown < 2*time.Minute; i++ {
			cooldown *= 2
		}
		if cooldown > 2*time.Minute {
			cooldown = 2 * time.Minute
		}
		h.RetryAfter = s.o.Now().Add(cooldown)
		h.LastError = "source unavailable"
		if errors.Is(err, context.DeadlineExceeded) {
			h.LastError = "source timed out"
		}
	} else {
		h.Failures, h.LastError, h.RetryAfter = 0, "", time.Time{}
		if played {
			h.LastPlayed = s.o.Now()
		}
	}
	if len(s.sourceHealth) >= 4096 {
		// Bound stale entries left by edited/deleted channel URLs.
		for k, v := range s.sourceHealth {
			if v.LastChecked.Before(s.o.Now().Add(-10 * time.Minute)) {
				delete(s.sourceHealth, k)
			}
		}
		if len(s.sourceHealth) >= 4096 {
			var oldest string
			var at time.Time
			for k, v := range s.sourceHealth {
				if oldest == "" || v.LastChecked.Before(at) {
					oldest, at = k, v.LastChecked
				}
			}
			delete(s.sourceHealth, oldest)
		}
	}
	s.sourceHealth[key] = h
}

// startIPTV selects one input per viewing session, under the caller's existing
// ten-second startup deadline. It never substitutes another provider's bytes,
// keys or media sequence into the active HLS resource map.
func (s *Service) startIPTV(ctx context.Context, ss *session) error {
	c := ss.info.Channel
	if err := normalizeStreamSources(&c); err != nil {
		return err
	}
	var candidates []StreamSource
	for _, src := range c.StreamSources {
		if src.Enabled && !s.sourceStatus(c.ID, src).RetryAfter.After(s.o.Now()) {
			candidates = append(candidates, src)
		}
	}
	var last error = core.ErrNotAvailable
	for _, src := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		attemptCtx := ctx
		cancel := func() {}
		if len(candidates) > 1 {
			// Leave a stalled primary only half the total tune budget, so a
			// backup can still be tried. Fast failures advance immediately.
			attemptCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
		}
		ss.inputURL, ss.inputSource = src.URL, src
		headers, err := s.upstreamHeaders(attemptCtx, c, src)
		if err != nil {
			cancel()
			last = err
			continue
		}
		ss.inputHeaders = headers.Clone()
		ss.info.MediaInfo, ss.relayInfo = nil, nil
		ss.resources = map[string]string{}
		err = s.startIPTVSource(attemptCtx, ss)
		cancel()
		if err == nil {
			ss.info.ActiveSourceID = src.ID
			s.recordSourceHealth(c.ID, src, nil, false)
			return nil
		}
		if ss.relay != nil {
			ss.relay.Close()
			ss.relay = nil
		}
		last = err
		if !errors.Is(ctx.Err(), context.Canceled) {
			s.recordSourceHealth(c.ID, src, err, false)
		}
	}
	return last
}
