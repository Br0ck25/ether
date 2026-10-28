package spike

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// watchedFraction marks an item watched once playback crosses it.
const watchedFraction = 0.9

type stateEntry struct {
	ws        core.WatchStatus
	completed bool // already counted for the current viewing
}

// memState is an in-memory core.State.
type memState struct {
	mu      sync.Mutex
	users   map[int64]map[core.RatingKey]*stateEntry
	history []core.HistoryEntry
	now     func() time.Time
}

func newMemState() *memState {
	return &memState{users: map[int64]map[core.RatingKey]*stateEntry{}, now: time.Now}
}

func (s *memState) entry(u int64, k core.RatingKey) *stateEntry {
	m := s.users[u]
	if m == nil {
		m = map[core.RatingKey]*stateEntry{}
		s.users[u] = m
	}
	e := m[k]
	if e == nil {
		e = &stateEntry{ws: core.WatchStatus{Key: k}}
		m[k] = e
	}
	return e
}

func (s *memState) Get(_ context.Context, u int64, k core.RatingKey) (core.WatchStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.users[u][k]; ok {
		return e.ws, nil
	}
	return core.WatchStatus{Key: k}, nil
}

func (s *memState) GetMany(_ context.Context, u int64, ks []core.RatingKey) (map[core.RatingKey]core.WatchStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[core.RatingKey]core.WatchStatus, len(ks))
	for _, k := range ks {
		if e, ok := s.users[u][k]; ok {
			out[k] = e.ws
		}
	}
	return out, nil
}

func (s *memState) Progress(_ context.Context, u int64, k core.RatingKey, off, dur time.Duration, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	e := s.entry(u, k)
	if dur > 0 {
		e.ws.Duration = dur
	}
	e.ws.UpdatedAt = now
	if e.ws.Duration > 0 && float64(off) >= watchedFraction*float64(e.ws.Duration) {
		if !e.completed {
			e.completed = true
			e.ws.ViewCount++
			e.ws.LastViewedAt = now
			s.history = append(s.history, core.HistoryEntry{UserID: u, Key: k, ViewedAt: now, Completed: true})
		}
		e.ws.ViewOffset = 0
		return nil
	}
	e.completed = false
	e.ws.ViewOffset = off
	e.ws.LastViewedAt = now
	return nil
}

func (s *memState) Scrobble(_ context.Context, u int64, k core.RatingKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	e := s.entry(u, k)
	e.ws.ViewCount++
	e.ws.ViewOffset = 0
	e.ws.LastViewedAt, e.ws.UpdatedAt = now, now
	s.history = append(s.history, core.HistoryEntry{UserID: u, Key: k, ViewedAt: now, Completed: true})
	return nil
}

func (s *memState) Unscrobble(_ context.Context, u int64, k core.RatingKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(u, k)
	e.ws.ViewCount, e.ws.ViewOffset, e.completed = 0, 0, false
	e.ws.UpdatedAt = s.now()
	return nil
}

func (s *memState) Rate(_ context.Context, u int64, k core.RatingKey, rating float64) error {
	if rating < 0 || rating > 10 {
		return fmt.Errorf("rating %v out of range", rating)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry(u, k)
	e.ws.UserRating = rating
	e.ws.UpdatedAt = s.now()
	return nil
}

func (s *memState) InProgress(_ context.Context, u int64, limit int) ([]core.WatchStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.WatchStatus
	for _, e := range s.users[u] {
		if e.ws.ViewOffset > 0 {
			out = append(out, e.ws)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *memState) History(_ context.Context, u int64, since time.Time, limit int) ([]core.HistoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.HistoryEntry
	for i := len(s.history) - 1; i >= 0; i-- {
		h := s.history[i]
		if h.UserID != u || h.ViewedAt.Before(since) {
			continue
		}
		out = append(out, h)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *memState) InProgressAny(context.Context) ([]core.RatingKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[core.RatingKey]bool{}
	var out []core.RatingKey
	for _, m := range s.users {
		for k, e := range m {
			if e.ws.ViewOffset > 0 && !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// ---------------------------------------------------------------------------
// Playlists

type memPlaylists struct {
	mu   sync.Mutex
	next int64
	pls  map[core.RatingKey]*core.Playlist
	now  func() time.Time
}

func newMemPlaylists() *memPlaylists {
	return &memPlaylists{next: 1, pls: map[core.RatingKey]*core.Playlist{}, now: time.Now}
}

func clonePlaylist(p *core.Playlist) *core.Playlist {
	cp := *p
	cp.Items = append([]core.RatingKey(nil), p.Items...)
	return &cp
}

func (m *memPlaylists) owned(u int64, k core.RatingKey) (*core.Playlist, error) {
	p, ok := m.pls[k]
	if !ok || p.UserID != u {
		return nil, core.ErrNotFound
	}
	return p, nil
}

func (m *memPlaylists) List(_ context.Context, u int64) ([]core.Playlist, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []core.Playlist
	for _, p := range m.pls {
		if p.UserID == u {
			out = append(out, *clonePlaylist(p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (m *memPlaylists) Get(_ context.Context, u int64, k core.RatingKey) (*core.Playlist, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.owned(u, k)
	if err != nil {
		return nil, err
	}
	return clonePlaylist(p), nil
}

func (m *memPlaylists) Create(_ context.Context, u int64, title string, items []core.RatingKey) (*core.Playlist, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := &core.Playlist{Key: core.PlaylistKey(m.next), UserID: u, Title: title, Items: append([]core.RatingKey(nil), items...), UpdatedAt: m.now()}
	m.next++
	m.pls[p.Key] = p
	return clonePlaylist(p), nil
}

func (m *memPlaylists) Add(_ context.Context, u int64, k core.RatingKey, items []core.RatingKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.owned(u, k)
	if err != nil {
		return err
	}
	p.Items = append(p.Items, items...)
	p.UpdatedAt = m.now()
	return nil
}

func (m *memPlaylists) Remove(_ context.Context, u int64, k core.RatingKey, idx int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.owned(u, k)
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(p.Items) {
		return core.ErrNotFound
	}
	p.Items = append(p.Items[:idx:idx], p.Items[idx+1:]...)
	p.UpdatedAt = m.now()
	return nil
}

func (m *memPlaylists) Move(_ context.Context, u int64, k core.RatingKey, from, to int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.owned(u, k)
	if err != nil {
		return err
	}
	if from < 0 || from >= len(p.Items) || to < 0 || to >= len(p.Items) {
		return core.ErrNotFound
	}
	x := p.Items[from]
	rest := append(append([]core.RatingKey(nil), p.Items[:from]...), p.Items[from+1:]...)
	p.Items = append(rest[:to:to], append([]core.RatingKey{x}, rest[to:]...)...)
	p.UpdatedAt = m.now()
	return nil
}

func (m *memPlaylists) Delete(_ context.Context, u int64, k core.RatingKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.owned(u, k); err != nil {
		return err
	}
	delete(m.pls, k)
	return nil
}
