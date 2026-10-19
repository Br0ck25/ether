package usenet

import (
	"fmt"
	"sync"
	"time"
)

// Provider states reported in statistics.
const (
	StateOK       = "ok"
	StateBackoff  = "backoff"     // connection failures; retrying after a delay
	StateAuthFail = "auth_failed" // disabled until the provider's settings change
	StateClosed   = "closed"
)

// breaker is a per-provider circuit breaker. Authentication failures open it
// permanently (a new pool, created when settings change, starts closed).
// Connection failures open it for an exponentially growing backoff once
// failThreshold consecutive connections or commands have failed; a successful
// article/STAT response resets it. A successful login alone does not.
type breaker struct {
	mu            sync.Mutex
	now           func() time.Time
	minBackoff    time.Duration
	maxBackoff    time.Duration
	failThreshold int

	failures int
	until    time.Time
	authErr  error
	lastErr  error
	lastAt   time.Time
}

func newBreaker(minB, maxB time.Duration, threshold int, now func() time.Time) *breaker {
	if threshold < 1 {
		threshold = 1
	}
	return &breaker{now: now, minBackoff: minB, maxBackoff: maxB, failThreshold: threshold}
}

// allow returns nil if the provider may be used now.
func (b *breaker) allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.authErr != nil {
		return fmt.Errorf("%w: %v", ErrProviderDisabled, b.authErr)
	}
	if now := b.now(); now.Before(b.until) {
		return fmt.Errorf("%w for %s: %v", ErrProviderBackoff, b.until.Sub(now).Round(time.Millisecond), b.lastErr)
	}
	return nil
}

func (b *breaker) success() {
	b.mu.Lock()
	b.failures = 0
	b.until = time.Time{}
	b.mu.Unlock()
}

// connFailure records a failed dial and returns the backoff now in force.
func (b *breaker) connFailure(err error) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	b.lastErr, b.lastAt = err, b.now()
	if b.failures < b.failThreshold {
		return 0
	}
	d := b.minBackoff
	for i := b.failThreshold; i < b.failures && d < b.maxBackoff; i++ {
		d *= 2
	}
	if d > b.maxBackoff {
		d = b.maxBackoff
	}
	b.until = b.now().Add(d)
	return d
}

func (b *breaker) authFailure(err error) {
	b.mu.Lock()
	b.authErr = err
	b.lastErr, b.lastAt = err, b.now()
	b.mu.Unlock()
}

// noteError records a non-tripping error for statistics.
func (b *breaker) noteError(err error) {
	b.mu.Lock()
	b.lastErr, b.lastAt = err, b.now()
	b.mu.Unlock()
}

func (b *breaker) state() (state string, until time.Time, lastErr error, lastAt time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.authErr != nil:
		state = StateAuthFail
	case b.now().Before(b.until):
		state = StateBackoff
	default:
		state = StateOK
	}
	return state, b.until, b.lastErr, b.lastAt
}
