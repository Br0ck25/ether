// Package faillog logs a repeating background failure (a heartbeat or sync
// loop that cannot reach the database) without flooding the log: the first
// failure is logged at WARN, later ones are summarised at most once per
// interval, and the recovery is logged once at INFO.
package faillog

import (
	"log/slog"
	"sync"
	"time"
)

// DefaultEvery is how often a continuing failure is summarised.
const DefaultEvery = time.Minute

// Tracker follows one repeating operation. The zero value is not usable;
// call New. Methods are safe for concurrent use and a nil *Tracker ignores
// every call.
type Tracker struct {
	log  *slog.Logger
	what string
	// Every is the summary interval (DefaultEvery); Now the clock. Both
	// may be changed before first use (tests).
	Every time.Duration
	Now   func() time.Time

	mu       sync.Mutex
	failing  bool
	since    time.Time
	attempts int // failures since the last summary
	total    int // failures since the outage began
	lastLog  time.Time
}

// New tracks the operation described by what ("cluster: heartbeat").
func New(log *slog.Logger, what string) *Tracker {
	if log == nil {
		log = slog.Default()
	}
	return &Tracker{log: log, what: what, Every: DefaultEvery, Now: time.Now}
}

// Fail records a failed attempt: the first of an outage is logged at WARN,
// later ones are summarised at most once per Every.
func (t *Tracker) Fail(err error, attrs ...any) {
	if t == nil {
		return
	}
	t.mu.Lock()
	now := t.Now()
	t.total++
	t.attempts++
	if !t.failing {
		t.failing, t.since, t.lastLog, t.attempts = true, now, now, 0
		t.mu.Unlock()
		t.log.Warn(t.what+" failed", append([]any{"err", err}, attrs...)...)
		return
	}
	if now.Sub(t.lastLog) < t.Every {
		t.mu.Unlock()
		return
	}
	n, since := t.attempts, t.since
	t.lastLog, t.attempts = now, 0
	t.mu.Unlock()
	t.log.Warn(t.what+" still failing", append([]any{"err", err, "attempts", n,
		"since", since.Format(time.RFC3339), "for", now.Sub(since).Round(time.Second)}, attrs...)...)
}

// OK records a successful attempt; after a failure it logs the recovery
// once at INFO.
func (t *Tracker) OK() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if !t.failing {
		t.mu.Unlock()
		return
	}
	now := t.Now()
	since, total := t.since, t.total
	t.failing, t.attempts, t.total = false, 0, 0
	t.mu.Unlock()
	t.log.Info(t.what+" recovered", "after", now.Sub(since).Round(100*time.Millisecond), "failures", total)
}

// Failing reports whether the operation is currently failing, and since
// when.
func (t *Tracker) Failing() (bool, time.Time) {
	if t == nil {
		return false, time.Time{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.failing, t.since
}
