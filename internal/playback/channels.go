package playback

import (
	"context"
	"fmt"
	"github.com/braedonsaunders/ether/internal/core"
	"strings"
)

// PrepareChannel is an internal playout operation. One viewing session may
// overlap at most three programme sources for continuity. The server and user
// limits count the group once, and ordinary Prepare cannot create groups.
func (m *Manager) PrepareChannel(ctx context.Context, u *core.User, key core.RatingKey, caps core.ClientCaps, group string) (*core.PlaySession, error) {
	if u == nil {
		return nil, core.ErrUnauthorized
	}
	if len(group) < 16 || len(group) > 128 || strings.ContainsAny(group, "/\\\x00\r\n") {
		return nil, fmt.Errorf("invalid channel session")
	}
	id := identity{userID: u.ID, clientID: caps.ClientID, key: key}
	m.mu.Lock()
	if m.channelGroups == nil {
		m.channelGroups = map[identity]string{}
	}
	if current := m.channelGroups[id]; current != "" && current != group {
		m.mu.Unlock()
		return nil, fmt.Errorf("programme source belongs to another channel session")
	}
	m.channelGroups[id] = group
	m.mu.Unlock()
	ps, err := m.Prepare(ctx, u, key, caps)
	if err != nil {
		m.mu.Lock()
		if m.byIdent[id] == nil && !m.hasFlightLocked(id) {
			delete(m.channelGroups, id)
		}
		m.mu.Unlock()
	}
	return ps, err
}

// reservationCountsLocked counts distinct viewing groups and ordinary sources.
func (m *Manager) reservationCountsLocked(id identity) (user, total, children int) {
	group := m.channelGroups[id]
	units := map[string]int64{}
	for g, slot := range m.externalGroups {
		if g == group && slot.user.ID == id.userID {
			continue
		}
		units[fmt.Sprintf("group/%d/%s", slot.user.ID, g)] = slot.user.ID
	}
	add := func(other identity, unit string) {
		g := m.channelGroups[other]
		if group != "" && g == group && other.userID == id.userID {
			children++
			return
		}
		if g != "" {
			unit = fmt.Sprintf("group/%d/%s", other.userID, g)
		}
		units[unit] = other.userID
	}
	for sid, s := range m.sessions {
		if s.ident.userID == id.userID && s.ident.clientID == id.clientID {
			continue
		}
		add(s.ident, "session/"+sid)
	}
	for other := range m.reservations {
		add(other, fmt.Sprintf("pending/%d/%s/%d", other.userID, other.clientID, other.key))
	}
	for _, uid := range units {
		total++
		if uid == id.userID {
			user++
		}
	}
	return
}

type externalGroup struct {
	user core.User
	caps core.ClientCaps
}

// BeginChannel reserves a viewing unit in the same transaction as ordinary
// Prepare. This also counts passthrough IPTV, which has no on-demand source.
func (m *Manager) BeginChannel(u *core.User, caps core.ClientCaps, group string) error {
	if u == nil {
		return core.ErrUnauthorized
	}
	if len(group) < 16 || len(group) > 128 {
		return fmt.Errorf("invalid channel group")
	}
	cfg := m.config()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("playback is closing")
	}
	if slot, ok := m.externalGroups[group]; ok {
		if slot.user.ID == u.ID {
			return nil
		}
		return core.ErrUnauthorized
	}
	id := identity{userID: u.ID, clientID: "external/" + group}
	userN, total, _ := m.reservationCountsLocked(id)
	if n := UserStreamLimit(cfg, u); n > 0 && userN >= n {
		return core.ErrStreamLimit
	}
	if n := ServerStreamLimit(cfg, m.opts.ConnectionsPerStream); n > 0 && total >= n {
		return core.ErrStreamLimit
	}
	if m.externalGroups == nil {
		m.externalGroups = map[string]externalGroup{}
	}
	m.externalGroups[group] = externalGroup{user: *u, caps: caps}
	return nil
}
func (m *Manager) EndChannel(group string) {
	m.mu.Lock()
	delete(m.externalGroups, group)
	m.mu.Unlock()
}
