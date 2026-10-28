// Package retryafter parses HTTP Retry-After values.
package retryafter

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Parse reads a Retry-After value, either delay-seconds or an HTTP-date,
// relative to now. It reports false for an empty, malformed or negative
// value, and for a date that is not in the future (usually clock skew), so
// callers apply their own default. A positive limit caps the delay: a huge
// value neither overflows nor pauses the caller for longer than it allows.
func Parse(v string, now time.Time, limit time.Duration) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	var d time.Duration
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 {
			return 0, false
		}
		d = time.Duration(min(n, int64(math.MaxInt64/time.Second))) * time.Second
	} else if t, err := http.ParseTime(v); err == nil && t.After(now) {
		d = t.Sub(now)
	} else {
		return 0, false
	}
	if limit > 0 {
		d = min(d, limit)
	}
	return d, true
}
