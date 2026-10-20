package debrid

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type mediaPause struct {
	until  time.Time
	status int
}

func mediaPauseKey(provider, raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	// Signed paths and queries are credentials, not a cooldown scope.
	return provider + "\x00" + strings.ToLower(u.Scheme+"://"+u.Host)
}

func mediaPausedError(p mediaPause, now time.Time) error {
	if p.status == http.StatusTooManyRequests {
		return fmt.Errorf("debrid: media HTTP %d; retry in %s: %w: %w", p.status, p.until.Sub(now).Round(time.Millisecond), core.ErrSourceUnavailable, ErrRateLimited)
	}
	return fmt.Errorf("debrid: media HTTP %d; retry in %s: %w", p.status, p.until.Sub(now).Round(time.Millisecond), core.ErrSourceUnavailable)
}

func (e *Engine) checkMediaPause(provider, raw string) error {
	key, now := mediaPauseKey(provider, raw), e.now()
	e.mediaPauseMu.Lock()
	defer e.mediaPauseMu.Unlock()
	p, ok := e.mediaPauses[key]
	if !ok {
		return nil
	}
	if !now.Before(p.until) {
		delete(e.mediaPauses, key)
		return nil
	}
	return mediaPausedError(p, now)
}

func (e *Engine) pauseMedia(provider, raw string, status int, after time.Duration) error {
	if after <= 0 {
		after = 2 * time.Second
	}
	now := e.now()
	p := mediaPause{until: now.Add(after), status: status}
	if key := mediaPauseKey(provider, raw); key != "" {
		e.mediaPauseMu.Lock()
		if e.mediaPauses == nil {
			e.mediaPauses = make(map[string]mediaPause)
		}
		for k, v := range e.mediaPauses {
			if !now.Before(v.until) {
				delete(e.mediaPauses, k)
			}
		}
		if _, exists := e.mediaPauses[key]; !exists && len(e.mediaPauses) >= 512 {
			// Signed links may rotate across many CDN hosts. Bound this
			// advisory cache; evict the entry closest to its retry time.
			var oldest string
			var until time.Time
			for k, v := range e.mediaPauses {
				if oldest == "" || v.until.Before(until) {
					oldest, until = k, v.until
				}
			}
			delete(e.mediaPauses, oldest)
		}
		if previous, ok := e.mediaPauses[key]; ok && previous.until.After(p.until) {
			p = previous
		}
		e.mediaPauses[key] = p
		e.mediaPauseMu.Unlock()
	}
	return mediaPausedError(p, now)
}
