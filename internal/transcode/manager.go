// Package transcode wraps Ether's bundled ffmpeg: discovery, hardware
// encoder detection, probing, the direct play / remux / transcode decision
// and HLS and MPEG-DASH sessions. Manager implements core.Transcoder.
package transcode

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

// ErrTooManyTranscodes is returned by StartHLS when config MaxTranscodes
// video transcodes are already running. Remuxes are not counted.
var ErrTooManyTranscodes = errors.New("transcode: too many concurrent transcodes")

// ErrImageSubtitle is returned when a bitmap subtitle is asked for as text.
var ErrImageSubtitle = errors.New("transcode: bitmap subtitles cannot be converted to text")

// Option tunes a Manager.
type Option func(*options)

type options struct {
	runner         Runner
	finder         *finder
	detectHW       bool
	idleTimeout    time.Duration
	segmentTimeout time.Duration
	restartAhead   time.Duration
	maxAhead       time.Duration
	keepBehind     time.Duration
	pollInterval   time.Duration
	segmentURI     func(n int, container string) string
	dashURLs       DASHURLs
	manifestWait   time.Duration
	onFailure      func(sessionID string, at time.Duration, err error)
}

func defaultOptions() options {
	return options{
		runner:         ExecRunner{},
		detectHW:       true,
		idleTimeout:    2 * time.Minute,
		segmentTimeout: 45 * time.Second,
		restartAhead:   24 * time.Second,
		maxAhead:       2 * time.Minute,
		keepBehind:     3 * time.Minute,
		pollInterval:   150 * time.Millisecond,
		segmentURI:     SegmentName,
		dashURLs:       PlexDASHURLs,
		manifestWait:   15 * time.Second,
	}
}

// WithRunner replaces the process runner (tests).
func WithRunner(r Runner) Option { return func(o *options) { o.runner = r } }

// WithoutHWDetection skips the startup hardware probe and encodes on CPU.
func WithoutHWDetection() Option { return func(o *options) { o.detectHW = false } }

// WithIdleTimeout sets how long an unrequested session lives (default 2m).
func WithIdleTimeout(d time.Duration) Option { return func(o *options) { o.idleTimeout = d } }

// WithSegmentTimeout bounds how long ServeSegment waits for ffmpeg
// (default 45s).
func WithSegmentTimeout(d time.Duration) Option { return func(o *options) { o.segmentTimeout = d } }

// WithBuffering tunes seeking and disk use: a request more than
// restartAhead past the encoder restarts ffmpeg there (default 24s); ffmpeg
// is suspended when maxAhead in front of the player (default 2m, 0 = never);
// segments more than keepBehind behind the player are deleted (default 3m,
// 0 = never).
func WithBuffering(restartAhead, maxAhead, keepBehind time.Duration) Option {
	return func(o *options) {
		o.restartAhead, o.maxAhead, o.keepBehind = restartAhead, maxAhead, keepBehind
	}
}

// WithSegmentURI sets the segment URIs written into media playlists. The
// default is SegmentName ("00042.ts", relative to index.m3u8).
func WithSegmentURI(f func(n int, container string) string) Option {
	return func(o *options) {
		if f != nil {
			o.segmentURI = f
		}
	}
}

// WithDASHURLs sets the segment URL templates written into DASH MPDs. The
// default is PlexDASHURLs, the layout of real PMS.
func WithDASHURLs(f DASHURLs) Option {
	return func(o *options) {
		if f != nil {
			o.dashURLs = f
		}
	}
}

// WithManifestWait bounds how long a DASH session's first MPD request waits
// for ffmpeg's init segments to learn the exact codecs (default 15s); after
// that the MPD is served with codecs estimated from the probe.
func WithManifestWait(d time.Duration) Option { return func(o *options) { o.manifestWait = d } }

// WithFailureHandler is called (in its own goroutine) when ffmpeg fails
// mid-session, e.g. because the source lost articles; at is the media time
// playback had reached. Playback typically answers with SwapInput.
func WithFailureHandler(f func(sessionID string, at time.Duration, err error)) Option {
	return func(o *options) { o.onFailure = f }
}

func withFinder(f finder) Option { return func(o *options) { o.finder = &f } }

// Manager runs ffmpeg for Ether. It implements core.Transcoder.
type Manager struct {
	log     *slog.Logger
	opts    options
	runner  Runner
	ffmpeg  string
	ffprobe string
	version string
	hw      core.HWAccel
	feat    features
	tempDir string

	mu       sync.Mutex
	cfg      config.Transcode
	sessions map[string]*Session
	closed   bool

	stop chan struct{}
	wg   sync.WaitGroup
}

var _ core.Transcoder = (*Manager)(nil)
var _ core.HLSSession = (*Session)(nil)
var _ core.DASHSession = (*Session)(nil)

// New finds ffmpeg/ffprobe, detects hardware encoders and starts the idle
// session reaper. It fails with ErrFFmpegNotFound when no ffmpeg exists.
func New(cfg config.Transcode, logger *slog.Logger, opts ...Option) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}
	o := defaultOptions()
	for _, fn := range opts {
		fn(&o)
	}
	m := &Manager{
		log:      logger.With("component", "transcode"),
		opts:     o,
		runner:   o.runner,
		cfg:      cfg,
		sessions: map[string]*Session{},
		stop:     make(chan struct{}),
	}
	f := defaultFinder()
	if o.finder != nil {
		f = *o.finder
	}
	var err error
	m.ffmpeg, m.ffprobe, err = f.discover(cfg.FFmpegPath, cfg.FFprobePath)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if m.version, err = toolVersion(ctx, m.runner, m.ffmpeg); err != nil {
		return nil, err
	}
	if m.feat, err = probeFeatures(ctx, m.runner, m.ffmpeg); err != nil {
		return nil, err
	}
	if !m.feat.hasEncoder("libx264") {
		m.log.Warn("ffmpeg has no libx264; CPU transcoding unavailable", "ffmpeg", m.ffmpeg)
	}
	mode := cfg.HWAccel
	if !o.detectHW {
		mode = AccelNone
	}
	m.hw = hwDetector{runner: m.runner, ffmpeg: m.ffmpeg, goos: f.goos, log: m.log, statDevice: deviceExists}.
		detect(ctx, mode, cfg.VAAPIDevice, m.feat)

	m.tempDir = cfg.TempDir
	if m.tempDir == "" {
		m.tempDir = filepath.Join(os.TempDir(), "ether-transcode")
	}
	if err := os.MkdirAll(m.tempDir, 0o755); err != nil {
		return nil, fmt.Errorf("transcode: temp dir: %w", err)
	}
	m.cleanStale()
	m.log.Info("transcoder ready", "ffmpeg", m.ffmpeg, "ffprobe", m.ffprobe, "version", m.version,
		"hwaccel", m.hw.Name, "encoders", m.hw.Encoders, "tempDir", m.tempDir)

	m.wg.Add(1)
	go m.reaper()
	return m, nil
}

// cleanStale removes session directories left by a previous process.
func (m *Manager) cleanStale() {
	entries, err := os.ReadDir(m.tempDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "s-") {
			_ = os.RemoveAll(filepath.Join(m.tempDir, e.Name()))
		}
	}
}

// Version is the ffmpeg version string.
func (m *Manager) Version() string { return m.version }

// FFmpegPath is the ffmpeg executable in use.
func (m *Manager) FFmpegPath() string { return m.ffmpeg }

// FFprobePath is the ffprobe executable in use.
func (m *Manager) FFprobePath() string { return m.ffprobe }

// HWAccel implements core.Transcoder.
func (m *Manager) HWAccel() core.HWAccel {
	hw := m.hw
	hw.Encoders = append([]string(nil), m.hw.Encoders...)
	return hw
}

// UpdateConfig applies changed limits (MaxTranscodes,
// RemoteMaxBitrateKbps). Paths and hardware choice need a restart.
func (m *Manager) UpdateConfig(cfg config.Transcode) {
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
}

// Policy is the server-side decision input derived from config and
// hardware.
func (m *Manager) Policy() Policy {
	m.mu.Lock()
	remote := m.cfg.RemoteMaxBitrateKbps
	m.mu.Unlock()
	hevcHW := false
	for _, e := range m.hw.Encoders {
		if strings.HasPrefix(e, "hevc_") {
			hevcHW = true
		}
	}
	// libx265 is too slow for real-time 4K/1080p transcodes on most CPUs,
	// so only a hardware HEVC encoder makes HEVC (and kept HDR) a target.
	return Policy{
		RemoteMaxBitrateKbps: remote,
		HEVCEncoder:          hevcHW,
		HEVCHardware:         hevcHW,
	}
}

// Probe implements core.Transcoder.
func (m *Manager) Probe(ctx context.Context, inputURL string) (*core.MediaInfo, error) {
	start := time.Now()
	info, err := probe(ctx, m.runner, m.ffprobe, inputURL)
	if err != nil {
		return nil, err
	}
	m.log.Debug("probed", "input", inputURL, "took", time.Since(start).Round(time.Millisecond),
		"container", info.Container, "duration", info.Duration, "streams", len(info.Streams))
	return info, nil
}

// Decide implements core.Transcoder.
func (m *Manager) Decide(info core.MediaInfo, caps core.ClientCaps, opts core.DecideOptions) core.Decision {
	return Decide(info, caps, opts, m.Policy())
}

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// StartHLS implements core.Transcoder: it starts an HLS session, or a DASH
// session when req.Protocol is core.ProtocolDASH. A session with the same
// ID is replaced (Plex reuses session IDs when it restarts a transcode).
func (m *Manager) StartHLS(ctx context.Context, req core.HLSRequest) (core.HLSSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.InputURL == "" {
		return nil, errors.New("transcode: StartHLS: empty input URL")
	}
	proto := req.Protocol
	switch proto {
	case "":
		proto = core.ProtocolHLS
	case core.ProtocolHLS, core.ProtocolDASH:
	default:
		return nil, fmt.Errorf("transcode: StartHLS: unknown protocol %q", req.Protocol)
	}
	d := req.Decision
	switch d.Kind {
	case core.DecisionRemux, core.DecisionTranscode:
	case core.DecisionDirectPlay:
		// Asked to stream something directly playable: remux it.
		d.Kind = core.DecisionRemux
		d.VideoCopy, d.AudioCopy = true, true
		d.Container = ""
	case "":
		return nil, errors.New("transcode: StartHLS: decision required")
	default:
		return nil, fmt.Errorf("transcode: StartHLS: unknown decision kind %q", d.Kind)
	}
	if proto == core.ProtocolDASH {
		d = dashDecision(req.Info, d)
	}
	v := req.Info.Video()
	if v == nil {
		d.VideoCopy = true // audio-only: nothing to encode
	}
	if d.VideoCopy && v != nil && d.VideoCodec == "" {
		d.VideoCodec = v.Codec
	}
	if d.AudioStream < 0 {
		if a := pickAudio(req.Info, -1); a != nil {
			d.AudioStream = a.Index
		}
	}
	if a := findStream(req.Info, d.AudioStream, "audio"); a != nil && d.AudioCopy && d.AudioCodec == "" {
		d.AudioCodec = a.Codec
	}
	if proto == core.ProtocolDASH {
		d.Container = ContainerFMP4
	} else if d.Container != ContainerFMP4 && d.Container != ContainerMPEGTS && d.Container != ContainerMatroska {
		d.Container = segmentContainer(d.VideoCodec, d.AudioCodec)
	}
	segDur := req.SegmentDuration
	if segDur <= 0 {
		segDur = DefaultSegmentDuration
		if proto == core.ProtocolDASH {
			segDur = DefaultDASHSegmentDuration
		}
	}
	offset := req.StartOffset
	if offset < 0 {
		offset = 0
	}
	if req.Info.Duration > 0 && offset >= req.Info.Duration {
		offset = req.Info.Duration - segDur
		if offset < 0 {
			offset = 0
		}
	}
	m.mu.Lock()
	adapt := adaptiveFromConfig(m.cfg)
	m.mu.Unlock()
	rungs := buildRungs(req.Info, d, adapt)
	if proto == core.ProtocolHLS && d.Container == ContainerMatroska && len(rungs) > 1 {
		// Plex's native player probes every rendition's initialization and
		// early segments concurrently. These rungs share one encoder, so
		// those probes would repeatedly stop the active run. Advertise the
		// selected quality alone until rungs can produce independently.
		// Explicit Plex quality changes still start a separate decision.
		rungs = rungs[:1]
	}

	// HLS playlists start at the offset; a DASH MPD always spans the whole
	// title and the first run starts at the offset's segment.
	base, startSeg, total := offset, 0, 0
	tracks := 1
	var reps []dashRep
	var plan *Timeline
	if proto == core.ProtocolDASH {
		base = 0
		startSeg = int(offset / segDur)
		total = dashSegmentCount(req.Info.Duration, segDur)
		if total > 0 && startSeg >= total {
			startSeg = total - 1
		}
		reps = dashRepsLadder(req.Info, d, rungs)
		if len(reps) == 0 {
			return nil, errors.New("transcode: StartHLS: no video or audio stream for DASH")
		}
		tracks = len(reps)
	} else {
		// One segment plan for the whole session (all rungs share it).
		span := time.Duration(0)
		if req.Info.Duration > 0 {
			span = req.Info.Duration - base
		}
		steadyMode := ModeTranscode
		if d.VideoCopy {
			steadyMode = ModeCopy
		}
		// Pure copy output cannot force the short opening GOPs. Chaining a
		// run at the nominal ramp boundary can seek back to an earlier source
		// keyframe and replay it. TS ramps encode their opening GOPs; other
		// copy containers keep one continuous run.
		fastStart := adapt.FastStart && v != nil && (!d.VideoCopy || d.Container == ContainerMPEGTS)
		plan = buildTimeline(span, segDur, timelineOptions{
			steadyMode: steadyMode,
			fastStart:  fastStart,
			rampSegs:   adapt.RampSegs,
			rampDur:    adapt.RampDur,
			keyframes:  req.Info.Keyframes,
			base:       base,
			snap:       d.VideoCopy && len(req.Info.Keyframes) > 0,
		})
		total = plan.Total()
		tracks = len(rungs) // one segment track per rung
	}
	id := req.SessionID
	if id == "" {
		id = randomHex(12)
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("transcode: manager closed")
	}
	old := m.sessions[id]
	if !d.VideoCopy && m.cfg.MaxTranscodes > 0 {
		n := 0
		for sid, s := range m.sessions {
			if sid != id && !s.Decision().VideoCopy {
				n++
			}
		}
		if n >= m.cfg.MaxTranscodes {
			m.mu.Unlock()
			return nil, fmt.Errorf("%w (%d running)", ErrTooManyTranscodes, n)
		}
	}
	delete(m.sessions, id)
	m.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	safe := unsafeID.ReplaceAllString(id, "_")
	if len(safe) > 48 {
		safe = safe[:48]
	}
	dir := filepath.Join(m.tempDir, "s-"+safe+"-"+randomHex(4))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("transcode: session dir: %w", err)
	}
	s := &Session{
		id:          id,
		m:           m,
		dir:         dir,
		segDur:      segDur,
		base:        base,
		total:       total,
		container:   d.Container,
		proto:       proto,
		log:         m.log.With("session", id),
		created:     time.Now(),
		input:       req.InputURL,
		info:        req.Info,
		decision:    d,
		done:        make([]map[int]bool, tracks),
		lastReq:     startSeg,
		endSeg:      -1,
		resumeAt:    -1,
		changed:     make(chan struct{}),
		plan:        plan,
		adapt:       adapt,
		rungs:       rungs,
		initFiles:   make([]string, tracks),
		seekRampSeg: -1,
		segSizes:    map[int]int64{},
		governorOn:  adapt.Governor && proto == core.ProtocolHLS && d.VideoCopy && v != nil,
	}
	for t := range s.done {
		s.done[t] = map[int]bool{}
	}
	if proto == core.ProtocolDASH {
		s.dash = newDASHState(reps, m.opts.dashURLs, id)
	}
	s.Touch()
	s.log.Info("transcode session starting", "protocol", proto, "kind", d.Kind, "container", d.Container,
		"offset", offset, "segment", startSeg, "segments", s.total, "reason", d.Reason)

	s.mu.Lock()
	err := s.startRunLocked(startSeg, false)
	s.mu.Unlock()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = s.Close()
		return nil, errors.New("transcode: manager closed")
	}
	if prev := m.sessions[id]; prev != nil {
		// A concurrent StartHLS for the same ID won the race: replace it.
		defer prev.Close()
	}
	m.sessions[id] = s
	m.mu.Unlock()
	return s, nil
}

// Session implements core.Transcoder.
func (m *Manager) Session(id string) (core.HLSSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	return s, true
}

// Sessions lists the live sessions (admin dashboard).
func (m *Manager) Sessions() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// Stop implements core.Transcoder.
func (m *Manager) Stop(id string) {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}

// forget unregisters a closed session (if it is still the registered one).
func (m *Manager) forget(s *Session) {
	m.mu.Lock()
	if m.sessions[s.id] == s {
		delete(m.sessions, s.id)
	}
	m.mu.Unlock()
}

func (m *Manager) reaper() {
	defer m.wg.Done()
	every := m.opts.idleTimeout / 4
	if every > 30*time.Second {
		every = 30 * time.Second
	}
	if every < 50*time.Millisecond {
		every = 50 * time.Millisecond
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.reapIdle(time.Now())
		}
	}
}

func (m *Manager) reapIdle(now time.Time) {
	if m.opts.idleTimeout <= 0 {
		return
	}
	for _, s := range m.Sessions() {
		if idle := now.Sub(s.LastAccess()); idle > m.opts.idleTimeout {
			s.log.Info("closing idle session", "idle", idle.Round(time.Second))
			_ = s.Close()
		}
	}
}

// Close stops every session and the reaper.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	close(m.stop)
	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			_ = s.Close()
		}(s)
	}
	wg.Wait()
	m.wg.Wait()
	return nil
}

// ExtractSubtitle writes an embedded text subtitle stream as WebVTT. It
// reads the whole input (subtitle packets are interleaved through it).
func (m *Manager) ExtractSubtitle(ctx context.Context, inputURL string, streamIndex int, codec string, w io.Writer) error {
	if codec != "" && IsImageSubtitle(codec) {
		return ErrImageSubtitle
	}
	var stderr tailWriter
	err := m.runner.Run(ctx, Command{Path: m.ffmpeg, Args: subtitleArgs(inputURL, streamIndex), Stdout: w, Stderr: &stderr})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("transcode: extract subtitle %d: %w: %s", streamIndex, err, tail(stderr.String(), 300))
	}
	return nil
}

// tailWriter keeps the last 4 KiB written.
type tailWriter struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailWriter) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(b), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
