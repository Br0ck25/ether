package transcode

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Progress is the latest `-progress` report of a session's ffmpeg run.
type Progress struct {
	Frame   int64         `json:"frame"`
	FPS     float64       `json:"fps"`
	Speed   float64       `json:"speed"`   // x realtime
	OutTime time.Duration `json:"outTime"` // media time reached
	Bitrate string        `json:"bitrate,omitempty"`
	Ended   bool          `json:"ended"`
	Updated time.Time     `json:"updated"`
}

// progressParser accumulates ffmpeg's key=value progress blocks; each block
// ends with progress=continue|end.
type progressParser struct {
	cur Progress
}

// line feeds one line; it returns a complete report at block end.
func (p *progressParser) line(l string) (Progress, bool) {
	k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
	if !ok {
		return Progress{}, false
	}
	v = strings.TrimSpace(v)
	switch k {
	case "frame":
		p.cur.Frame, _ = strconv.ParseInt(v, 10, 64)
	case "fps":
		p.cur.FPS, _ = strconv.ParseFloat(v, 64)
	case "bitrate":
		if v != "N/A" {
			p.cur.Bitrate = v
		}
	case "out_time_us", "out_time_ms": // both are microseconds in ffmpeg
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			p.cur.OutTime = time.Duration(n) * time.Microsecond
		}
	case "speed":
		if f, err := strconv.ParseFloat(strings.TrimSuffix(v, "x"), 64); err == nil {
			p.cur.Speed = f
		}
	case "progress":
		p.cur.Ended = v == "end"
		p.cur.Updated = time.Now()
		out := p.cur
		return out, true
	}
	return Progress{}, false
}

// lineWriter splits written bytes into lines for fn. Safe for one writer
// goroutine at a time (how os/exec copies a pipe).
type lineWriter struct {
	mu  sync.Mutex
	buf []byte
	fn  func(string)
}

func (w *lineWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, b...)
	for {
		i := bytes.IndexAny(w.buf, "\r\n")
		if i < 0 {
			break
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if strings.TrimSpace(line) != "" {
			w.fn(line)
		}
	}
	if len(w.buf) > 64<<10 { // runaway line
		w.fn(string(w.buf))
		w.buf = w.buf[:0]
	}
	return len(b), nil
}

// tailBuffer keeps the last n lines (ffmpeg stderr for error reports).
type tailBuffer struct {
	mu    sync.Mutex
	n     int
	lines []string
}

func newTailBuffer(n int) *tailBuffer { return &tailBuffer{n: n} }

func (t *tailBuffer) add(l string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, l)
	if len(t.lines) > t.n {
		t.lines = t.lines[len(t.lines)-t.n:]
	}
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}
