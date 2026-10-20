package playback

import (
	"net/http"
	"time"
)

// stallWriter arms a fresh write deadline on the underlying connection
// before every Write. The servers deliberately have no WriteTimeout (a
// direct-play response can last hours), so without this a client that
// stops reading blocks Write forever, pinning the generation's reference
// and with it the Usenet source. With it a stalled client errors out after
// timeout of no progress, while a slow but moving one is never cut off.
type stallWriter struct {
	http.ResponseWriter
	rc      *http.ResponseController
	timeout time.Duration
}

// newStallWriter wraps w; timeout <= 0 returns w unchanged.
func newStallWriter(w http.ResponseWriter, timeout time.Duration) http.ResponseWriter {
	if timeout <= 0 {
		return w
	}
	return &stallWriter{ResponseWriter: w, rc: http.NewResponseController(w), timeout: timeout}
}

func (w *stallWriter) Write(p []byte) (int, error) {
	// ErrNotSupported (e.g. a test recorder) leaves the write unguarded.
	_ = w.rc.SetWriteDeadline(time.Now().Add(w.timeout))
	return w.ResponseWriter.Write(p)
}

func (w *stallWriter) Flush() {
	_ = w.rc.SetWriteDeadline(time.Now().Add(w.timeout))
	_ = w.rc.Flush()
}

// Unwrap lets http.ResponseController reach the real response.
func (w *stallWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
