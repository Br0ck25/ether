package transcode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ether-media/ether/internal/core"
)

// Session is one HLS remux or transcode. It implements core.HLSSession.
//
// Segment n always covers media time base+n*segDur (base is the request's
// StartOffset). ffmpeg runs are started at a segment boundary with -ss and
// -copyts, so restarting at any segment (a seek far ahead or behind, a
// failover to another release) keeps numbering and timestamps continuous.
type Session struct {
	id        string
	m         *Manager
	dir       string
	segDur    time.Duration
	base      time.Duration
	total     int // segments in the playlist; 0 = unknown duration
	container string
	log       *slog.Logger
	created   time.Time

	lastAccess atomic.Int64 // unix nanos

	mu       sync.Mutex
	input    string
	info     core.MediaInfo
	decision core.Decision
	forceCPU bool // hardware encoder failed; stay on CPU
	noPause  bool // platform cannot suspend ffmpeg
	resumeAt int  // segment to restart at after a throttle stop; -1 none
	done     map[int]bool
	cur      *run
	runSeq   int
	lastReq  int
	endSeg   int // last segment of the input once a run hit EOF; -1 unknown
	initFile string
	failures int // consecutive runs that failed without output
	failErr  error
	closed   bool
	changed  chan struct{}
	playlist []byte
}

// run is one ffmpeg process producing segments from startSeg on.
type run struct {
	seq      int
	startSeg int
	spec     runSpec
	proc     Process
	hw       bool
	maxDone  int
	produced int
	finished bool
	stopped  bool // stopped deliberately, not a failure
	paused   bool
	err      error
	stderr   *tailBuffer
	started  time.Time

	progressMu sync.Mutex
	progress   Progress
}

func (r *run) next() int {
	if r.produced == 0 {
		return r.startSeg
	}
	return r.maxDone + 1
}

// ID implements core.HLSSession.
func (s *Session) ID() string { return s.id }

// LastAccess implements core.HLSSession.
func (s *Session) LastAccess() time.Time { return time.Unix(0, s.lastAccess.Load()) }

// Touch marks the session as in use (e.g. on a Plex timeline ping while the
// client is paused) so the idle reaper keeps it.
func (s *Session) Touch() { s.lastAccess.Store(time.Now().UnixNano()) }

// Decision is the session's current decision (it can change on SwapInput).
func (s *Session) Decision() core.Decision {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.decision
}

// Info is the media info of the current input.
func (s *Session) Info() core.MediaInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// Progress is the current ffmpeg run's latest progress report.
func (s *Session) Progress() Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.cur
	if r == nil {
		return Progress{}
	}
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	return r.progress
}

// HWAccelerated reports whether the current run encodes on hardware.
func (s *Session) HWAccelerated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur != nil && s.cur.hw
}

// SegmentDuration is the session's segment length.
func (s *Session) SegmentDuration() time.Duration { return s.segDur }

// Container is "mpegts" or "fmp4".
func (s *Session) Container() string { return s.container }

func (s *Session) segName(n int) string { return s.m.opts.segmentURI(n, s.container) }

func (s *Session) segPath(n int) string {
	return filepath.Join(s.dir, segmentFile(n, segmentExt(s.container)))
}

func (s *Session) segStart(n int) time.Duration { return s.base + time.Duration(n)*s.segDur }

func (s *Session) broadcastLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// ServePlaylist implements core.HLSSession: the full-length VOD playlist.
func (s *Session) ServePlaylist(w http.ResponseWriter, r *http.Request) {
	s.Touch()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		http.Error(w, "session closed", http.StatusGone)
		return
	}
	var body []byte
	if s.total > 0 {
		if s.playlist == nil {
			s.playlist = mediaPlaylist(s.span(), s.segDur, s.container, s.segName)
		}
		body = s.playlist
	} else {
		done := make([]int, 0, len(s.done))
		for n := range s.done {
			done = append(done, n)
		}
		sort.Ints(done)
		body = eventPlaylist(done, s.segDur, s.container, s.endSeg >= 0, s.segName)
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

func (s *Session) span() time.Duration {
	d := s.info.Duration - s.base
	if d < 0 {
		return 0
	}
	return d
}

// ServeSegment implements core.HLSSession. n == InitSegment serves the fMP4
// init segment.
func (s *Session) ServeSegment(w http.ResponseWriter, r *http.Request, n int) {
	s.Touch()
	if n == InitSegment {
		s.serveInit(w, r)
		return
	}
	if n < 0 || (s.total > 0 && n >= s.total) {
		http.NotFound(w, r)
		return
	}
	timeout := time.NewTimer(s.m.opts.segmentTimeout)
	defer timeout.Stop()
	restarts := 0
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			http.Error(w, "session closed", http.StatusGone)
			return
		}
		s.lastReq = n
		s.throttleLocked()
		last := s.total > 0 && n == s.total-1
		waitTail := false
		if s.done[n] && last && !s.tailSettledLocked(n) {
			// Wait for ffmpeg to finish the title so trailing slivers can be
			// appended (below).
			waitTail = true
		} else if s.done[n] {
			paths := []string{s.segPath(n)}
			if last {
				// ffmpeg may cut a short final segment past the playlist's
				// last one; TS and fMP4 fragments concatenate cleanly.
				for k := n + 1; s.done[k]; k++ {
					paths = append(paths, s.segPath(k))
				}
			}
			s.pruneLocked()
			s.mu.Unlock()
			if s.serveFiles(w, r, paths, n) {
				return
			}
			s.mu.Lock()
			delete(s.done, n) // vanished (pruned meanwhile): produce again
			s.mu.Unlock()
			continue
		}
		if s.endSeg >= 0 && n > s.endSeg {
			s.mu.Unlock()
			http.NotFound(w, r)
			return
		}
		if !waitTail && s.needsRestartLocked(n) {
			if restarts >= 2 {
				err := s.failErr
				s.mu.Unlock()
				s.log.Warn("segment unavailable after restarts", "segment", n, "err", err)
				http.Error(w, "segment unavailable", http.StatusBadGateway)
				return
			}
			restarts++
			if err := s.startRunLocked(n, s.failures > 0); err != nil {
				s.mu.Unlock()
				s.log.Error("start ffmpeg", "segment", n, "err", err)
				http.Error(w, "transcoder failed to start", http.StatusInternalServerError)
				return
			}
		}
		ch := s.changed
		s.mu.Unlock()
		select {
		case <-ch:
		case <-r.Context().Done():
			return
		case <-timeout.C:
			s.log.Warn("timed out waiting for segment", "segment", n)
			http.Error(w, "timed out waiting for segment", http.StatusGatewayTimeout)
			return
		}
	}
}

// needsRestartLocked decides between waiting for the running ffmpeg and
// restarting it at n: wait when n is at or a little ahead of the encoder,
// restart when n is behind the run's start, far ahead, or nothing runs.
func (s *Session) needsRestartLocked(n int) bool {
	r := s.cur
	if r == nil || r.finished || r.stopped {
		return true
	}
	if n < r.startSeg || n < r.next() {
		// Behind the run's start, or already passed by it (pruned).
		return true
	}
	ahead := int(s.m.opts.restartAhead / s.segDur)
	if ahead < 2 {
		ahead = 2
	}
	return n > r.next()+ahead
}

func (s *Session) serveInit(w http.ResponseWriter, r *http.Request) {
	if s.container != ContainerFMP4 {
		http.NotFound(w, r)
		return
	}
	timeout := time.NewTimer(s.m.opts.segmentTimeout)
	defer timeout.Stop()
	restarted := false
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			http.Error(w, "session closed", http.StatusGone)
			return
		}
		if s.initFile != "" {
			path := filepath.Join(s.dir, s.initFile)
			s.mu.Unlock()
			if s.serveFile(w, r, path, InitSegment) {
				return
			}
			http.Error(w, "init segment missing", http.StatusInternalServerError)
			return
		}
		if (s.cur == nil || s.cur.finished) && !restarted {
			restarted = true
			start := s.lastReq
			if start < 0 {
				start = 0
			}
			if err := s.startRunLocked(start, s.failures > 0); err != nil {
				s.mu.Unlock()
				http.Error(w, "transcoder failed to start", http.StatusInternalServerError)
				return
			}
		}
		ch := s.changed
		s.mu.Unlock()
		select {
		case <-ch:
		case <-r.Context().Done():
			return
		case <-timeout.C:
			http.Error(w, "timed out waiting for init segment", http.StatusGatewayTimeout)
			return
		}
	}
}

// tailSettledLocked reports whether nothing more will follow segment n of
// the current input: ffmpeg hit EOF, moved past n, or is not running.
func (s *Session) tailSettledLocked(n int) bool {
	if s.endSeg >= 0 {
		return true
	}
	r := s.cur
	return r == nil || r.finished || r.maxDone > n
}

// serveFiles writes one segment made of one or more files.
func (s *Session) serveFiles(w http.ResponseWriter, r *http.Request, paths []string, n int) bool {
	if len(paths) == 1 {
		return s.serveFile(w, r, paths[0], n)
	}
	var buf bytes.Buffer
	var mod time.Time
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return false
		}
		buf.Write(b)
		if st, err := os.Stat(p); err == nil && st.ModTime().After(mod) {
			mod = st.ModTime()
		}
	}
	s.setSegmentHeaders(w)
	http.ServeContent(w, r, filepath.Base(paths[0]), mod, bytes.NewReader(buf.Bytes()))
	return true
}

func (s *Session) setSegmentHeaders(w http.ResponseWriter) {
	ct := "video/mp2t"
	if s.container == ContainerFMP4 {
		ct = "video/mp4"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
}

// serveFile writes a finished segment; false when the file is gone.
func (s *Session) serveFile(w http.ResponseWriter, r *http.Request, path string, n int) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false
	}
	s.setSegmentHeaders(w)
	name := filepath.Base(path)
	if n == InitSegment {
		name = InitSegmentName
	}
	http.ServeContent(w, r, name, st.ModTime(), f)
	return true
}

// throttleLocked keeps ffmpeg from running far ahead of the player, so a
// remux does not pull a whole title from Usenet up front: it suspends
// ffmpeg and resumes it as the player catches up. Where processes cannot be
// suspended (Windows) it stops ffmpeg instead and restarts it at the next
// missing segment in time.
func (s *Session) throttleLocked() {
	if s.m.opts.maxAhead <= 0 {
		return
	}
	limit := s.aheadLimit()
	r := s.cur
	if r == nil || r.finished {
		if s.resumeAt >= 0 && s.endSeg < 0 && s.resumeAt-s.lastReq <= limit/2 && !s.closed {
			at := s.resumeAt
			s.log.Debug("throttle: restarting ffmpeg", "segment", at)
			if err := s.startRunLocked(at, false); err != nil {
				s.log.Warn("throttle: restart failed", "err", err)
			}
		}
		return
	}
	ahead := r.maxDone - s.lastReq
	if r.produced == 0 {
		ahead = 0
	}
	switch {
	case !r.paused && ahead > limit:
		if !s.noPause {
			err := r.proc.Pause()
			if err == nil {
				r.paused = true
				s.log.Debug("throttle: paused ffmpeg", "ahead", ahead)
				return
			}
			if !errors.Is(err, ErrPauseUnsupported) {
				return
			}
			s.noPause = true
		}
		resume := r.maxDone + 1
		s.stopRunLocked()
		s.resumeAt = resume
		s.log.Debug("throttle: stopped ffmpeg", "ahead", ahead, "resumeAt", resume)
	case r.paused && ahead <= limit/2:
		if err := r.proc.Resume(); err == nil {
			r.paused = false
			s.log.Debug("throttle: resumed ffmpeg", "ahead", ahead)
		}
	}
}

// aheadLimit is how many segments ffmpeg may run ahead of the player.
func (s *Session) aheadLimit() int {
	limit := int(s.m.opts.maxAhead / s.segDur)
	if limit < 4 {
		limit = 4
	}
	return limit
}

// pruneLocked deletes finished segments well behind the play position (and,
// when throttling bounds the encoder, stale ones far ahead of it) to bound
// disk use.
func (s *Session) pruneLocked() {
	if s.m.opts.keepBehind <= 0 {
		return
	}
	behind := int(s.m.opts.keepBehind / s.segDur)
	farAhead := -1
	if s.m.opts.maxAhead > 0 {
		farAhead = 2*s.aheadLimit() + 8
	}
	for n := range s.done {
		if n < s.lastReq-behind || (farAhead >= 0 && n > s.lastReq+farAhead) {
			if err := os.Remove(s.segPath(n)); err == nil || errors.Is(err, os.ErrNotExist) {
				delete(s.done, n)
			}
		}
	}
}

// startRunLocked stops the current run and starts ffmpeg at segment seg.
func (s *Session) startRunLocked(seg int, bigProbe bool) error {
	if s.closed {
		return errors.New("transcode: session closed")
	}
	s.stopRunLocked()
	s.resumeAt = -1
	if s.total > 0 && seg >= s.total {
		seg = s.total - 1
	}
	if seg < 0 {
		seg = 0
	}
	s.runSeq++
	accel := s.m.hw
	if s.forceCPU {
		accel = core.HWAccel{Name: AccelNone}
	}
	spec := runSpec{
		Input:    s.input,
		Info:     s.info,
		Decision: s.decision,
		Start:    s.segStart(seg),
		SegDur:   s.segDur,
		StartSeg: seg,
		Dir:      s.dir,
		Playlist: fmt.Sprintf("run-%d.m3u8", s.runSeq),
		InitName: fmt.Sprintf("init-%d.mp4", s.runSeq),
		Accel:    accel,
		Features: s.m.feat,
		BigProbe: bigProbe,
	}
	r := &run{
		seq:      s.runSeq,
		startSeg: seg,
		spec:     spec,
		hw:       spec.usesHW(),
		maxDone:  -1,
		stderr:   newTailBuffer(40),
		started:  time.Now(),
	}
	args := buildArgs(spec)
	runLog := s.log.With("run", r.seq)
	var pp progressParser
	lastLog := time.Time{}
	stdout := &lineWriter{fn: func(l string) {
		p, ok := pp.line(l)
		if !ok {
			return
		}
		// Never take s.mu here: Stop holds it while waiting for ffmpeg to
		// exit, and exit waits for this pipe to drain.
		r.progressMu.Lock()
		r.progress = p
		r.progressMu.Unlock()
		if time.Since(lastLog) > 15*time.Second {
			lastLog = time.Now()
			runLog.Debug("ffmpeg progress", "outTime", p.OutTime, "speed", p.Speed, "fps", p.FPS)
		}
	}}
	stderr := &lineWriter{fn: func(l string) {
		r.stderr.add(l)
		runLog.Debug("ffmpeg", "line", l)
	}}
	proc, err := s.m.runner.Start(Command{Path: s.m.ffmpeg, Args: args, Dir: s.dir, Stdout: stdout, Stderr: stderr})
	if err != nil {
		return fmt.Errorf("transcode: start ffmpeg: %w", err)
	}
	r.proc = proc
	s.cur = r
	runLog.Info("ffmpeg started", "segment", seg, "at", spec.Start, "kind", s.decision.Kind,
		"videoCopy", s.decision.VideoCopy, "audioCopy", s.decision.AudioCopy, "hw", r.hw)
	runLog.Debug("ffmpeg args", "args", strings.Join(args, " "))
	go s.watch(r)
	s.broadcastLocked()
	return nil
}

// stopRunLocked kills the current run. ffmpeg exits in milliseconds after a
// kill, and the run's waiter never takes s.mu, so this cannot deadlock.
func (s *Session) stopRunLocked() {
	r := s.cur
	if r == nil {
		return
	}
	s.cur = nil
	if !r.finished {
		r.stopped = true
		r.proc.Stop()
	}
}

// watch polls a run's playlist for finished segments until ffmpeg exits.
func (s *Session) watch(r *run) {
	t := time.NewTicker(s.m.opts.pollInterval)
	defer t.Stop()
	playlist := filepath.Join(s.dir, r.spec.Playlist)
	for {
		select {
		case <-r.proc.Done():
			s.poll(r, playlist)
			s.finish(r)
			return
		case <-t.C:
			s.poll(r, playlist)
		}
	}
}

func (s *Session) poll(r *run, playlist string) {
	p, err := readFFmpegPlaylist(playlist)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if r.stopped && s.cur != r {
		return // superseded; its segments may already be discarded
	}
	changed := false
	for _, n := range p.segments {
		if n < r.startSeg || (n <= r.maxDone && r.produced > 0) {
			continue
		}
		if s.total > 0 && n >= s.total+2 {
			continue // past any plausible trailing sliver
		}
		if s.container == ContainerFMP4 && s.initFile != r.spec.InitName {
			if _, err := os.Stat(filepath.Join(s.dir, r.spec.InitName)); err == nil {
				s.initFile = r.spec.InitName
			}
		}
		r.produced++
		r.maxDone = n
		if !s.done[n] {
			s.done[n] = true
		}
		changed = true
	}
	if changed {
		if r.produced > 0 && s.cur == r {
			s.failures = 0
			s.failErr = nil
		}
		s.throttleLocked()
		s.broadcastLocked()
	}
}

// finish records a run's exit and reacts to failures: a hardware encoder
// that dies before its first segment is retried on CPU, any other start
// failure is retried once with a larger probe, and a mid-stream failure is
// reported to the failure handler (playback fails over to another release).
func (s *Session) finish(r *run) {
	err := r.proc.Err()
	s.mu.Lock()
	defer s.mu.Unlock()
	r.finished = true
	r.err = err
	r.paused = false
	defer s.broadcastLocked()
	if s.closed || r.stopped {
		return
	}
	log := s.log.With("run", r.seq)
	if err == nil {
		// ffmpeg reached the end of the input.
		if r.produced > 0 {
			s.endSeg = r.maxDone
		} else {
			s.endSeg = r.startSeg - 1
		}
		log.Info("ffmpeg finished", "lastSegment", s.endSeg)
		return
	}
	stderr := r.stderr.String()
	log.Warn("ffmpeg failed", "err", err, "segments", r.produced, "stderr", tail(stderr, 1500))
	if s.cur != r {
		return
	}
	if r.produced == 0 {
		if r.hw && !s.forceCPU && looksLikeEncoderFailure(stderr, r.spec) {
			s.forceCPU = true
			log.Warn("hardware encoder failed; falling back to CPU", "hwaccel", r.spec.Accel.Name)
			if err := s.startRunLocked(r.startSeg, r.spec.BigProbe); err != nil {
				s.failErr = err
			}
			return
		}
		s.failures++
		s.failErr = fmt.Errorf("ffmpeg: %w: %s", err, tail(stderr, 300))
		if s.failures == 1 {
			if err := s.startRunLocked(r.startSeg, true); err != nil {
				s.failErr = err
			}
			return
		}
	} else {
		s.failErr = fmt.Errorf("ffmpeg: %w: %s", err, tail(stderr, 300))
	}
	if h := s.m.opts.onFailure; h != nil {
		at := s.segStart(r.next())
		ferr := s.failErr
		go h(s.id, at, ferr)
	}
}

// looksLikeEncoderFailure tells a hardware encoder/filter failure apart
// from an input failure, so only the former switches the session to CPU.
func looksLikeEncoderFailure(stderr string, spec runSpec) bool {
	if strings.TrimSpace(stderr) == "" {
		return true
	}
	l := strings.ToLower(stderr)
	enc, _ := videoEncoder(spec.Decision.VideoCodec, spec.Accel, spec.Features)
	for _, k := range []string{strings.ToLower(enc), "encoder", "hwaccel", "hwupload", "hw_frames", "hardware",
		"device", "vaapi", "cuda", "nvenc", "qsv", "videotoolbox", "scale_vt", "tonemap_vaapi", "impossible to convert",
		"function not implemented", "filter", "pixel format"} {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

// SwapInput implements core.HLSSession: restart on a new input at `at`
// (mid-stream failover). Segments from `at` on are discarded and produced
// again from the new input; numbering stays continuous.
func (s *Session) SwapInput(ctx context.Context, inputURL string, info core.MediaInfo, at time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if inputURL == "" {
		return errors.New("transcode: SwapInput: empty input URL")
	}
	s.Touch()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("transcode: session closed")
	}
	seg := 0
	if at > s.base {
		seg = int((at - s.base) / s.segDur)
	}
	if s.total > 0 && seg >= s.total {
		seg = s.total - 1
	}
	s.stopRunLocked()
	for n := range s.done {
		if n >= seg {
			_ = os.Remove(s.segPath(n))
			delete(s.done, n)
		}
	}
	next := adaptDecision(s.decision, s.info, info, s.container)
	if next.Kind != s.decision.Kind || next.VideoCopy != s.decision.VideoCopy || next.AudioCopy != s.decision.AudioCopy {
		s.log.Info("failover changes decision", "from", s.decision.Kind, "to", next.Kind, "reason", next.Reason)
	}
	s.input = inputURL
	s.info = info
	s.decision = next
	s.endSeg = -1
	s.failures = 0
	s.failErr = nil
	s.lastReq = seg
	s.log.Info("input swapped", "at", at, "segment", seg)
	return s.startRunLocked(seg, false)
}

// adaptDecision keeps a failover's output compatible with what the client
// is already playing: same codecs, container and size. Streams are matched
// by language; anything the new release cannot copy is transcoded to the
// codec the client already accepted.
func adaptDecision(old core.Decision, oldInfo, newInfo core.MediaInfo, container string) core.Decision {
	d := old
	d.Container = container
	var notes []string

	// Audio: same language (and codec when possible) as before.
	oldA := findStream(oldInfo, old.AudioStream, "audio")
	newA := matchStream(newInfo, oldA, "audio")
	d.AudioStream = -1
	if newA != nil {
		d.AudioStream = newA.Index
	}
	// Subtitles.
	oldS := findStream(oldInfo, old.SubtitleStream, "subtitle")
	newS := matchStream(newInfo, oldS, "subtitle")
	d.SubtitleStream = -1
	if oldS != nil && newS != nil {
		d.SubtitleStream = newS.Index
	}
	if d.BurnSubtitles && newS == nil {
		d.BurnSubtitles = false
		notes = append(notes, "subtitle track missing in new release")
	}
	if newS != nil && IsImageSubtitle(newS.Codec) && oldS != nil {
		d.BurnSubtitles = true
	}

	oldV, newV := oldInfo.Video(), newInfo.Video()
	if newV != nil && oldV != nil {
		if d.VideoCopy {
			sameRange := dynamicRange(oldV) == dynamicRange(newV) ||
				(dynamicRange(newV) == rangeDV && rangeOfDVBase(newV) == dynamicRange(oldV))
			if newV.Codec != oldV.Codec || newV.Height > oldV.Height || !sameRange || d.BurnSubtitles {
				d.Kind = core.DecisionTranscode
				d.VideoCopy = false
				d.VideoCodec = oldV.Codec
				if d.VideoCodec != "h264" && d.VideoCodec != "hevc" {
					d.VideoCodec = "h264"
				}
				d.MaxHeight = oldV.Height
				if d.BitrateKbps <= 0 {
					d.BitrateKbps = bitrateCeiling(d.MaxHeight, d.VideoCodec)
				}
				d.ToneMap = isHDR(newV) && !isHDR(oldV)
				notes = append(notes, fmt.Sprintf("video %s %dp re-encoded to match the stream", newV.Codec, newV.Height))
			}
		} else {
			keptHDR := isHDR(oldV) && !old.ToneMap
			d.ToneMap = isHDR(newV) && !keptHDR
		}
	}
	if newA != nil && oldA != nil && d.AudioCopy && newA.Codec != oldA.Codec {
		d.AudioCopy = false
		d.AudioCodec = oldA.Codec
		notes = append(notes, fmt.Sprintf("audio %s re-encoded to %s", newA.Codec, d.AudioCodec))
	}
	if !d.AudioCopy && newA != nil {
		switch d.AudioCodec {
		case "aac", "ac3", "eac3", "mp3":
		case "opus":
			if container != ContainerFMP4 {
				d.AudioCodec = "aac"
			}
		default: // not encodable for HLS (flac, truehd, dts...)
			d.AudioCodec = "aac"
		}
	}
	if d.Kind == core.DecisionDirectPlay {
		d.Kind = core.DecisionRemux
	}
	if len(notes) > 0 {
		d.Reason = old.Reason + "; failover: " + strings.Join(notes, ", ")
	}
	return d
}

func rangeOfDVBase(v *core.StreamInfo) string {
	b, ok := dvBaseLayer(v)
	if !ok {
		return ""
	}
	return b
}

// matchStream finds the stream in info that best matches prev: same
// language and codec, then same language, then the default/first one.
func matchStream(info core.MediaInfo, prev *core.StreamInfo, typ string) *core.StreamInfo {
	var sameLang, first, def *core.StreamInfo
	for i := range info.Streams {
		s := &info.Streams[i]
		if s.Type != typ || s.External {
			continue
		}
		if first == nil {
			first = s
		}
		if s.Default && def == nil {
			def = s
		}
		if prev == nil {
			continue
		}
		if s.Language == prev.Language {
			if s.Codec == prev.Codec && s.Forced == prev.Forced {
				return s
			}
			if sameLang == nil {
				sameLang = s
			}
		}
	}
	if sameLang != nil {
		return sameLang
	}
	if typ == "subtitle" {
		return nil // never invent subtitles the user did not pick
	}
	if def != nil {
		return def
	}
	return first
}

// Close implements core.HLSSession: kill ffmpeg and delete the segments.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.stopRunLocked()
	s.broadcastLocked()
	s.mu.Unlock()
	err := os.RemoveAll(s.dir)
	s.m.forget(s)
	s.log.Info("session closed", "age", time.Since(s.created).Round(time.Second))
	return err
}
