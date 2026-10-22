package core

import (
	"context"
	"time"
)

// RepairEvent reports verified damage and bounded recovery work. Offset is in
// the selected media file, never an NNTP article or archive-volume coordinate.
// Estimate is conservative, not a guarantee; zero means not yet known.
type RepairEvent struct {
	Key            int
	Attempt        uint64 // distinguishes concurrent readers repairing one article
	Offset         int64
	Approximate    bool
	Phase          string // missing, planning, repairing, repaired, failed, obsolete
	Estimate       time.Duration
	RemainingBytes int64
}

// RepairWindow is playback's current useful repair lifetime. A zero deadline
// leaves the source's ordinary bound in force. Stop cancels obsolete work.
type RepairWindow struct {
	Deadline       time.Time
	Stop           bool
	ReadAheadBytes int64
}

type RepairReporter interface {
	SetRepairHandler(func(RepairEvent) RepairWindow)
}

// RecoveryBuffer reports contiguous, published media, not the encoder's input
// offset. It deliberately does not claim that bytes reached the client.
type RecoveryBuffer struct {
	Position, ReadyUntil time.Duration
	SeekAt               time.Time // a discontinuous output request invalidates older telemetry
}
type RecoveryBufferSource interface{ RecoveryBuffer() RecoveryBuffer }

// BufferedInputSwapper preserves published media before starting replacement
// input at the contiguous buffer boundary. Only equal timelines qualify.
type BufferedInputSwapper interface {
	SwapInputBuffered(context.Context, string, MediaInfo, time.Duration) error
}

// PlaybackTimeline carries actual client position without changing history.
type PlaybackTimeline interface {
	PlaybackProgress(userID int64, clientID string, key RatingKey, position time.Duration, state string)
}
