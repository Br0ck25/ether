package transcode

import (
	"io"
	"net/http"
	"sync"
	"time"
)

// goodputEstimator is a per-session rolling estimate of segment delivery
// throughput: bytes written to the client divided by the wall time spent
// writing them (never the time spent waiting for ffmpeg). An EWMA over
// segment-sized samples keeps it robust to one slow write while still
// tracking a degrading link within a few segments.
type goodputEstimator struct {
	mu      sync.Mutex
	ewmaBps float64 // bytes per second
	samples int
	last    time.Time
}

const (
	goodputMinBytes = 32 << 10 // ignore tiny writes (init segments, ranges)
	goodputMinDur   = 2 * time.Millisecond
	goodputAlpha    = 0.35
)

// add records one completed segment write.
func (g *goodputEstimator) add(bytes int64, dur time.Duration) {
	if bytes < goodputMinBytes || dur < goodputMinDur {
		return
	}
	bps := float64(bytes) / dur.Seconds()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.samples == 0 {
		g.ewmaBps = bps
	} else {
		g.ewmaBps = goodputAlpha*bps + (1-goodputAlpha)*g.ewmaBps
	}
	g.samples++
	g.last = time.Now()
}

// Kbps is the current estimate in kbit/s; 0 until a usable sample exists.
func (g *goodputEstimator) Kbps() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.samples == 0 {
		return 0
	}
	return int(g.ewmaBps * 8 / 1000)
}

// Samples is how many writes fed the estimate.
func (g *goodputEstimator) Samples() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.samples
}

// GoodputKbps is the session's measured client delivery goodput.
func (s *Session) GoodputKbps() int { return s.goodput.Kbps() }

// countingWriter wraps a ResponseWriter counting body bytes, so serveFile
// can time the write phase of a segment delivery.
type countingWriter struct {
	http.ResponseWriter
	bytes int64
}

func (w *countingWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// ReadFrom keeps sendfile-style fast paths where the underlying writer
// has one, counting through a wrapping reader.
func (w *countingWriter) ReadFrom(r io.Reader) (int64, error) {
	cr := &countingReader{r: r}
	var n int64
	var err error
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err = rf.ReadFrom(cr)
	} else {
		n, err = io.Copy(struct{ io.Writer }{w.ResponseWriter}, cr)
	}
	w.bytes += cr.n
	return n, err
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	return n, err
}

// Flush forwards to the underlying writer when it can flush.
func (w *countingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
