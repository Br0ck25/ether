package playback

import (
	"context"
	"time"

	"github.com/ether-media/ether/internal/core"
)

// pinLive protects a release served by a live session. Pins are
// reference-counted here so several sessions on one release share a pin.
func (m *Manager) pinLive(releaseID string) {
	if m.deps.Cache == nil || releaseID == "" {
		return
	}
	m.pinMu.Lock()
	defer m.pinMu.Unlock()
	m.livePins[releaseID]++
	if m.livePins[releaseID] == 1 {
		if err := m.deps.Cache.Pin(releaseID, PinReasonLive); err != nil {
			m.log.Warn("cache pin failed", "release", releaseID, "err", err)
		}
	}
}

func (m *Manager) unpinLive(releaseID string) {
	if m.deps.Cache == nil || releaseID == "" {
		return
	}
	m.pinMu.Lock()
	defer m.pinMu.Unlock()
	n, ok := m.livePins[releaseID]
	if !ok {
		return
	}
	if n > 1 {
		m.livePins[releaseID] = n - 1
		return
	}
	delete(m.livePins, releaseID)
	if err := m.deps.Cache.Unpin(releaseID, PinReasonLive); err != nil {
		m.log.Warn("cache unpin failed", "release", releaseID, "err", err)
	}
}

// kickPins asks the pin loop to reconcile soon.
func (m *Manager) kickPins() {
	select {
	case m.pinKick <- struct{}{}:
	default:
	}
}

func (m *Manager) pinLoop() {
	if m.deps.Cache == nil || m.resume == nil {
		<-m.ctx.Done()
		return
	}
	m.reconcileAndLog()
	t := time.NewTicker(m.opts.PinInterval)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
		case <-m.pinKick:
			// Debounce bursts of session ends.
			select {
			case <-m.ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
		m.reconcileAndLog()
	}
}

func (m *Manager) reconcileAndLog() {
	ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
	defer cancel()
	if err := m.ReconcilePins(ctx); err != nil && m.ctx.Err() == nil {
		m.log.Warn("pin reconcile failed", "err", err)
	}
}

// ReconcilePins makes the cache's in-progress pins match the resume
// records of titles in progress for any user: each such user's release is
// pinned, releases no longer in progress are unpinned, and records past
// Options.ResumeRetention that are neither in progress nor live are
// deleted. The first pass also unpins stale pins from a previous run.
func (m *Manager) ReconcilePins(ctx context.Context) error {
	if m.deps.Cache == nil || m.resume == nil {
		return nil
	}
	recs, err := m.resume.all(ctx)
	if err != nil {
		return err
	}
	inProgress := map[core.RatingKey]bool{}
	if m.deps.State != nil {
		keys, err := m.deps.State.InProgressAny(ctx)
		if err != nil {
			return err
		}
		for _, k := range keys {
			inProgress[k] = true
		}
	}
	live := map[identity]bool{}
	liveRel := map[string]bool{}
	m.mu.Lock()
	for _, s := range m.sessions {
		live[identity{userID: s.ident.userID, key: s.ident.key}] = true
		if g := s.current(); g != nil {
			liveRel[g.releaseID()] = true
		}
	}
	m.mu.Unlock()

	now := m.now()
	desired := map[string]bool{}
	var expired []resumeRecord
	for _, rec := range recs {
		progress := false
		if inProgress[rec.key] {
			st, err := m.deps.State.Get(ctx, rec.userID, rec.key)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// Unknown: keep the pin rather than risk evicting a resume.
				progress = true
			} else {
				progress = st.ViewOffset > 0
			}
		}
		if progress {
			desired[rec.releaseID] = true
			continue
		}
		if !live[identity{userID: rec.userID, key: rec.key}] && now.Sub(rec.updatedAt) > m.opts.ResumeRetention {
			expired = append(expired, rec)
		}
	}

	m.pinMu.Lock()
	for id := range desired {
		if !m.progressPins[id] {
			if err := m.deps.Cache.Pin(id, PinReasonInProgress); err != nil {
				m.log.Warn("cache pin failed", "release", id, "err", err)
				continue
			}
			m.progressPins[id] = true
		}
	}
	for id := range m.progressPins {
		if !desired[id] {
			if err := m.deps.Cache.Unpin(id, PinReasonInProgress); err != nil {
				m.log.Warn("cache unpin failed", "release", id, "err", err)
				continue
			}
			delete(m.progressPins, id)
		}
	}
	if !m.pinsSwept {
		// Pins may outlive the process; drop ours that are no longer wanted.
		for _, rec := range recs {
			if !desired[rec.releaseID] {
				_ = m.deps.Cache.Unpin(rec.releaseID, PinReasonInProgress)
			}
			if !liveRel[rec.releaseID] && m.livePins[rec.releaseID] == 0 {
				_ = m.deps.Cache.Unpin(rec.releaseID, PinReasonLive)
			}
		}
		m.pinsSwept = true
	}
	m.pinMu.Unlock()

	for _, rec := range expired {
		if err := m.resume.delete(ctx, rec.userID, rec.key); err != nil {
			return err
		}
	}
	return nil
}
