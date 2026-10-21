package transcode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

// Session is one HLS or DASH remux or transcode. It implements
// core.HLSSession, and core.DASHSession for DASH sessions.
//
// Segment n always covers media time base+n*segDur (base is the request's
// StartOffset for HLS and 0 for DASH, whose MPD always spans the whole
// title). ffmpeg runs are started at a segment boundary with -ss and
// -copyts, so restarting at any segment (a seek far ahead or behind, a
// failover to another release) keeps numbering and timestamps continuous.
//
// An HLS session has one track of muxed segments. A DASH session has one
// track per representation (video, audio); ffmpeg's dash muxer writes short
// chunks that the session regroups into numbered segments by timestamp.
type Session struct {
	rtxConfig          config.RTXVideo
	rtxTimeline        bool
	rtxDiscontinuities map[int]bool

	liveInput    bool
	inputHeaders map[string]string
	id           string
	m            *Manager
	dir          string
	watchers     sync.WaitGroup // running watch goroutines; Close waits for them
	removals     pendingRemovals
	segDur       time.Duration
	base         time.Duration
	total        int // segments in the playlist; 0 = unknown duration
	container    string
	proto        string // core.ProtocolHLS or core.ProtocolDASH
	log          *slog.Logger
	created      time.Time

	lastAccess atomic.Int64 // unix nanos

	mu          sync.Mutex
	input       string
	info        core.MediaInfo
	decision    core.Decision
	forceCPU    bool           // hardware encoder failed; stay on CPU
	noPause     bool           // platform cannot suspend ffmpeg
	resumeAt    int            // segment to restart at after a throttle stop; -1 none
	done        []map[int]bool // finished segments per track
	cur         *run
	runSeq      int
	lastReq     int
	mkvDelivery map[int]mkvDelivery // last response per track, including retries of a seek
	// reqSeq numbers segment requests as they arrive; movedBy is the number
	// of the request that last moved the encoder. A request that arrived
	// before that move is one the player abandoned by seeking: it must not
	// drag the encoder back to its old position.
	reqSeq      uint64
	movedBy     uint64
	endSeg      int // last segment of the input once a run hit EOF; -1 unknown
	initFile    string
	failures    int // consecutive runs that failed without output
	failErr     error
	inputFailed bool // exhausted this input; HTTP retries cannot restart it
	closed      bool
	changed     chan struct{}
	playlist    []byte
	listed      []int      // unknown duration: per-track listed prefix; never shrinks
	dash        *dashState // DASH sessions only

	// Adaptive HLS engine: the shared segment plan (HLS only), the config
	// snapshot, the ABR ladder and measured delivery goodput.
	plan           *Timeline
	adapt          adaptiveSettings
	rungs          []rung
	initFiles      []string // per HLS track: init path relative to dir
	wantTrack      int      // track the encoder should be producing (rung/rep)
	videoRequested bool     // a client has requested video media, not just initialization
	deliveredTrack int
	deliveredSeg   int
	deliveredCount int // sequential complete media deliveries on the active run
	goodput        goodputEstimator
	segSizes       map[int]int64 // primary-track segment sizes (spike governor)
	governorOn     bool          // plan may change ahead of the player
	lastGovSeg     int           // last segment the governor evaluated from
	govCooldown    time.Time     // no new clamp planning before this
	seekRampSeg    int           // segment a seek restarted at (preset ramp); -1 none
}

// mkvDelivery keeps a retried segment on the same representation: a seek
// may serve the GOP covering an empty cluster, while sequential playback
// needs the empty cluster to avoid repeating that GOP.
type mkvDelivery struct {
	segment int
	seek    bool
}

// run is one ffmpeg process producing segments from startSeg on.
type run struct {
	rtx          bool
	rtxConfirmed atomic.Bool

	seq      int
	startSeg int
	spec     runSpec
	proc     Process
	hw       bool
	maxDone  int // last segment finished (DASH: on every track)
	produced int // segments finished (DASH: on the slowest track)
	finished bool
	stopped  bool // stopped deliberately, not a failure
	paused   bool
	err      error
	stderr   *tailBuffer
	started  time.Time
	mode     SegMode // plan mode this run produces
	planEnd  int     // first segment NOT produced by this run (mode change); -1 none
	track    int     // session track (HLS rung / DASH video rep) encoded

	progressMu sync.Mutex
	progress   Progress

	dash   *dashRun  // DASH runs only; owned by the run's watch goroutine
	chunks *chunkRun // chunked HLS runs only (chunks.go); likewise
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

// encoding reports whether the session counts against the transcode cap:
// it is a transcode by decision, or a copy session whose current run
// encodes (a fast-start ramp, a governor clamp or a lower rung).
func (s *Session) encoding() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.decision.VideoCopy {
		return true
	}
	r := s.cur
	if r == nil || r.finished || r.stopped || r.err != nil {
		return false
	}
	return r.mode != ModeCopy || r.track != 0
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

// RequestedPosition is the title position of the player's latest segment
// request. Source reads can be far ahead or at the container's tail index.
func (s *Session) RequestedPosition() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.segStart(s.lastReq)
}

// Container is "mpegts", "fmp4", or explicitly negotiated "mkv".
func (s *Session) Container() string { return s.container }

// Protocol is core.ProtocolHLS or core.ProtocolDASH.
func (s *Session) Protocol() string { return s.proto }

func (s *Session) isDASH() bool { return s.proto == core.ProtocolDASH }

func (s *Session) segName(n int) string { return s.m.opts.segmentURI(n, s.container) }

// segPath is the file of segment n of the first track.
func (s *Session) segPath(n int) string { return s.trackSegPath(0, n) }

// runDirLocked is where the wanted track's ffmpeg run writes segments:
// the session dir for the primary rung, a rung subdir for other rungs
// (segment N has different bytes per rung).
func (s *Session) runDirLocked() string {
	if s.isDASH() || s.wantTrack <= 0 || s.wantTrack >= len(s.rungs) {
		return s.dir
	}
	return filepath.Join(s.dir, "rung-"+s.rungs[s.wantTrack].id)
}

func (s *Session) trackSegPath(t, n int) string {
	if s.isDASH() {
		return filepath.Join(s.dir, fmt.Sprintf("seg-%d-%05d.m4s", t, n))
	}
	name := segmentFile(n, segmentExt(s.container))
	if t > 0 && t < len(s.rungs) {
		return filepath.Join(s.dir, "rung-"+s.rungs[t].id, name)
	}
	return filepath.Join(s.dir, name)
}

func (s *Session) segStart(n int) time.Duration {
	if s.plan != nil && !s.isDASH() {
		return s.base + s.plan.Start(n)
	}
	return s.base + time.Duration(n)*s.segDur
}

func (s *Session) broadcastLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// ServePlaylist implements core.HLSSession: the full-length VOD playlist
// (the MPD for DASH sessions).
func (s *Session) ServePlaylist(w http.ResponseWriter, r *http.Request) {
	if s.isDASH() {
		s.ServeManifest(w, r)
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		http.Error(w, "session closed", http.StatusGone)
		return
	}
	body := s.renderPlaylistLocked(0)
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

// renderPlaylistLocked renders track t's media playlist from the plan.
// The primary track's VOD playlist is cached until the plan changes ahead
// of the player (spike governor) or SwapInput; governed sessions render
// an EVENT playlist fresh each time so reloads see re-planned segments.
func (s *Session) renderPlaylistLocked(t int) []byte {
	if s.plan == nil {
		if s.total > 0 {
			return mediaPlaylist(s.span(), s.segDur, s.container, s.segName)
		}
		done := make([]int, 0, len(s.done[0]))
		for n := range s.done[0] {
			done = append(done, n)
		}
		sort.Ints(done)
		return eventPlaylist(done, s.segDur, s.container, s.endSeg >= 0, s.segName)
	}
	discont := func(n int) bool { return s.discontBeforeLocked(t, n) }
	if s.total > 0 {
		if s.governorOn && t == 0 {
			return planPlaylist(s.plan, s.container, s.segName, discont, true, s.endSeg >= 0, 0)
		}
		if t == 0 {
			if s.playlist == nil {
				s.playlist = planPlaylist(s.plan, s.container, s.segName, discont, false, true, 0)
			}
			return s.playlist
		}
		return planPlaylist(s.plan, s.container, s.segName, discont, false, true, 0)
	}
	// Unknown duration: list the contiguous completed prefix.
	return planPlaylist(s.plan, s.container, s.segName, discont, true, s.endSeg >= 0, s.listedLocked(t))
}

// listedLocked advances and returns how many segments track t's
// unknown-duration EVENT playlist lists: the contiguous completed prefix.
// It never shrinks, so pruning segments far behind the player (pruneLocked)
// cannot remove them from the playlist, which EVENT playlists forbid.
func (s *Session) listedLocked(t int) int {
	if t < 0 || t >= len(s.done) {
		t = 0
	}
	for len(s.listed) <= t {
		s.listed = append(s.listed, 0)
	}
	done := s.done[t]
	for done[s.listed[t]] {
		s.listed[t]++
	}
	return s.listed[t]
}

// rampTranscodesLocked reports whether the ramp replaces the first
// segments of a copy stream with a quick transcode (only then does the
// switch back to copy need a discontinuity). fMP4 copy sessions instead
// keep copying (with shorter segments): their init segment would change
// across the switch.
func (s *Session) rampTranscodesLocked() bool {
	return s.decision.VideoCopy && s.container == ContainerMPEGTS && s.info.Video() != nil
}

// discontBeforeLocked: a discontinuity is needed exactly where the encode
// parameters the decoder sees change — ramp encode -> copy, and in/out of
// governor-clamped segments. Same-codec/same-resolution preset changes
// (transcode ramp) need none, matching ordinary seek restarts. Rung
// tracks (t > 0) are steady transcodes of one shape, so only the primary
// track can have discontinuities.
func (s *Session) discontBeforeLocked(t, n int) bool {
	if s.rtxDiscontinuities[n] {
		return true
	}
	if s.plan == nil || t != 0 || n <= 0 {
		return false
	}
	prev, cur := s.plan.ModeAt(n-1), s.plan.ModeAt(n)
	if prev == cur {
		return false
	}
	if prev == ModeFastStart && cur == ModeCopy {
		return s.rampTranscodesLocked()
	}
	return cur == ModeClamp || prev == ModeClamp
}

// runPlan is what one ffmpeg run starting at a segment must do.
type runPlan struct {
	decision  core.Decision
	hlsDur    time.Duration // -hls_time for this run
	fastStart bool
	mode      SegMode
	planEnd   int // exclusive; -1 = to the end (or unknown)
	rate      rateControl
}

// runPlanLocked resolves the plan at seg into a run: decision (primary
// decision, fast-start or clamp variant, or the wanted rung's), segment
// length for ffmpeg's muxer and the segment where the mode changes.
func (s *Session) runPlanLocked(seg int) runPlan {
	rp := runPlan{decision: s.trackDecisionLocked(s.wantTrack), hlsDur: s.segDur, planEnd: -1, rate: s.vbvRateLocked(s.wantTrack)}
	if s.isDASH() || s.plan == nil || s.wantTrack != 0 {
		return rp
	}
	rp.mode = s.plan.ModeAt(seg)
	rp.planEnd = s.plan.NextChange(seg)
	switch rp.mode {
	case ModeFastStart:
		rp.fastStart = true
		rp.hlsDur = s.plan.Dur(seg)
		if s.rampTranscodesLocked() {
			rp.decision = fastStartDecision(s.decision, s.info, s.adapt)
			rp.rate = rateControl{}
		}
	case ModeClamp:
		rp.decision = clampDecision(s.decision, s.info, s.plan.ClampKbps(seg))
		rp.rate = rateControl{}
	}
	// An in-session seek restart of a transcode (the playlist, and so the
	// segment lengths, are already fixed) still gets a fast start: the
	// first segments of the new run use the low-latency preset, then the
	// run chains into the steady preset. Copy sessions cannot do this
	// mid-playlist (the switch would need a discontinuity the player does
	// not have); they ramp when Plex restarts the session at the seek
	// offset, which is the usual seek path.
	if s.seekRampSeg == seg && s.adapt.FastStart && rp.mode == ModeTranscode && !rp.fastStart {
		rp.fastStart = true
		if end := seg + s.adapt.RampSegs; rp.planEnd < 0 || end < rp.planEnd {
			rp.planEnd = end
		}
	}
	return rp
}

// trackDecisionLocked is the steady decision of track t: an HLS rung's
// own decision, or for DASH the decision of the rung behind video
// track t.
func (s *Session) trackDecisionLocked(t int) core.Decision {
	if s.isDASH() {
		if r := rungForDashTrack(t); r >= 0 && r < len(s.rungs) {
			return s.rungs[r].decision
		}
		return s.decision
	}
	if t >= 0 && t < len(s.rungs) {
		return s.rungs[t].decision
	}
	return s.decision
}

// vbvRateLocked is track t's VBV rate plan for a new run. A DASH track is
// not a rung index (track 1 is audio); map it like trackDecisionLocked.
func (s *Session) vbvRateLocked(t int) rateControl {
	if s.isDASH() {
		t = rungForDashTrack(t)
	}
	if !s.adapt.VBV || t < 0 || t >= len(s.rungs) {
		return rateControl{}
	}
	r := s.rungs[t]
	if r.decision.VideoCopy || r.peakKbps <= 0 {
		return rateControl{}
	}
	maxrate := r.peakKbps
	if gp := s.goodput.Kbps(); gp > 0 {
		// Leave a modest delivery margin while allowing the reservoir to
		// spend bits on complex scenes.
		cap := gp * 9 / 10
		if cap > 0 && cap < maxrate {
			maxrate = cap
		}
	}
	if maxrate < 300 {
		maxrate = 300
	}
	seconds := s.adapt.VBVBufSeconds
	if seconds <= 0 {
		seconds = 8
	}
	return rateControl{Quality: true, MaxrateKbps: maxrate,
		BufsizeKbps: maxrate * seconds, TargetKbps: r.avgKbps}
}

// maybeGovernLocked re-plans upcoming copy segments as clamped transcodes
// when their bitrate spikes over the measured goodput (the clamp plan lives
// in Timeline.Clamp, segment_plan.go).
func (s *Session) maybeGovernLocked() {
	if !s.governorOn || s.plan == nil || s.goodput.Samples() < 2 || len(s.segSizes) == 0 {
		return
	}
	goodput := s.goodput.Kbps()
	if goodput <= 0 {
		return
	}
	// Only alter not-yet-requested segments in the encoder read-ahead
	// window. EVENT playlists let clients discover the revised plan.
	limit := s.aheadLimit()
	if limit <= 0 {
		limit = int(24 * time.Second / s.segDur)
	}
	maxSegs := limit
	if maxSegs < 2 {
		maxSegs = 2
	}
	margin := s.adapt.SpikeMargin
	if margin < 1 {
		margin = 1.25
	}
	for n := s.lastReq + 1; n <= s.lastReq+maxSegs; n++ {
		if s.plan.ModeAt(n) != ModeCopy || s.plan.ClampKbps(n) != 0 {
			continue
		}
		sz := s.segSizes[n]
		if sz <= 0 {
			continue
		}
		dur := s.plan.Dur(n)
		if dur <= 0 {
			dur = s.segDur
		}
		kbps := int(float64(sz*8) / dur.Seconds() / 1000)
		if float64(kbps) <= float64(goodput)*margin {
			continue
		}
		// Clamp total output below observed goodput; clampDecision retains
		// the source resolution and audio selection.
		clamp := goodput * 8 / 10
		if clamp < 300 {
			clamp = 300
		}
		s.plan.SetClamp(n, clamp)
		s.playlist = nil
		// Stop the active primary copy run at the new boundary. The run's
		// normal chaining path starts a clamp encode there; discard any copy
		// files already written at or beyond it so they cannot win a request
		// race before the replacement is complete.
		if r := s.cur; r != nil && r.track == 0 && r.mode == ModeCopy &&
			(r.planEnd < 0 || n < r.planEnd) {
			r.planEnd = n
			for produced := range s.done[0] {
				if produced >= n {
					delete(s.done[0], produced)
					_ = os.Remove(s.trackSegPath(0, produced))
				}
			}
		}
		s.log.Info("governed VBR spike", "segment", n, "sourceKbps", kbps,
			"goodputKbps", goodput, "clampKbps", clamp)
		break // one clamp run at a time; re-evaluate after it completes
	}
}

// ServeSegment implements core.HLSSession. n == InitSegment serves the fMP4
// init segment. For DASH sessions it serves the first representation.
func (s *Session) ServeSegment(w http.ResponseWriter, r *http.Request, n int) {
	if n == InitSegment {
		s.serveInit(w, r, 0)
		return
	}
	s.serveSegment(w, r, 0, n)
}

// serveSegment writes segment n of track t, waiting for ffmpeg and
// restarting it when n is behind or far ahead of the encoder.
func (s *Session) serveSegment(w http.ResponseWriter, r *http.Request, t, n int) {
	s.Touch()
	if t < 0 || t >= len(s.done) || n < 0 || (s.total > 0 && n >= s.total) {
		http.NotFound(w, r)
		return
	}
	timeout := time.NewTimer(s.m.opts.segmentTimeout)
	defer timeout.Stop()
	restarts := 0
	s.mu.Lock()
	s.reqSeq++
	seq := s.reqSeq
	s.mu.Unlock()
	for {
		if r.Context().Err() != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			http.Error(w, "session closed", http.StatusGone)
			return
		}
		stale := seq < s.movedBy
		if !stale && r.Method == http.MethodGet {
			s.lastReq = n
		}
		s.throttleLocked()
		done := s.done[t]
		// HLS only: ffmpeg's segmenter may cut slivers past the playlist's
		// last segment. DASH regroups chunks itself and never does.
		last := !s.isDASH() && s.total > 0 && n == s.total-1
		waitTail := false
		if done[n] && last && !s.tailSettledLocked(n) {
			// Wait for ffmpeg to finish the title so trailing slivers can be
			// appended (below).
			waitTail = true
		} else if done[n] {
			paths := []string{s.trackSegPath(t, n)}
			if s.container == ContainerMatroska {
				prev, served := s.mkvDelivery[t]
				seek := !served || (prev.segment != n-1 && prev.segment != n) || (prev.segment == n && prev.seek)
				if seek {
					// A fresh decoder needs the GOP covering n. Retrying
					// that request (including a Range) must keep its bytes.
					p, ok := s.decodableMKVLocked(t, n)
					if !ok {
						delete(done, n) // its GOP is gone: produce n again
						s.mu.Unlock()
						continue
					}
					paths[0] = p
				}
				// HEAD probes deliver no GOP to the decoder and cannot
				// make the next GET a continuation of this segment.
				if r.Method != http.MethodHead {
					if s.mkvDelivery == nil {
						s.mkvDelivery = make(map[int]mkvDelivery)
					}
					s.mkvDelivery[t] = mkvDelivery{segment: n, seek: seek}
				}
			}
			if last {
				// ffmpeg may cut a short final segment past the playlist's
				// last one; TS and fMP4 fragments concatenate cleanly.
				for k := n + 1; done[k]; k++ {
					paths = append(paths, s.trackSegPath(t, k))
				}
			}
			s.pruneLocked()
			s.mu.Unlock()
			if s.serveFiles(w, r, paths, t, n) {
				return
			}
			s.mu.Lock()
			delete(s.done[t], n) // vanished (pruned meanwhile): produce again
			s.mu.Unlock()
			continue
		}
		if s.inputFailed {
			s.mu.Unlock()
			writeStartError(w, errInputExhausted)
			return
		}
		if s.endSeg >= 0 && n > s.endSeg {
			s.mu.Unlock()
			http.NotFound(w, r)
			return
		}
		// Cached media requires no encoder work. A delayed request for an
		// old representation must not switch the current video encoder.
		// Audio shares the active video run and never chooses its rung.
		previousTrack := s.wantTrack
		if !waitTail && !stale && r.Context().Err() == nil && r.Method == http.MethodGet {
			if !s.isDASH() || t != s.audioTrackLocked() {
				s.wantTrack = t
				if s.isDASH() {
					s.videoRequested = true
				}
			}
		}
		if r.Method != http.MethodGet &&
			(s.cur == nil || s.cur.track != t) {
			s.mu.Unlock()
			http.Error(w, "rendition not active", http.StatusServiceUnavailable)
			return
		}
		var dwell *time.Timer
		if !waitTail && s.needsRestartLocked(n) {
			if stale {
				// Superseded by a seek: answer now instead of moving the
				// encoder away from where the player went. A player that
				// still wants this segment retries as a new request.
				s.mu.Unlock()
				w.Header().Set("Retry-After", "1")
				http.Error(w, "superseded by a newer request", http.StatusServiceUnavailable)
				return
			}
			if left := s.rungDwellLocked(r.Method == http.MethodGet && r.Header.Get("Range") == "", n); left > 0 {
				// Another rung's run is inside its dwell: wait it out
				// instead of restarting the encoder now.
				s.wantTrack = previousTrack // pending/canceled probes must not steer chaining
				dwell = time.NewTimer(left)
			} else {
				if restarts >= 2 {
					err := s.failErr
					s.mu.Unlock()
					s.log.Warn("segment unavailable after restarts", "segment", n, "err", err)
					http.Error(w, "segment unavailable", http.StatusBadGateway)
					return
				}
				restarts++
				s.seekRampSeg = n
				s.movedBy = seq
				if err := s.startRunLocked(n, s.failures > 0); err != nil {
					s.mu.Unlock()
					s.log.Error("start ffmpeg", "segment", n, "err", err)
					writeStartError(w, err)
					return
				}
			}
		}
		ch := s.changed
		s.mu.Unlock()
		var dwellC <-chan time.Time
		if dwell != nil {
			dwellC = dwell.C
		}
		select {
		case <-ch:
		case <-dwellC:
		case <-r.Context().Done():
			stopTimer(dwell)
			return
		case <-timeout.C:
			stopTimer(dwell)
			s.log.Warn("timed out waiting for segment", "segment", n)
			http.Error(w, "timed out waiting for segment", http.StatusGatewayTimeout)
			return
		}
		stopTimer(dwell)
	}
}

// decodableMKVLocked is the file to serve for Matroska segment n of track
// t to a fresh decoder: n itself, or for an empty Cluster (a segment
// inside a GOP longer than a segment, chunks.go) the earlier segment
// holding that GOP; false when it is no longer on disk.
func (s *Session) decodableMKVLocked(t, n int) (string, bool) {
	for k := n; k >= 0 && s.done[t][k]; k-- {
		p := s.trackSegPath(t, k)
		st, err := os.Stat(p)
		if err != nil {
			return "", false
		}
		if st.Size() > emptyClusterSize {
			return p, true
		}
	}
	return "", false
}

func stopTimer(t *time.Timer) {
	if t != nil {
		t.Stop()
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
	if r.track != s.wantTrack {
		return true // the client switched rungs: re-encode from n there
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

// rungDwellLocked is how much longer the current run must keep producing
// its track before a request on another track may replace it; zero when
// no switch is pending or the dwell has passed. Rungs share one encoder,
// so a player that probes every rendition's opening segments would stop
// the run on each probe and never let it get ahead. Waiting out the dwell
// bounds that to one restart per dwell; a real quality switch still
// retains the dwell for probes and recovery; progressing media downshifts
// can bypass it using completed delivery evidence.
func (s *Session) rungDwellLocked(media bool, n int) time.Duration {
	r := s.cur
	if r == nil || r.finished || r.stopped || r.err != nil || r.track == s.wantTrack || s.m.opts.rungDwell <= 0 {
		return 0
	}
	// Encoder production is not client buffer. Only a progressing media GET
	// on a lower rung can bypass probe protection, after actual delivery.
	// A slow completed delivery can justify downshifting after the first
	// segment; otherwise require two sequential deliveries. Recovery retains
	// the normal dwell. The client still chooses which rung to request.
	if media && s.deliveredTrack == r.track && s.deliveredCount > 0 &&
		n == s.deliveredSeg+1 {
		current, next := s.trackDecisionLocked(r.track), s.trackDecisionLocked(s.wantTrack)
		currentRate, nextRate := decisionTotalKbps(s.info, current), decisionTotalKbps(s.info, next)
		gp := s.goodput.Kbps()
		pressure := gp > 0 && gp < currentRate
		if nextRate > 0 && nextRate < currentRate && (pressure || s.deliveredCount >= 2) {
			return 0
		}
	}
	if left := s.m.opts.rungDwell - s.goodput.now().Sub(r.started); left > 0 {
		return left
	}
	return 0
}

// serveInit writes the init segment of track t, waiting for ffmpeg to
// write it.
func (s *Session) serveInit(w http.ResponseWriter, r *http.Request, t int) {
	s.Touch()
	if !hasInitSegment(s.container) || t < 0 || t >= len(s.done) {
		http.NotFound(w, r)
		return
	}
	timeout := time.NewTimer(s.m.opts.segmentTimeout)
	defer timeout.Stop()
	restarted := false
	for {
		if r.Context().Err() != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			http.Error(w, "session closed", http.StatusGone)
			return
		}
		if name := s.initFileLocked(t); name != "" {
			// Open under the lock: a newer run's init may replace (and
			// delete) this one as soon as the lock is released, and the
			// open handle keeps serving the bytes the lookup chose.
			f, err := os.Open(filepath.Join(s.dir, name))
			if err == nil && s.container == ContainerMatroska && r.Method != http.MethodHead {
				// An init GET starts a decoder. Its first media GET needs
				// a GOP even when it follows the previous decoder's last
				// segment; metadata-only HEAD probes do not reset it.
				delete(s.mkvDelivery, t)
			}
			s.mu.Unlock()
			if err == nil {
				defer f.Close()
				if s.serveOpened(w, r, f, t, InitSegment) {
					return
				}
			}
			http.Error(w, "init segment missing", http.StatusInternalServerError)
			return
		}
		if s.inputFailed {
			s.mu.Unlock()
			writeStartError(w, errInputExhausted)
			return
		}
		// A DASH player requests initialization before media. Selecting a
		// lazy video representation must start it here; waiting for its
		// first media request otherwise deadlocks startup. Audio shares the
		// current video run and must never select another video rung.
		if r.Method != http.MethodGet || r.Header.Get("Range") != "" {
			s.mu.Unlock()
			http.Error(w, "initialization not ready", http.StatusServiceUnavailable)
			return
		}
		previousTrack := s.wantTrack
		selectTrack := !s.isDASH() || t != s.audioTrackLocked()
		if selectTrack && !restarted {
			s.wantTrack = t
		}
		var dwell *time.Timer
		if (s.cur == nil || s.cur.finished || s.cur.stopped || (selectTrack && s.cur.track != t)) && !restarted {
			left := s.rungDwellLocked(false, s.lastReq)
			if s.isDASH() && selectTrack && !s.videoRequested && s.runSeq <= 1 {
				// No video was requested yet: the initial quality choice
				// has no active rendition to protect with a dwell.
				left = 0
			}
			if left > 0 {
				// A rendition probe for another rung's init segment waits
				// out the running rung's dwell (see rungDwellLocked).
				s.wantTrack = previousTrack // pending/canceled probes must not steer chaining
				dwell = time.NewTimer(left)
			} else {
				restarted = true
				start := s.lastReq
				// The rounded final DASH slot may contain only trailing
				// audio. An init-only rendition switch still needs video
				// headers, so start one slot earlier and let the slotter
				// publish the tail alias from that run.
				if s.isDASH() && s.total > 1 && start >= s.total-1 {
					start = s.total - 2
				}
				if start < 0 {
					start = 0
				}
				if err := s.startRunLocked(start, s.failures > 0); err != nil {
					s.mu.Unlock()
					writeStartError(w, err)
					return
				}
			}
		}
		ch := s.changed
		s.mu.Unlock()
		var dwellC <-chan time.Time
		if dwell != nil {
			dwellC = dwell.C
		}
		select {
		case <-ch:
		case <-dwellC:
		case <-r.Context().Done():
			stopTimer(dwell)
			return
		case <-timeout.C:
			stopTimer(dwell)
			http.Error(w, "timed out waiting for init segment", http.StatusGatewayTimeout)
			return
		}
		stopTimer(dwell)
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
func (s *Session) serveFiles(w http.ResponseWriter, r *http.Request, paths []string, t, n int) bool {
	if len(paths) == 1 {
		return s.serveFile(w, r, paths[0], t, n)
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
	armSegmentDeadline(w)
	s.serveCounted(w, r, filepath.Base(paths[0]), mod, bytes.NewReader(buf.Bytes()), t, n)
	return true
}

// serveCounted is http.ServeContent with delivery goodput measured over
// the write phase only (the wait for ffmpeg already happened).
func (s *Session) serveCounted(w http.ResponseWriter, r *http.Request, name string, mod time.Time, content io.ReadSeeker, t, n int) {
	if r.Method != http.MethodGet || n == InitSegment || r.Header.Get("Range") != "" {
		http.ServeContent(w, r, name, mod, content)
		return
	}
	size, err := content.Seek(0, io.SeekEnd)
	if err != nil {
		http.ServeContent(w, r, name, mod, content)
		return
	}
	if _, err = content.Seek(0, io.SeekStart); err != nil {
		return
	}
	cw := &countingWriter{ResponseWriter: w}
	start := s.goodput.now()
	http.ServeContent(cw, r, name, mod, content)
	dur := s.goodput.now().Sub(start)
	if cw.status != http.StatusOK || cw.err != nil || cw.bytes != size || size == 0 || r.Context().Err() != nil {
		return
	}
	if remoteDelivery(r) {
		s.goodput.add(cw.bytes, dur)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Audio, retries, stale representations and overlapping requests cannot
	// manufacture sequential video progress or drain another rung's buffer.
	if s.cur != nil && s.cur.track == t && (!s.isDASH() || t != s.audioTrackLocked()) {
		if s.deliveredCount == 0 || s.deliveredTrack != t || n != s.deliveredSeg+1 {
			if s.deliveredCount > 0 && s.deliveredTrack == t && n <= s.deliveredSeg {
				return
			}
			s.deliveredCount = 0
		}
		s.deliveredTrack, s.deliveredSeg = t, n
		s.deliveredCount++
	}
	// React to delivery evidence now, even when encoding is paused ahead.
	s.maybeGovernLocked()
}

// initFileLocked is the current init segment file of track t, relative
// to the session dir ("" none).
func (s *Session) initFileLocked(t int) string {
	if s.isDASH() {
		return s.dash.inits[t]
	}
	if t >= 0 && t < len(s.initFiles) && s.initFiles[t] != "" {
		return s.initFiles[t]
	}
	if t == 0 {
		return s.initFile
	}
	return ""
}

func (s *Session) setSegmentHeaders(w http.ResponseWriter) {
	ct := "video/mp2t"
	switch {
	case s.isDASH():
		ct = "application/octet-stream" // as PMS serves DASH segments
	case s.container == ContainerFMP4:
		ct = "video/mp4"
	case s.container == ContainerMatroska:
		ct = "video/x-matroska"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
}

// serveFile writes a finished segment; false when the file is gone.
func (s *Session) serveFile(w http.ResponseWriter, r *http.Request, path string, t, n int) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	return s.serveOpened(w, r, f, t, n)
}

// serveOpened writes segment n (or the init segment) from an open file.
func (s *Session) serveOpened(w http.ResponseWriter, r *http.Request, f *os.File, t, n int) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	s.setSegmentHeaders(w)
	name := filepath.Base(f.Name())
	if n == InitSegment {
		name = initSegmentName(s.container)
	}
	armSegmentDeadline(w)
	s.serveCounted(w, r, name, st.ModTime(), f, t, n)
	return true
}

// segmentWriteTimeout bounds delivering one finished segment. Servers have
// no WriteTimeout (direct play lasts hours), so without it a client that
// stops reading would hold the request goroutine and the open segment file
// (which Windows then cannot delete on session cleanup) forever. A segment
// that takes longer than this is far below playable bitrate anyway.
const segmentWriteTimeout = 5 * time.Minute

// armSegmentDeadline sets the connection write deadline just before a
// segment is written (never before waiting for ffmpeg).
func armSegmentDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(segmentWriteTimeout))
}

// throttleLocked keeps ffmpeg from running far ahead of the player, so a
// remux does not pull a whole title from Usenet up front: it suspends
// ffmpeg and resumes it as the player catches up. Where processes cannot be
// suspended (Windows) it stops ffmpeg instead and restarts it at the next
// missing segment in time.
func (s *Session) throttleLocked() {
	if s.m.opts.maxAhead <= 0 || s.info.Duration <= 0 {
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
	for t, done := range s.done {
		if s.total == 0 && !s.isDASH() {
			s.listedLocked(t) // keep pruned segments in the EVENT playlist
		}
		for n := range done {
			if n < s.lastReq-behind || (farAhead >= 0 && n > s.lastReq+farAhead) {
				if err := os.Remove(s.trackSegPath(t, n)); err == nil || errors.Is(err, os.ErrNotExist) {
					delete(done, n)
				}
			}
		}
	}
}

// startRunLocked stops the current run and starts ffmpeg at segment seg.
func (s *Session) startRunLocked(seg int, bigProbe bool) error {
	if s.closed {
		return errors.New("transcode: session closed")
	}
	if s.inputFailed {
		return errInputExhausted
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
	rp := s.runPlanLocked(seg)
	s.prepareRTXRunLocked(&rp, seg)
	s.seekRampSeg = -1
	spec := runSpec{
		RTXTimeline:  s.rtxTimeline,
		LiveInput:    s.liveInput,
		InputHeaders: s.inputHeaders,
		Input:        s.input,
		Info:         s.info,
		Decision:     rp.decision,
		Start:        s.segStart(seg),
		SegDur:       rp.hlsDur,
		StartSeg:     seg,
		Dir:          s.runDirLocked(),
		Playlist:     fmt.Sprintf("run-%d.m3u8", s.runSeq),
		InitName:     fmt.Sprintf("init-%d.mp4", s.runSeq),
		Accel:        accel,
		Features:     s.m.feat,
		BigProbe:     bigProbe,
		Protocol:     s.proto,
		FastStart:    rp.fastStart,
		Rate:         rp.rate,
	}
	if s.container == ContainerMatroska {
		spec.InitName = fmt.Sprintf("header-%d.mkv", s.runSeq)
	}
	if err := os.MkdirAll(spec.Dir, 0o755); err != nil {
		return fmt.Errorf("transcode: run dir: %w", err)
	}
	var dr *dashRun
	if s.isDASH() {
		spec.Dir = filepath.Join(s.dir, fmt.Sprintf("run-%d", s.runSeq))
		spec.Playlist = "manifest.mpd"
		spec.ChunkDur = dashChunkDuration
		if spec.ChunkDur > s.segDur/2 {
			spec.ChunkDur = s.segDur / 2
		}
		if err := os.MkdirAll(spec.Dir, 0o755); err != nil {
			return fmt.Errorf("transcode: run dir: %w", err)
		}
		// The muxer produces video (the wanted rung) then audio; map
		// those to their session tracks.
		var mapping []int
		if s.info.Video() != nil {
			mapping = append(mapping, s.wantTrack)
		}
		if findStream(s.info, spec.Decision.AudioStream, "audio") != nil {
			at := s.audioTrackLocked()
			if at < 0 {
				at = len(mapping)
			}
			mapping = append(mapping, at)
		}
		dr = newDASHRun(spec.Dir, mapping, s.base, s.segDur, s.total, seg)
	}
	var cr *chunkRun
	if s.chunkedLocked(spec.Decision) {
		spec.ChunkDir = filepath.Join(spec.Dir, fmt.Sprintf("c-%d", s.runSeq))
		spec.ChunkCut = chunkCutFor(s.segDur)
		if err := os.MkdirAll(spec.ChunkDir, 0o755); err != nil {
			return fmt.Errorf("transcode: chunk dir: %w", err)
		}
		if !spec.Decision.VideoCopy {
			// Force keyframes at the plan's boundaries (through the one
			// the run stops at), in the run's output clock.
			end := rp.planEnd
			if end < 0 || end > s.total {
				end = s.total
			}
			if end-seg > maxForcedKeySegments {
				end = seg + maxForcedKeySegments
				rp.planEnd = end
			}
			origin := time.Duration(0)
			if len(inputTimestampArgs(spec)) == 0 {
				origin = spec.Start // output timestamps count from -ss
			}
			for k := seg + 1; k <= end && k < s.total; k++ {
				spec.KeyTimes = append(spec.KeyTimes, s.segStart(k)-origin)
			}
		}
		sl := newSlotter(s.base, s.segDur, s.total, seg)
		plan, base := s.plan, s.base // boundaries never change after StartHLS
		sl.index = func(t time.Duration) int { return plan.IndexAt(t - base) }
		cr = &chunkRun{dir: spec.ChunkDir, container: s.container,
			initPath: filepath.Join(spec.Dir, spec.InitName), startAt: spec.Start, sl: sl}
	}
	r := &run{
		chunks:   cr,
		seq:      s.runSeq,
		startSeg: seg,
		spec:     spec,
		hw:       spec.usesHW(),
		maxDone:  -1,
		stderr:   newTailBuffer(40),
		started:  time.Now(),
		dash:     dr,
		mode:     rp.mode,
		planEnd:  rp.planEnd,
		track:    s.wantTrack,
	}
	if hint := s.m.opts.onRunStart; hint != nil {
		// The first segment needs its whole span, and a GOP either side.
		go hint(spec.Input, spec.Start, spec.Start+2*s.segDur)
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
		l = redactRTXLine(l, spec.Input, spec.InputHeaders)
		if strings.Contains(strings.ToLower(l), "resize: ngx-vsr ") {
			r.rtxConfirmed.Store(true)
		}
		r.stderr.add(l)
		runLog.Debug("ffmpeg", "line", l)
	}}
	var proc Process
	var err error
	if enhancementSelected(spec.Decision) {
		proc, args, err = s.startRTXRunLocked(spec, stdout, stderr)
		if err != nil {
			s.fallbackRTXLocked(err.Error(), seg)
			spec.Decision = s.trackDecisionLocked(s.wantTrack)
			r.spec = spec
			args = buildArgs(spec)
		} else {
			r.rtx = true
			r.hw = true
		}
	}
	if proc == nil {
		proc, err = s.m.runner.Start(Command{Path: s.m.ffmpeg, Args: args, Dir: s.dir, Stdout: stdout, Stderr: stderr})
	}
	if err != nil {
		if dr != nil {
			_ = os.RemoveAll(dr.dir)
		}
		if cr != nil {
			_ = os.RemoveAll(cr.dir)
		}
		return fmt.Errorf("transcode: start ffmpeg: %w", err)
	}
	r.proc = proc
	s.cur = r
	s.deliveredCount = 0
	runLog.Info("ffmpeg started", "segment", seg, "at", spec.Start, "kind", s.decision.Kind,
		"videoCopy", s.decision.VideoCopy, "audioCopy", s.decision.AudioCopy, "hw", r.hw)
	runLog.Debug("ffmpeg args", "args", strings.Join(redactedRTXArgs(args), " "))
	s.watchers.Add(1)
	go s.watch(r)
	s.broadcastLocked()
	return nil
}

// chunkedLocked reports whether an HLS run producing decision d must cut
// at every keyframe into chunks the session regroups onto the plan
// (chunks.go), because ffmpeg's segmenters cannot be told where to cut:
//   - the plan's boundaries are source keyframes (encodes force keyframes
//     at them);
//   - a copy without a keyframe index: wherever a GOP is longer than a
//     segment, ffmpeg would make fewer segments than the playlist lists.
//     Regrouped, every segment starts on a keyframe (or, inside a GOP
//     longer than a segment, repeats the GOP covering it).
//
// Encodes on a nominal plan force keyframes at its uniform boundaries,
// where ffmpeg cuts exactly.
func (s *Session) chunkedLocked(d core.Decision) bool {
	if s.isDASH() || s.plan == nil || s.info.Video() == nil {
		return false
	}
	return s.plan.Keyframed() || d.VideoCopy
}

// stopRunLocked kills the current run. ffmpeg exits in milliseconds after a
// kill, and the run's waiter never takes s.mu, so this cannot deadlock.
func (s *Session) stopRunLocked() {
	r := s.cur
	if r == nil {
		return
	}
	s.cur = nil
	if r.rtx && enhancementSelected(s.decision) {
		s.decision.Enhancement.State = "stopped"
		s.decision.Enhancement.Reason = "enhancement run stopped"
	}
	if !r.finished {
		r.stopped = true
		r.proc.Stop()
	}
}

// watch polls a run's output for finished segments until ffmpeg exits.
func (s *Session) watch(r *run) {
	defer s.watchers.Done()
	t := time.NewTicker(s.m.opts.pollInterval)
	defer t.Stop()
	playlist := filepath.Join(r.spec.Dir, r.spec.Playlist)
	collect := func(exited bool) {
		switch {
		case r.dash != nil:
			s.collectDASH(r, exited)
		case r.chunks != nil:
			s.collectChunks(r, exited)
		default:
			s.poll(r, playlist)
		}
	}
	for {
		select {
		case <-r.proc.Done():
			collect(true)
			s.finish(r)
			if r.dash != nil {
				_ = os.RemoveAll(r.dash.dir)
			}
			if r.chunks != nil {
				_ = os.RemoveAll(r.chunks.dir)
			}
			return
		case <-t.C:
			collect(false)
			s.removals.retry()
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
	t := r.track
	if t < 0 || t >= len(s.done) {
		t = 0
	}
	for _, n := range p.segments {
		if n < r.startSeg || (n <= r.maxDone && r.produced > 0) {
			continue
		}
		if s.total > 0 && n >= s.total+2 {
			continue // past any plausible trailing sliver
		}
		if r.planEnd > 0 && n >= r.planEnd {
			// Produced past this run's mode boundary before we could stop
			// it: drop the segment, the next run redoes it in its mode.
			_ = os.Remove(s.trackSegPath(t, n))
			continue
		}
		s.noteInitLocked(r, t)
		if r.produced == 0 {
			s.log.Info("first segment ready", "run", r.seq, "segment", n, "after", time.Since(r.started))
		}
		r.produced++
		r.maxDone = n
		s.done[t][n] = true
		if t == 0 {
			s.noteSegSizeLocked(n, s.trackSegPath(t, n))
		}
		changed = true
	}
	if changed {
		s.progressedLocked(r)
	}
}

// noteInitLocked publishes run r's init segment (fMP4 init or Matroska
// header) as track t's once ffmpeg has written it.
func (s *Session) noteInitLocked(r *run, t int) {
	if !hasInitSegment(s.container) {
		return
	}
	if _, err := os.Stat(filepath.Join(r.spec.Dir, r.spec.InitName)); err != nil {
		return
	}
	rel, err := filepath.Rel(s.dir, filepath.Join(r.spec.Dir, r.spec.InitName))
	if err != nil {
		return
	}
	for len(s.initFiles) <= t {
		s.initFiles = append(s.initFiles, "")
	}
	s.initFiles[t] = rel
	if t == 0 {
		s.initFile = rel
	}
}

// noteSegSizeLocked records a primary-track segment's size (governor).
func (s *Session) noteSegSizeLocked(n int, path string) {
	if s.segSizes == nil {
		s.segSizes = map[int]int64{}
	}
	if st, err := os.Stat(path); err == nil {
		s.segSizes[n] = st.Size()
	}
}

// progressedLocked reacts to run r finishing segments: failure counters,
// the governor, chaining at a plan mode boundary, throttling, waiters.
func (s *Session) progressedLocked(r *run) {
	if r.produced > 0 && s.cur == r {
		if r.rtx && r.rtxConfirmed.Load() {
			s.decision.Enhancement.State = "running"
			s.decision.Enhancement.Reason = "NVEncC NGX VSR produced enhanced segments"
		}
		s.failures = 0
		s.failErr = nil
	}
	s.maybeGovernLocked()
	// A run that reached its plan's mode boundary (fast-start ramp ->
	// steady, copy -> clamp, ...) stops there and the next mode's run
	// starts immediately, so the switch segment has no startup gap.
	if s.cur == r && r.planEnd > 0 && r.maxDone >= r.planEnd-1 && (s.total == 0 || r.planEnd < s.total) && s.endSeg < 0 {
		next := r.planEnd
		s.log.Debug("plan mode boundary; chaining run", "segment", next, "fromMode", r.mode)
		if err := s.startRunLocked(next, false); err != nil {
			s.log.Warn("chain run at mode boundary", "segment", next, "err", err)
		}
	}
	s.throttleLocked()
	s.broadcastLocked()
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
		if r.rtx && s.cur == r {
			s.decision.Enhancement.State = "stopped"
			s.decision.Enhancement.Reason = "enhancement run completed"
		}
		// A run bounded by a plan mode boundary that produced up to it
		// has not hit the end of the input: chain the next mode's run
		// when the player is in (or near) this run's range.
		if r.planEnd > 0 && r.produced > 0 && r.maxDone >= r.planEnd-1 && (s.total == 0 || r.planEnd < s.total) && s.cur == r {
			if s.lastReq >= r.startSeg {
				log.Debug("ffmpeg finished at plan boundary; chaining", "segment", r.planEnd)
				if err := s.startRunLocked(r.planEnd, false); err != nil {
					s.failErr = err
				}
				return
			}
		}
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
	if r.rtx && r.produced == 0 && !s.inputFailed {
		s.fallbackRTXLocked("native VSR run failed before producing segments", r.startSeg)
		if err := s.startRunLocked(r.startSeg, r.spec.BigProbe); err != nil {
			s.failErr = err
		}
		return
	}
	if r.rtx {
		s.decision.Enhancement.State = "fallback"
		s.decision.Enhancement.Reason = "native VSR input/process failed"
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
	if s.inputFailed {
		return
	}
	s.inputFailed = true
	if h := s.m.opts.onInputFailure; h != nil {
		at, input, ferr := s.segStart(r.next()), s.input, s.failErr
		go h(s.id, input, at, ferr)
	} else if h := s.m.opts.onFailure; h != nil {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("transcode: session closed")
	}
	seg := 0
	if at > s.base {
		if s.plan != nil && !s.isDASH() {
			seg = s.plan.IndexAt(at - s.base)
		} else {
			seg = int((at - s.base) / s.segDur)
		}
	}
	if s.total > 0 && seg >= s.total {
		seg = s.total - 1
	}
	s.stopRunLocked()
	for t, done := range s.done {
		for n := range done {
			if n >= seg {
				_ = os.Remove(s.trackSegPath(t, n))
				delete(done, n)
			}
		}
	}
	next := adaptDecision(s.decision, s.info, info, s.container)
	if inputURL == s.input {
		next.AudioStream = s.decision.AudioStream
		next.SubtitleStream = s.decision.SubtitleStream
	}
	if next.Enhancement.Backend == rtxBackend && next.Enhancement.State == "stopped" && next.Enhancement.Width > 0 {
		next.Enhancement.State = "selected"
	}
	if s.isDASH() {
		next = dashDecision(info, next)
	}
	if next.Kind != s.decision.Kind || next.VideoCopy != s.decision.VideoCopy || next.AudioCopy != s.decision.AudioCopy {
		s.log.Info("failover changes decision", "from", s.decision.Kind, "to", next.Kind, "reason", next.Reason)
	}
	// Keep the ladder's track layout; adapt every rung like the primary.
	if len(s.rungs) > 0 {
		s.rungs[0].decision = next
		for i := 1; i < len(s.rungs); i++ {
			old := s.rungs[i].decision
			nd := adaptDecision(old, s.info, info, s.container)
			if s.isDASH() {
				nd = dashDecision(info, nd)
			}
			nd.VideoCopy = false
			nd.Kind = core.DecisionTranscode
			nd.MaxHeight, nd.BitrateKbps = old.MaxHeight, old.BitrateKbps
			s.rungs[i].decision = nd
		}
	}
	if s.input != inputURL {
		// SwapInput has no replacement-credential parameter. Never carry an
		// old provider's credentials onto an independently resolved URL.
		s.inputHeaders = nil
	}
	s.input = inputURL
	s.info = info
	s.decision = next
	s.endSeg = -1
	s.failures = 0
	s.failErr = nil
	s.inputFailed = false
	s.lastReq = seg
	s.seekRampSeg = seg
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
		oldRange := dynamicRange(oldV)
		if old.StripDolbyVision {
			oldRange = rangeHDR10 // the decoder holds HDR10, not source DV headers
		}
		if d.VideoCopy {
			sameRange := oldRange == dynamicRange(newV) ||
				(dynamicRange(newV) == rangeDV && rangeOfDVBase(newV) == oldRange)
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
				d.ToneMap = encodedRange(newV) != rangeSDR && !isHDR(oldV)
				notes = append(notes, fmt.Sprintf("video %s %dp re-encoded to match the stream", newV.Codec, newV.Height))
			}
		} else {
			keptHDR := isHDR(oldV) && !old.ToneMap
			d.ToneMap = encodedRange(newV) != rangeSDR && !keptHDR
		}
		d.StripDolbyVision = d.VideoCopy && oldRange == rangeHDR10 && newV.DolbyVision && rangeOfDVBase(newV) == rangeHDR10
	}
	var audioNote string
	d, audioNote = preserveAudio(d, oldA, newA)
	if audioNote != "" {
		notes = append(notes, audioNote)
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
	// The watcher may still be assembling segment files; on Windows open
	// files can't be deleted and late writes would outlive RemoveAll.
	waitTimeout(&s.watchers, 10*time.Second)
	err := removeAllRetry(s.dir)
	s.m.forget(s)
	s.log.Info("session closed", "age", time.Since(s.created).Round(time.Second))
	return err
}
