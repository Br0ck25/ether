package transcode

import (
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// goodputEstimator tracks conservative completed delivery evidence, not a
// measurement of the client's playback buffer or end-to-end link capacity.
// Decreases apply immediately; recovery needs sustained backpressured samples.
type goodputEstimator struct {
	mu       sync.Mutex
	ewmaBps  float64
	samples  int
	last     time.Time
	recovery int
	clock    func() time.Time // fixed before use; nil uses the real clock
}

const (
	goodputMinBytes = 32 << 10
	// Sub-100ms writes commonly measure kernel/proxy buffering, not delivery.
	goodputMinDur        = 100 * time.Millisecond
	goodputRecoveryAlpha = 0.10
	goodputMaxAge        = 30 * time.Second
)

func (g *goodputEstimator) now() time.Time {
	if g.clock != nil {
		return g.clock()
	}
	return time.Now()
}

func (g *goodputEstimator) add(bytes int64, dur time.Duration) {
	if bytes < goodputMinBytes || dur < goodputMinDur {
		return
	}
	bps := float64(bytes) / dur.Seconds()
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if g.samples == 0 || now.Sub(g.last) > goodputMaxAge {
		g.ewmaBps, g.recovery, g.samples = bps, 0, 0
	} else if bps < g.ewmaBps {
		g.ewmaBps, g.recovery = bps, 0
	} else {
		g.recovery++
		if g.recovery >= 3 {
			g.ewmaBps += goodputRecoveryAlpha * (bps - g.ewmaBps)
		}
	}
	g.samples++
	g.last = now
}

// Kbps expires stale evidence rather than steering a new run with old data.
func (g *goodputEstimator) Kbps() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.samples == 0 || g.now().Sub(g.last) > goodputMaxAge {
		return 0
	}
	return int(g.ewmaBps * 8 / 1000)
}

func (g *goodputEstimator) Samples() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.samples
}

// Local writes cannot establish internet goodput. Forwarded headers are not
// trusted here: reverse-proxy buffering also hides actual client delivery.
func remoteDelivery(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && !ip.IsLoopback() && !ip.IsUnspecified()
}

// GoodputKbps is the session's measured client delivery goodput.
func (s *Session) GoodputKbps() int { return s.goodput.Kbps() }

// countingWriter wraps a ResponseWriter counting body bytes, so serveFile
// can time the write phase of a segment delivery.
type countingWriter struct {
	http.ResponseWriter
	bytes  int64
	status int
	err    error
}

func (w *countingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *countingWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *countingWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	if err != nil {
		w.err = err
	}
	return n, err
}

// ReadFrom keeps sendfile-style fast paths where the underlying writer
// has one, counting bytes actually accepted by the writer.
func (w *countingWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	var n int64
	var err error
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err = rf.ReadFrom(r)
	} else {
		n, err = io.Copy(struct{ io.Writer }{w.ResponseWriter}, r)
	}
	w.bytes += n // bytes accepted by Write, not bytes read before a failed write
	if err != nil {
		w.err = err
	}
	return n, err
}

// Flush forwards to the underlying writer when it can flush.
func (w *countingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
