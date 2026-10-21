package transcodebench

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// streamWriter is a bounded-memory http.ResponseWriter: the body is
// streamed to zero or more sinks (retained file, per-segment file, hash)
// and counted, never buffered whole. Only a small snippet is kept, and
// only so a non-200 response can be reported.
type streamWriter struct {
	header  http.Header
	status  int
	sinks   []io.Writer
	hash    hash.Hash
	count   int64
	err     error // first sink write error
	snippet []byte
}

func newStreamWriter(sinks ...io.Writer) *streamWriter {
	h := sha256.New()
	all := append([]io.Writer{h}, sinks...)
	return &streamWriter{header: make(http.Header), status: http.StatusOK, sinks: all, hash: h}
}

func (w *streamWriter) Header() http.Header { return w.header }

func (w *streamWriter) WriteHeader(code int) { w.status = code }

func (w *streamWriter) Write(p []byte) (int, error) {
	if len(w.snippet) < 512 {
		room := 512 - len(w.snippet)
		if len(p) > room {
			w.snippet = append(w.snippet, p[:room]...)
		} else {
			w.snippet = append(w.snippet, p...)
		}
	}
	for _, s := range w.sinks {
		if _, err := s.Write(p); err != nil && w.err == nil {
			w.err = err
		}
	}
	w.count += int64(len(p))
	if w.err != nil {
		return len(p), w.err
	}
	return len(p), nil
}

func (w *streamWriter) sum() string {
	if w.hash == nil {
		return ""
	}
	return hex.EncodeToString(w.hash.Sum(nil))
}

// captureWriter buffers a body up to a cap (playlists are small). Used
// where the body itself must be parsed (EXTINF durations).
type captureWriter struct {
	header http.Header
	status int
	buf    []byte
	cap    int
	count  int64
}

func newCaptureWriter(capBytes int) *captureWriter {
	return &captureWriter{header: make(http.Header), status: http.StatusOK, cap: capBytes}
}

func (w *captureWriter) Header() http.Header { return w.header }
func (w *captureWriter) WriteHeader(c int)   { w.status = c }
func (w *captureWriter) Write(p []byte) (int, error) {
	w.count += int64(len(p))
	if room := w.cap - len(w.buf); room > 0 {
		if len(p) > room {
			w.buf = append(w.buf, p[:room]...)
		} else {
			w.buf = append(w.buf, p...)
		}
	}
	return len(p), nil
}

// parsePlaylistDurations returns the EXTINF duration (seconds) of each
// media segment in an HLS playlist, in order. Pure; unit-tested.
func parsePlaylistDurations(body []byte) []float64 {
	var out []float64
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#EXTINF:") {
			continue
		}
		v := strings.TrimSuffix(strings.TrimPrefix(line, "#EXTINF:"), ",")
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			out = append(out, f)
		}
	}
	return out
}

// deliveredSeconds sums the actual playlist durations of the first n
// segments. When the playlist has no entry for a segment it falls back to
// the nominal segment duration, capped at the requested total — it never
// invents more media than was requested or than the playlist describes
// for a shortened final segment.
func deliveredSeconds(durs []float64, n int, segDur float64, requested float64) float64 {
	var total float64
	for i := 0; i < n; i++ {
		if i < len(durs) {
			total += durs[i]
		} else {
			total += segDur
		}
	}
	if requested > 0 && total > requested {
		total = requested
	}
	return total
}
