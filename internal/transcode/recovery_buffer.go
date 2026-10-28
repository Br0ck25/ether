package transcode

import "github.com/braedonsaunders/ether/internal/core"

var (
	_ core.RecoveryBufferSource = (*Session)(nil)
	_ core.BufferedInputSwapper = (*Session)(nil)
)

func (s *Session) RecoveryBuffer() core.RecoveryBuffer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recoveryBufferLocked()
}

func (s *Session) recoveryBufferLocked() core.RecoveryBuffer {
	start := max(0, s.lastReq)
	b := core.RecoveryBuffer{Position: s.segStart(start), ReadyUntil: s.segStart(start), SeekAt: s.recoverySeekAt}
	if s.closed || len(s.done) == 0 {
		return b
	}
	for n := start; n < start+512; n++ {
		if s.total > 0 && n >= s.total {
			break
		}
		track := min(max(0, s.wantTrack), len(s.done)-1)
		ready := s.done[track][n]
		// Inactive video representations are not playback dependencies.
		if audio := s.audioTrackLocked(); audio >= 0 && audio < len(s.done) {
			ready = ready && s.done[audio][n]
		}
		if !ready {
			break
		}
		b.ReadyUntil = s.segStart(n + 1)
	}
	track := min(max(0, s.wantTrack), len(s.done)-1)
	for n := start; n < start+512 && (s.total <= 0 || n < s.total); n++ {
		if !s.completedDeliveries[track][n] {
			break
		}
		if audio := s.audioTrackLocked(); audio >= 0 && audio < len(s.done) && !s.completedDeliveries[audio][n] {
			break
		}
		b.DeliveredUntil = s.segStart(n + 1)
	}
	return b
}
