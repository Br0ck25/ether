package admin

import (
	"net"
	"net/http"
	"sync"
	"time"
)

type authBucket struct {
	count int
	until time.Time
}
type authLimiter struct {
	mu    sync.Mutex
	peers map[string]authBucket
}

func newAuthLimiter() *authLimiter { return &authLimiter{peers: map[string]authBucket{}} }
func (l *authLimiter) allow(peer string, now time.Time) bool {
	host, _, err := net.SplitHostPort(peer)
	if err == nil {
		peer = host
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, b := range l.peers {
		if !now.Before(b.until) {
			delete(l.peers, k)
		}
	}
	b, ok := l.peers[peer]
	if !ok {
		if len(l.peers) >= 4096 {
			return false
		}
		b.until = now.Add(time.Minute)
	}
	b.count++
	l.peers[peer] = b
	return b.count <= 30
}
func (h *Handler) limitAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.authLimit.allow(r.RemoteAddr, time.Now()) {
			w.Header().Set("Retry-After", "60")
			writeError(w, 429, "rate_limited", "Too many sign-in requests. Try again in a minute.")
			return
		}
		next(w, r)
	}
}
