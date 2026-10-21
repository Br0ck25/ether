package playback

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// ObservedMedia is historical evidence, not a promise about the next selected release.
// No provider, probe, source opening or keyframe loading is performed by this lookup.
type ObservedMedia struct {
	Info core.MediaInfo
	At   time.Time
}

// ObservedMediaInfo returns this viewer's most recent exact probe only while it
// still matches their current resume release, payload hash and media size. Facts
// expire after 30 days and never cross viewer boundaries.
func (m *Manager) ObservedMediaInfo(ctx context.Context, userID int64, key core.RatingKey) (*ObservedMedia, error) {
	if userID <= 0 {
		return nil, nil
	}
	// Avoid allocating/sorting full public session snapshots for every poster.
	m.mu.Lock()
	var matching []*session
	for _, session := range m.sessions {
		if session.ident.userID == userID && session.ident.key == key {
			matching = append(matching, session)
		}
	}
	m.mu.Unlock()
	var latest *ObservedMedia
	for _, session := range matching {
		session.mu.Lock()
		g, closed, started := session.cur, session.closed, session.startedAt
		session.mu.Unlock()
		if g == nil || closed {
			continue
		}
		info, ok := persistedProbeFacts(g.infoSnapshot())
		if ok && (latest == nil || started.After(latest.At)) {
			info.Chapters = nil
			latest = &ObservedMedia{Info: info, At: started}
		}
	}
	if latest != nil {
		return latest, nil
	}
	if m.resume == nil {
		return nil, nil
	}
	return m.resume.observedMedia(ctx, userID, key, m.now())
}

func (s *resumeStore) observedMedia(ctx context.Context, userID int64, key core.RatingKey, now time.Time) (*ObservedMedia, error) {
	var raw string
	var size, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT p.info_json,p.size,p.updated_at FROM playback_probe p
 JOIN playback_resume r ON r.user_id=p.user_id AND r.rating_key=p.rating_key
 AND r.release_id=p.release_id AND r.nzb_hash=p.nzb_hash AND r.size=p.size
 WHERE p.user_id=? AND p.rating_key=? AND p.nzb_hash<>'' AND p.updated_at BETWEEN ? AND ?`,
		userID, key, now.Add(-30*24*time.Hour).UnixMilli(), now.UnixMilli()).Scan(&raw, &size, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > maxPersistedProbeBytes {
		return nil, nil
	}
	var info core.MediaInfo
	if json.Unmarshal([]byte(raw), &info) != nil {
		return nil, nil
	}
	facts, ok := persistedProbeFacts(info)
	if !ok || facts.Size != size {
		return nil, nil
	}
	// Artwork consumers only need stream facts, never chapters or external paths.
	facts.Chapters = nil
	return &ObservedMedia{Info: facts, At: time.UnixMilli(updated)}, nil
}
