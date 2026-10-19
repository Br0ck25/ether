package playback

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

const preResolveTTL = time.Minute

type warmEntry struct {
	caps    string
	done    chan struct{}
	ready   sync.Once
	expires time.Time
	res     *core.Resolution
}

func capsKey(c core.ClientCaps) string { b, _ := json.Marshal(c); return string(b) }

// PreResolve schedules detail-page warming under a separate speculative budget:
// two concurrent titles server-wide, one per user, six requests/minute/user,
// 8 seconds each, and at most 2 MiB at background priority. Request cancellation
// does not cancel a detail-page warmup; manager shutdown always does. It neither
// reserves a live stream nor writes resume/history. False means skipped.
func (m *Manager) PreResolve(_ context.Context, user *core.User, key core.RatingKey, caps core.ClientCaps) bool {
	if local, ok := m.deps.LocalMedia.(interface {
		Available(context.Context, core.RatingKey) bool
	}); ok && local.Available(m.ctx, key) {
		return false
	}
	if user == nil || caps.ClientID == "" || caps.Product == "" {
		return false
	}
	id := identity{userID: user.ID, clientID: caps.ClientID, key: key}
	now := m.now()
	m.mu.Lock()
	if m.closed || m.byIdent[id] != nil || m.flights[id] != nil {
		m.mu.Unlock()
		return false
	}
	for ident, e := range m.warm {
		if !now.Before(e.expires) {
			delete(m.warm, ident)
		}
	}
	if m.warm[id] != nil || len(m.warm) >= 2 {
		m.mu.Unlock()
		return false
	}
	for ident := range m.warm {
		if ident.userID == user.ID {
			m.mu.Unlock()
			return false
		}
	}
	// Active playback always takes priority over speculation.
	if len(m.sessions) > 0 || m.pendingN > 0 {
		m.mu.Unlock()
		return false
	}
	requests := m.warmRequests[user.ID]
	recent := requests[:0]
	for _, at := range requests {
		if now.Sub(at) < time.Minute {
			recent = append(recent, at)
		}
	}
	if len(recent) >= 6 {
		m.mu.Unlock()
		return false
	}
	m.warmRequests[user.ID] = append(recent, now)
	e := &warmEntry{caps: capsKey(caps), done: make(chan struct{}), expires: now.Add(preResolveTTL)}
	m.warm[id] = e
	u := *user
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer e.ready.Do(func() { close(e.done) })
		ctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
		defer cancel()
		title, err := m.deps.Catalog.Get(ctx, key)
		if err != nil || title == nil || title.Type == core.TypeShow || title.Type == core.TypeSeason {
			return
		}
		res, err := m.deps.Resolver.Resolve(ctx, core.ResolveRequest{Title: title, Client: caps, User: &u})
		if err != nil || res == nil {
			return
		}
		src, err := m.deps.Engine.Open(ctx, res.Release.ID, res.NZB)
		if err != nil {
			return
		}
		_ = src.Close()
		// Publish before prefetch so a play request need not wait for background IO.
		m.mu.Lock()
		e.res = res
		m.mu.Unlock()
		e.ready.Do(func() { close(e.done) })
		_ = m.deps.Engine.Prefetch(ctx, res.Release.ID, res.NZB, 0, 2<<20)
	}()
	return true
}

func (m *Manager) takeWarmResolution(ctx context.Context, id identity, caps core.ClientCaps) *core.Resolution {
	m.mu.Lock()
	e := m.warm[id]
	if e == nil || e.caps != capsKey(caps) || !m.now().Before(e.expires) {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	select {
	case <-e.done:
	case <-ctx.Done():
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.warm[id] != e {
		return nil
	}
	delete(m.warm, id)
	return e.res
}
