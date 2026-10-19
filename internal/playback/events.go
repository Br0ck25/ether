package playback

import (
	"github.com/braedonsaunders/ether/internal/core"
	"sync"
	"time"
)

// Event contains a playback snapshot; subscribers must not retain live Sources.
type Event struct {
	Kind    string
	Session *core.PlaySession
	At      time.Time
	Error   string
}
type eventBus struct {
	mu   sync.RWMutex
	next int
	subs map[int]func(Event)
}

// Subscribe observes prepared sessions, decisions, failovers, errors and stops.
// It runs after session locks have been released. Unsubscribe during shutdown.
func (m *Manager) Subscribe(fn func(Event)) func() {
	m.events.mu.Lock()
	if m.events.subs == nil {
		m.events.subs = map[int]func(Event){}
	}
	id := m.events.next
	m.events.next++
	m.events.subs[id] = fn
	m.events.mu.Unlock()
	return func() { m.events.mu.Lock(); delete(m.events.subs, id); m.events.mu.Unlock() }
}
func (m *Manager) emitEvent(kind string, s *session, err error) {
	ev := Event{Kind: kind, At: m.now()}
	if s != nil {
		ev.Session = s.snapshot(ev.At)
		ev.Session.Source = nil
		ev.Session.Resolution = nil
	}
	if err != nil {
		ev.Error = "Playback failed; see server logs"
	}
	m.events.mu.RLock()
	fns := make([]func(Event), 0, len(m.events.subs))
	for _, fn := range m.events.subs {
		fns = append(fns, fn)
	}
	m.events.mu.RUnlock()
	for _, fn := range fns {
		func() {
			defer func() {
				if v := recover(); v != nil {
					m.log.Warn("playback subscriber panicked", "recover", v)
				}
			}()
			fn(ev)
		}()
	}
}
