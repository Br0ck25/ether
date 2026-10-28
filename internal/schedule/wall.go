// Package schedule provides wall-clock deadlines for periodic background work.
// Playback timeouts and rate limiting should continue to use elapsed time.
package schedule

import (
	"context"
	"time"
)

// MaxWait bounds how long a suspended machine can take to notice overdue work
// after resuming, even where monotonic timers exclude time spent asleep.
const MaxWait = time.Minute

// Due checks a persisted last-attempt timestamp. A future timestamp from a
// corrected clock gets one new attempt; the caller records the current time.
func Due(now, last time.Time, interval time.Duration) bool {
	now, last = now.Round(0), last.Round(0)
	return last.IsZero() || now.Before(last) || now.Sub(last) >= interval
}

// Deadline is owned by a single scheduler goroutine. Reset after a completed
// attempt coalesces missed intervals instead of replaying a backlog.
type Deadline struct {
	start, at time.Time
	interval  time.Duration
}

func New(now time.Time, interval time.Duration) Deadline {
	now = now.Round(0)
	interval = max(interval, 0)
	return Deadline{now, now.Add(interval), interval}
}

// Remaining uses wall time. A backwards clock correction rebases the deadline
// once, limiting postponement to one interval without creating a retry storm.
func (d *Deadline) Remaining(now time.Time) time.Duration {
	now = now.Round(0)
	if now.Before(d.start) {
		*d = New(now, d.interval)
	}
	return max(d.at.Sub(now), 0)
}

// Wait waits one interval with bounded wall-clock rechecks. It owns no goroutine
// and returns false on cancellation, including when already overdue.
func Wait(ctx context.Context, now func() time.Time, interval time.Duration) bool {
	return wait(ctx, now, interval, pause)
}

func wait(ctx context.Context, now func() time.Time, interval time.Duration, sleep func(context.Context, time.Duration) bool) bool {
	d := New(now(), interval)
	for ctx.Err() == nil {
		left := d.Remaining(now())
		if left == 0 {
			return true
		}
		if !sleep(ctx, min(left, MaxWait)) {
			return false
		}
	}
	return false
}

func pause(ctx context.Context, delay time.Duration) bool {
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return ctx.Err() == nil
	}
}
