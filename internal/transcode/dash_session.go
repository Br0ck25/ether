package transcode

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ether-media/ether/internal/core"
)

// dashState is what a DASH session adds to Session: its representations
// and their current init segments. Guarded by Session.mu.
type dashState struct {
	reps     []dashRep
	initURL  string
	mediaURL string
	inits    []string // per track: current init segment file in the session dir
	codecs   []string // per track: codecs read from that init segment
	mpd      []byte   // cached once every track's codecs are known
}

func newDASHState(reps []dashRep, urls DASHURLs, sessionID string) *dashState {
	if urls == nil {
		urls = PlexDASHURLs
	}
	initURL, mediaURL := urls(sessionID)
	return &dashState{
		reps:     reps,
		initURL:  initURL,
		mediaURL: mediaURL,
		inits:    make([]string, len(reps)),
		codecs:   make([]string, len(reps)),
	}
}

// dashRun is one ffmpeg run's chunk bookkeeping, owned by its watch
// goroutine.
type dashRun struct {
	dir    string
	tracks []*dashTrack
}

// dashTrack follows one representation's chunks in a run directory.
type dashTrack struct {
	rep       int
	next      int // next chunk number ffmpeg will write (from 1)
	init      []byte
	info      trackInit
	published bool // init copied into the session
	sl        *slotter
}

func newDASHRun(dir string, tracks int, base, segDur time.Duration, total, startSeg int) *dashRun {
	dr := &dashRun{dir: dir}
	for t := 0; t < tracks; t++ {
		dr.tracks = append(dr.tracks, &dashTrack{rep: t, next: 1, sl: newSlotter(base, segDur, total, startSeg)})
	}
	return dr
}

// dashDecision makes a decision streamable as DASH: fMP4 segments with
// codecs browsers decode through Media Source Extensions.
func dashDecision(info core.MediaInfo, d core.Decision) core.Decision {
	if d.Container != "" {
		d.Reason = strings.Replace(d.Reason, "remux to HLS ("+d.Container+")", "remux to DASH (fmp4)", 1)
	}
	d.Container = ContainerFMP4
	if v := info.Video(); v != nil && d.VideoCopy && !dashVideoOK(v.Codec) {
		d.VideoCopy = false
		d.VideoCodec = "h264"
		if d.MaxHeight <= 0 || d.MaxHeight > 1080 {
			d.MaxHeight = min(max(v.Height, 0), 1080)
		}
		d.ToneMap = isHDR(v)
		d.BitrateKbps = 0
		d.Reason += fmt.Sprintf("; DASH: video %s re-encoded to h264", v.Codec)
	}
	if a := findStream(info, d.AudioStream, "audio"); a != nil {
		if d.AudioCopy && !dashAudioOK(a.Codec) {
			d.AudioCopy = false
			d.AudioCodec = ""
			d.Reason += fmt.Sprintf("; DASH: audio %s re-encoded to aac", a.Codec)
		}
		if !d.AudioCopy && !dashAudioOK(d.AudioCodec) {
			d.AudioCodec = "aac"
		}
	}
	if !d.VideoCopy {
		d.Kind = core.DecisionTranscode
	}
	return d
}

// ServeManifest implements core.DASHSession: the static, full-length MPD.
// The first request waits (bounded) for ffmpeg's init segments so the MPD
// carries exact codecs strings; until then they are estimated.
func (s *Session) ServeManifest(w http.ResponseWriter, r *http.Request) {
	s.Touch()
	if !s.isDASH() {
		http.NotFound(w, r)
		return
	}
	wait := time.NewTimer(s.m.opts.manifestWait)
	defer wait.Stop()
	timedOut, restarted := false, false
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			http.Error(w, "session closed", http.StatusGone)
			return
		}
		body := s.dash.mpd
		if body == nil {
			complete := true
			for _, c := range s.dash.codecs {
				if c == "" {
					complete = false
				}
			}
			idle := s.cur == nil || s.cur.finished
			if idle && !restarted && !complete {
				restarted = true
				if err := s.startRunLocked(s.lastReq, s.failures > 0); err == nil {
					idle = false
				}
			}
			if complete || timedOut || idle {
				body = s.renderMPDLocked()
				if complete {
					s.dash.mpd = body
				}
			}
		}
		if body != nil {
			s.mu.Unlock()
			h := w.Header()
			h.Set("Content-Type", "application/dash+xml")
			h.Set("Cache-Control", "no-cache")
			h.Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write(body)
			}
			return
		}
		ch := s.changed
		s.mu.Unlock()
		select {
		case <-ch:
		case <-r.Context().Done():
			return
		case <-wait.C:
			timedOut = true
		}
	}
}

// renderMPDLocked renders the MPD with the best codecs known.
func (s *Session) renderMPDLocked() []byte {
	reps := append([]dashRep(nil), s.dash.reps...)
	for i := range reps {
		if c := s.dash.codecs[i]; c != "" {
			reps[i].Codecs = c
		}
	}
	return renderMPD(s.info.Duration, s.segDur, s.dash.initURL, s.dash.mediaURL, reps)
}

// ServeInit implements core.DASHSession: representation rep's
// initialization segment.
func (s *Session) ServeInit(w http.ResponseWriter, r *http.Request, rep int) {
	s.serveInit(w, r, rep)
}

// ServeMedia implements core.DASHSession: media segment n of
// representation rep. Each segment starts with its run's init segment, so
// segments from different ffmpeg runs (seeks, failovers) play back to back.
func (s *Session) ServeMedia(w http.ResponseWriter, r *http.Request, rep, n int) {
	s.serveSegment(w, r, rep, n)
}

// collectDASH picks up the chunks a DASH run has finished, regroups them
// into numbered segments and publishes those. exited is true for the final
// pass after ffmpeg exited.
func (s *Session) collectDASH(r *run, exited bool) {
	s.mu.Lock()
	skip := s.closed || (r.stopped && s.cur != r)
	stopped := r.stopped
	s.mu.Unlock()
	if skip {
		return
	}
	dr := r.dash
	type initPub struct {
		t    int
		file string
		info trackInit
	}
	type segPub struct {
		t    int
		out  slotOut
		file string
	}
	var inits []initPub
	var segs []segPub
	eof := false
	if exited {
		eof = !stopped && r.proc.Err() == nil
	}
	for t, tr := range dr.tracks {
		for {
			chunk := filepath.Join(dr.dir, dashChunkFile(tr.rep, tr.next))
			if _, err := os.Stat(chunk); err != nil {
				break
			}
			if tr.init == nil {
				// The muxer writes the init segment before the first chunk.
				b, err := os.ReadFile(filepath.Join(dr.dir, dashInitFile(tr.rep)))
				if err != nil {
					break
				}
				info, err := parseInit(b)
				if err != nil {
					s.log.Warn("unreadable DASH init segment", "run", r.seq, "rep", tr.rep, "err", err)
					break
				}
				tr.init, tr.info = b, info
			}
			if !tr.published {
				tr.published = true
				name := fmt.Sprintf("init-%d-%d.mp4", t, r.seq)
				if err := os.WriteFile(filepath.Join(s.dir, name), tr.init, 0o644); err == nil {
					inits = append(inits, initPub{t: t, file: name, info: tr.info})
				}
			}
			c, err := scanChunk(chunk, tr.info)
			if err != nil {
				// Chunks are renamed into place once complete, so this is
				// a muxer bug or a full disk; skip rather than stall.
				s.log.Warn("unreadable DASH chunk", "run", r.seq, "rep", tr.rep, "chunk", tr.next, "err", err)
				tr.next++
				continue
			}
			tr.next++
			for _, o := range tr.sl.add(c) {
				segs = append(segs, segPub{t: t, out: o})
			}
		}
		if exited {
			for _, o := range tr.sl.flush(eof) {
				segs = append(segs, segPub{t: t, out: o})
			}
		}
	}
	// Assemble segment files outside the lock.
	for i := range segs {
		sp := &segs[i]
		if sp.out.alias >= 0 {
			continue
		}
		file := filepath.Join(dr.dir, fmt.Sprintf("seg-%d-%05d.m4s", sp.t, sp.out.n))
		if err := writeSegmentFile(file, dr.tracks[sp.t].init, sp.out.chunks); err != nil {
			s.log.Warn("assemble DASH segment", "segment", sp.out.n, "rep", sp.t, "err", err)
			continue
		}
		sp.file = file
		for _, c := range sp.out.chunks {
			_ = os.Remove(c.path)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || (r.stopped && s.cur != r) {
		for _, in := range inits {
			_ = os.Remove(filepath.Join(s.dir, in.file))
		}
		return
	}
	changed := false
	for _, in := range inits {
		old := s.dash.inits[in.t]
		s.dash.inits[in.t] = in.file
		s.dash.codecs[in.t] = in.info.Codecs
		if old != "" && old != in.file {
			_ = os.Remove(filepath.Join(s.dir, old))
		}
		changed = true
	}
	for _, sp := range segs {
		done := s.done[sp.t]
		n := sp.out.n
		if done[n] {
			continue // an earlier run's copy is as good
		}
		dst := s.trackSegPath(sp.t, n)
		if sp.out.alias >= 0 {
			if !done[sp.out.alias] || linkOrCopy(s.trackSegPath(sp.t, sp.out.alias), dst) != nil {
				continue
			}
		} else if sp.file == "" || os.Rename(sp.file, dst) != nil {
			continue
		}
		done[n] = true
		changed = true
	}
	produced, maxDone := -1, -1
	for _, tr := range dr.tracks {
		if produced < 0 || tr.sl.count < produced {
			produced, maxDone = tr.sl.count, tr.sl.last
		} else if tr.sl.count == produced && tr.sl.last < maxDone {
			maxDone = tr.sl.last
		}
	}
	if produced > r.produced {
		r.produced, r.maxDone = produced, maxDone
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

// writeSegmentFile writes a self-initializing segment to path.
func writeSegmentFile(path string, init []byte, chunks []fmp4Chunk) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := writeSegment(f, init, chunks); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

// linkOrCopy makes dst a copy of src (a hard link where possible).
func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
