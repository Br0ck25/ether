package transcodebench

import (
	"bytes"
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/transcode"
)

// Launch is one recorded Runner invocation. PID/CPU are present only when
// the underlying Process implements the optional interfaces
// interface{ PID() int } / interface{ CPUTime() (time.Duration, time.Duration) };
// they are never invented when the API does not expose them.
type Launch struct {
	ID           int       `json:"id"`
	Kind         string    `json:"kind"` // "start" (long-running) or "run" (to completion)
	Path         string    `json:"path"`
	Args         []string  `json:"args"` // redacted
	StartedAt    time.Time `json:"startedAt"`
	DurationMs   int64     `json:"durationMs,omitempty"`
	ExitError    string    `json:"exitError,omitempty"`
	PID          int       `json:"pid,omitempty"`
	CPUUserMs    *int64    `json:"cpuUserMs,omitempty"`
	CPUSystemMs  *int64    `json:"cpuSystemMs,omitempty"`
	PeakRSSBytes *uint64   `json:"peakRssBytes,omitempty"` // individual encoder peak, not aggregate tree RSS
	Encoder      string    `json:"encoder,omitempty"`      // -c:v value, when present
	HWEncoder    bool      `json:"hwEncoder,omitempty"`
	LogTail      string    `json:"logTail,omitempty"`
}

const maxLaunchLog = 64 << 10

type tailBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.buf.Len() < maxLaunchLog {
		room := maxLaunchLog - t.buf.Len()
		if len(p) > room {
			t.buf.Write(p[:room])
		} else {
			t.buf.Write(p)
		}
	}
	return len(p), nil
}

func (t *tailBuf) tail() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.buf.String()
	if len(s) > 4000 {
		s = s[len(s)-4000:]
	}
	return s
}

type launchRec struct {
	launch Launch
	proc   transcode.Process
	buf    *tailBuf
	start  time.Time
	done   bool
}

// Recorder wraps a transcode.Runner (ExecRunner by default) and records
// every launch: redacted argv, timing, exit error, captured log tail, and
// PID/CPU when the process exposes them.
type Recorder struct {
	inner transcode.Runner
	mu    sync.Mutex
	next  int
	recs  []*launchRec
}

func NewRecorder() *Recorder { return &Recorder{inner: transcode.ExecRunner{}} }

// NewRecorderWith wraps a specific inner runner (tests).
func NewRecorderWith(inner transcode.Runner) *Recorder {
	if inner == nil {
		inner = transcode.ExecRunner{}
	}
	return &Recorder{inner: inner}
}

func (r *Recorder) add(kind, path string, args []string) *launchRec {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	rec := &launchRec{
		launch: Launch{ID: r.next, Kind: kind, Path: path, Args: RedactArgs(args),
			StartedAt: time.Now(), Encoder: VideoEncoderFromArgs(args)},
		buf:   &tailBuf{},
		start: time.Now(),
	}
	if rec.launch.Encoder != "" {
		rec.launch.HWEncoder = IsHWEncoder(rec.launch.Encoder)
	}
	r.recs = append(r.recs, rec)
	return rec
}

func tee(w io.Writer, buf *tailBuf) io.Writer {
	if w == nil {
		return buf
	}
	return io.MultiWriter(w, buf)
}

// Start implements transcode.Runner.
func (r *Recorder) Start(cmd transcode.Command) (transcode.Process, error) {
	rec := r.add("start", cmd.Path, cmd.Args)
	cmd.Stdout = tee(cmd.Stdout, rec.buf)
	cmd.Stderr = tee(cmd.Stderr, rec.buf)
	proc, err := r.inner.Start(cmd)
	if err != nil {
		rec.launch.ExitError = err.Error()
		rec.launch.DurationMs = time.Since(rec.start).Milliseconds()
		rec.done = true
		return nil, err
	}
	rec.proc = proc
	if p, ok := proc.(core.ProcessIdentity); ok {
		if pid := p.PID(); pid > 0 {
			rec.launch.PID = pid
		}
	}
	return &recProcess{Process: proc, rec: rec}, nil
}

// Run implements transcode.Runner.
func (r *Recorder) Run(ctx context.Context, cmd transcode.Command) error {
	rec := r.add("run", cmd.Path, cmd.Args)
	cmd.Stdout = tee(cmd.Stdout, rec.buf)
	cmd.Stderr = tee(cmd.Stderr, rec.buf)
	err := r.inner.Run(ctx, cmd)
	rec.launch.DurationMs = time.Since(rec.start).Milliseconds()
	if err != nil {
		rec.launch.ExitError = err.Error()
	}
	rec.launch.LogTail = rec.buf.tail()
	rec.done = true
	return err
}

type recProcess struct {
	transcode.Process
	rec *launchRec
}

func (p *recProcess) Stop() {
	p.Process.Stop()
	p.rec.finalize()
}

func (rec *launchRec) finalize() {
	if rec.done {
		return
	}
	// Only mark done once the process has actually exited.
	select {
	case <-rec.proc.Done():
	default:
		return
	}
	rec.done = true
	rec.launch.DurationMs = time.Since(rec.start).Milliseconds()
	if err := rec.proc.Err(); err != nil {
		rec.launch.ExitError = err.Error()
	}
	if rec.launch.PID == 0 {
		if p, ok := rec.proc.(core.ProcessIdentity); ok && p.PID() > 0 {
			rec.launch.PID = p.PID()
		}
	}
	if p, ok := rec.proc.(core.ProcessCPUTime); ok {
		user, sys := p.CPUTime()
		u, s := user.Milliseconds(), sys.Milliseconds()
		rec.launch.CPUUserMs = &u
		rec.launch.CPUSystemMs = &s
	}
	if p, ok := rec.proc.(core.ProcessPeakMemory); ok {
		if peak, available := p.PeakRSSBytes(); available {
			rec.launch.PeakRSSBytes = &peak
		}
	}
	rec.launch.LogTail = rec.buf.tail()
}

// Snapshot returns a copy of all launches, finalizing any exited processes.
func (r *Recorder) Snapshot() []Launch {
	r.mu.Lock()
	recs := append([]*launchRec(nil), r.recs...)
	r.mu.Unlock()
	out := make([]Launch, 0, len(recs))
	for _, rec := range recs {
		if rec.proc != nil {
			rec.finalize()
		}
		l := rec.launch
		if l.LogTail == "" {
			l.LogTail = rec.buf.tail()
		}
		if !rec.done && rec.proc != nil {
			l.DurationMs = time.Since(rec.start).Milliseconds()
		}
		out = append(out, l)
	}
	return out
}

// RedactArgs returns args with credentials removed: values following
// -headers/-user_agent/-referer, Authorization/token material anywhere,
// and sensitive URL query values. The -i input itself is kept (it is a
// loopback bench URL) except for sensitive query parameters.
func RedactArgs(args []string) []string {
	out := append([]string(nil), args...)
	for i := 0; i < len(out); i++ {
		a := out[i]
		switch a {
		case "-headers", "-user_agent", "-referer":
			if i+1 < len(out) {
				out[i+1] = "[redacted]"
				i++
			}
			continue
		}
		lower := strings.ToLower(a)
		if strings.Contains(lower, "authorization:") || strings.Contains(lower, "bearer ") {
			out[i] = "[redacted]"
			continue
		}
		out[i] = redactURLQuery(a)
		// key=value style secrets.
		if idx := strings.IndexByte(out[i], '='); idx > 0 {
			key := strings.ToLower(out[i][:idx])
			if key == "token" || key == "api_key" || key == "apikey" || key == "password" || key == "secret" {
				out[i] = out[i][:idx+1] + "[redacted]"
			}
		}
	}
	return out
}

func redactURLQuery(s string) string {
	i := strings.IndexByte(s, '?')
	if i < 0 {
		return s
	}
	base, query := s[:i], s[i+1:]
	parts := strings.Split(query, "&")
	for j, p := range parts {
		key, _, _ := strings.Cut(p, "=")
		switch strings.ToLower(key) {
		case "token", "api_key", "apikey", "key", "password", "secret", "signature", "x-plex-token":
			parts[j] = key + "=[redacted]"
		}
	}
	return base + "?" + strings.Join(parts, "&")
}

// VideoEncoderFromArgs returns the -c:v (or -c:v:0) encoder name, or "".
func VideoEncoderFromArgs(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-c:v" || args[i] == "-c:v:0" {
			return args[i+1]
		}
	}
	return ""
}

// IsHWEncoder reports whether an encoder name is a hardware encoder rather
// than a software libx* encoder or stream copy.
func IsHWEncoder(enc string) bool {
	switch enc {
	case "", "copy", "libx264", "libx265", "libxvid", "mpeg4":
		return false
	}
	for _, suf := range []string{"_nvenc", "_qsv", "_vaapi", "_videotoolbox", "_amf", "_v4l2m2m", "_mf"} {
		if strings.HasSuffix(enc, suf) {
			return true
		}
	}
	return false
}

// recordedVideoEncoder returns the encoder of the last long-running launch
// that has one (session ffmpeg runs, not probe/test Run calls).
func recordedVideoEncoder(ls []Launch) (enc string, hw bool, seen bool) {
	for _, l := range ls {
		if l.Kind == "start" && l.Encoder != "" {
			enc, hw, seen = l.Encoder, l.HWEncoder, true
		}
	}
	return enc, hw, seen
}

// softwareFallbackEncoders returns every distinct non-hardware encoder used
// by ANY long-running launch. Hardware verification must inspect all
// launches: with concurrent sessions one software session can otherwise be
// masked by a hardware session that happens to be recorded last.
func softwareFallbackEncoders(ls []Launch) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range ls {
		if l.Kind != "start" || l.Encoder == "" || l.HWEncoder {
			continue
		}
		if !seen[l.Encoder] {
			seen[l.Encoder] = true
			out = append(out, l.Encoder)
		}
	}
	return out
}
