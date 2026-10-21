package transcode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// startRTXRunLocked either returns a native backend process or records an
// explicit fallback and lets the caller start ordinary FFmpeg. The permit is
// released only after the OS process is reaped, including start failures.
func (s *Session) startRTXRunLocked(spec runSpec, stdout, stderr *lineWriter) (Process, []string, error) {
	if _, reason := normalizedRTX(s.rtxConfig); reason != "" {
		return nil, nil, fmt.Errorf("RTX Video: %s", reason)
	}
	if s.rtxConfig.Mode == "off" || s.rtxConfig.Mode == "" {
		return nil, nil, fmt.Errorf("RTX Video disabled by owner")
	}
	if v := spec.Info.Video(); v == nil || v.Height > s.rtxConfig.SourceMaxHeight || spec.Decision.Enhancement.Height > s.rtxConfig.TargetMaxHeight {
		return nil, nil, fmt.Errorf("RTX Video source or target exceeds owner limits")
	}
	args, keys, err := buildRTXArgs(spec)
	if err != nil {
		return nil, nil, err
	}
	b := &s.m.rtx
	b.mu.Lock()
	if !b.support.Available || b.path == "" {
		b.mu.Unlock()
		return nil, nil, fmt.Errorf("RTX Video backend unavailable")
	}
	if b.active >= s.rtxConfig.MaxConcurrent {
		b.mu.Unlock()
		return nil, nil, fmt.Errorf("RTX Video enhancement concurrency cap reached")
	}
	b.active++
	path := b.path
	b.mu.Unlock()
	release := func() { b.mu.Lock(); b.active--; b.mu.Unlock() }
	if err := os.WriteFile(filepath.Join(spec.Dir, "rtx-keyframes.txt"), keys, 0o600); err != nil {
		release()
		return nil, nil, fmt.Errorf("RTX Video keyframe plan: %w", err)
	}
	start := s.m.runner.Start
	if owner, ok := s.m.runner.(interface {
		StartRTX(Command) (Process, error)
	}); ok {
		start = owner.StartRTX
	}
	p, err := start(Command{Path: path, Args: args, Dir: s.dir, Stdout: stdout, Stderr: stderr})
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("RTX Video executable failed to start")
	}
	held := &rtxPermitProcess{Process: p, done: make(chan struct{}), release: release}
	go func() { <-p.Done(); held.once.Do(release); close(held.done) }()
	s.decision.Enhancement.State = "starting"
	s.decision.Enhancement.Reason = "waiting for enhanced segments from NVEncC/NGX"
	return held, args, nil
}

type rtxPermitProcess struct {
	Process
	done    chan struct{}
	once    sync.Once
	release func()
}

func (p *rtxPermitProcess) Done() <-chan struct{} { return p.done }
func (p *rtxPermitProcess) Stop()                 { p.Process.Stop(); <-p.done }

// fallbackRTXLocked removes the enhancement plan from every rendition and
// refreshes manifest dimensions. It never labels an ordinary scaler RTX VSR.
func (s *Session) fallbackRTXLocked(reason string, seg int) {
	old := s.decision.Enhancement
	if old.Backend != rtxBackend {
		return
	}
	s.decision.Enhancement = core.VideoEnhancement{Backend: rtxBackend, State: "fallback", Reason: reason}
	s.decision.Reason += "; RTX Video fallback: " + reason
	v := s.info.Video()
	if v != nil {
		s.decision.MaxHeight = minPositive(s.decision.MaxHeight, v.Height)
	}
	for i := range s.rungs {
		d := s.rungs[i].decision
		d.Enhancement = s.decision.Enhancement
		if v != nil {
			d.MaxHeight = minPositive(d.MaxHeight, v.Height)
			s.rungs[i].width, s.rungs[i].height = decisionOutputSize(v, d)
		}
		s.rungs[i].decision = d
		s.rungs[i].codecs = rungCodecs(s.info, d, s.rungs[i].height)
	}
	if seg > 0 {
		if s.rtxDiscontinuities == nil {
			s.rtxDiscontinuities = map[int]bool{}
		}
		s.rtxDiscontinuities[seg] = true
	}
	s.playlist = nil
	if s.dash != nil {
		// Transport shape/track selection is unchanged; only video dimensions
		// change. collectDASH publishes this run's new initialization segment.
		for i := range s.dash.reps {
			if s.dash.reps[i].Kind == "video" && v != nil {
				s.dash.reps[i].Width, s.dash.reps[i].Height = decisionOutputSize(v, s.decision)
			}
		}
		s.dash.mpd = nil
	}
	s.log.Warn("RTX Video fallback", "reason", reason)
}

func redactRTXLine(line, input string, headers map[string]string) string {
	if input != "" {
		line = strings.ReplaceAll(line, input, "[source]")
	}
	for _, value := range headers {
		if value != "" {
			line = strings.ReplaceAll(line, value, "[redacted]")
		}
	}
	return line
}

// Preserve optional runner diagnostics through the concurrency wrapper.
func (p *rtxPermitProcess) PID() int {
	if x, ok := p.Process.(interface{ PID() int }); ok {
		return x.PID()
	}
	return 0
}
func (p *rtxPermitProcess) CPUTime() (time.Duration, time.Duration) {
	if x, ok := p.Process.(interface {
		CPUTime() (time.Duration, time.Duration)
	}); ok {
		return x.CPUTime()
	}
	return 0, 0
}
