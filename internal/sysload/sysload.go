// Package sysload samples this machine's load for the owner Overview: CPU,
// memory, network throughput, GPU (when the platform reports it) and
// Ether's own CPU and memory. A background loop keeps a short history so
// the page can draw recent trends the moment it opens.
//
// CPU, memory, network and process figures come from gopsutil, which is
// pure Go on Linux, macOS and Windows. GPU figures are best effort (see
// gpu.go) and are only collected while someone is looking, because some
// platforms need a helper process per reading.
package sysload

import (
	"context"
	"log/slog"
	"runtime"
	"sync"
	"time"
)

// Sample is one reading. Rates and percentages cover the interval since
// the previous reading; percentages are of the whole machine (all cores).
type Sample struct {
	At       time.Time `json:"at"`
	CPU      float64   `json:"cpu"`
	MemUsed  uint64    `json:"memUsed"`
	NetRxBps float64   `json:"netRxBps"`
	NetTxBps float64   `json:"netTxBps"`
	ProcCPU  float64   `json:"procCpu"`
	ProcRSS  uint64    `json:"procRss"`
	// GPU is the busiest GPU's utilisation, when known.
	GPU *float64 `json:"gpu,omitempty"`
}

// GPU is one graphics adapter's current state. Unknown figures are nil or 0.
type GPU struct {
	Name string `json:"name"`
	// Util is overall utilisation in percent.
	Util *float64 `json:"util,omitempty"`
	// Encoder and Decoder are the video engines' utilisation (NVIDIA).
	Encoder  *float64 `json:"encoder,omitempty"`
	Decoder  *float64 `json:"decoder,omitempty"`
	MemUsed  uint64   `json:"memUsed,omitempty"`
	MemTotal uint64   `json:"memTotal,omitempty"`
}

// Snapshot is what the Overview shows.
type Snapshot struct {
	Cores     int       `json:"cores"`
	MemTotal  uint64    `json:"memTotal"`
	SwapUsed  uint64    `json:"swapUsed"`
	SwapTotal uint64    `json:"swapTotal"`
	Load      []float64 `json:"load,omitempty"` // 1, 5 and 15 minutes; not on Windows
	// IntervalSeconds is the spacing of History.
	IntervalSeconds float64  `json:"intervalSeconds"`
	Current         *Sample  `json:"current,omitempty"`
	History         []Sample `json:"history"`
	GPUs            []GPU    `json:"gpus"`
	Error           string   `json:"error,omitempty"`
}

// Counters are cumulative readings; Sampler turns them into rates.
type Counters struct {
	CPUBusy, CPUTotal float64 // seconds across all cores
	MemUsed, MemTotal uint64
	SwapUsed          uint64
	SwapTotal         uint64
	NetRx, NetTx      uint64  // bytes on physical interfaces
	ProcCPU           float64 // this process's CPU seconds
	ProcRSS           uint64
	Load              []float64
}

// Source reads the machine. The default reads the real one; tests fake it.
type Source interface {
	Counters(ctx context.Context) (Counters, error)
	GPUs(ctx context.Context) ([]GPU, error)
}

// Options configure a Sampler.
type Options struct {
	Source Source
	// Interval between readings (default 2s).
	Interval time.Duration
	// History is how many readings to keep (default 150: five minutes).
	History int
	// GPUInterval spaces GPU readings (default 5s).
	GPUInterval time.Duration
	// GPUIdle stops GPU readings this long after the last Snapshot
	// (default 1m).
	GPUIdle time.Duration
	Logger  *slog.Logger
	Now     func() time.Time
}

// Sampler keeps recent readings.
type Sampler struct {
	opts Options

	mu        sync.Mutex
	prev      *Counters
	prevAt    time.Time
	last      Counters
	history   []Sample
	gpus      []GPU
	gpuAt     time.Time
	gpuBusy   bool
	viewedAt  time.Time
	err       error
	loggedErr bool
}

// New builds a Sampler; call Run to start sampling.
func New(opts Options) *Sampler {
	if opts.Source == nil {
		opts.Source = hostSource{}
	}
	if opts.Interval <= 0 {
		opts.Interval = 2 * time.Second
	}
	if opts.History <= 0 {
		opts.History = 150
	}
	if opts.GPUInterval <= 0 {
		opts.GPUInterval = 5 * time.Second
	}
	if opts.GPUIdle <= 0 {
		opts.GPUIdle = time.Minute
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Sampler{opts: opts}
}

// Run samples until ctx ends.
func (s *Sampler) Run(ctx context.Context) {
	t := time.NewTicker(s.opts.Interval)
	defer t.Stop()
	s.Sample(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sample(ctx)
		}
	}
}

// Sample takes one reading now.
func (s *Sampler) Sample(ctx context.Context) {
	rctx, cancel := context.WithTimeout(ctx, s.opts.Interval)
	defer cancel()
	c, err := s.opts.Source.Counters(rctx)
	now := s.opts.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
	if err != nil {
		if !s.loggedErr {
			s.opts.Logger.Warn("system load unavailable", "err", err)
			s.loggedErr = true
		}
		return
	}
	s.loggedErr = false
	s.last = c
	if s.prev != nil {
		if dt := now.Sub(s.prevAt).Seconds(); dt > 0 {
			s.history = append(s.history, s.derive(*s.prev, c, dt, now))
			if over := len(s.history) - s.opts.History; over > 0 {
				s.history = append(s.history[:0], s.history[over:]...)
			}
		}
	}
	s.prev, s.prevAt = &c, now
	s.maybeGPULocked(ctx, now)
}

func (s *Sampler) derive(a, b Counters, dt float64, now time.Time) Sample {
	smp := Sample{At: now, MemUsed: b.MemUsed, ProcRSS: b.ProcRSS}
	if total := b.CPUTotal - a.CPUTotal; total > 0 {
		smp.CPU = clampPct((b.CPUBusy - a.CPUBusy) / total * 100)
		// The process's CPU seconds against every core's elapsed time.
		smp.ProcCPU = clampPct((b.ProcCPU - a.ProcCPU) / total * 100)
	}
	smp.NetRxBps = rate(a.NetRx, b.NetRx, dt)
	smp.NetTxBps = rate(a.NetTx, b.NetTx, dt)
	smp.GPU = busiest(s.gpus)
	return smp
}

// maybeGPULocked starts a background GPU reading when someone has looked
// recently and the last reading is stale. Readings may spawn a helper
// (nvidia-smi, ioreg), so they never block sampling.
func (s *Sampler) maybeGPULocked(ctx context.Context, now time.Time) {
	if s.gpuBusy || now.Sub(s.viewedAt) > s.opts.GPUIdle || now.Sub(s.gpuAt) < s.opts.GPUInterval {
		return
	}
	s.gpuBusy = true
	go func() {
		gctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		gpus, err := s.opts.Source.GPUs(gctx)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.gpuBusy = false
		s.gpuAt = s.opts.Now()
		if err == nil {
			s.gpus = gpus
		}
	}()
}

// Snapshot returns the current state and history, and keeps GPU readings
// running for a while.
func (s *Sampler) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.Now()
	wasIdle := now.Sub(s.viewedAt) > s.opts.GPUIdle
	s.viewedAt = now
	if wasIdle {
		s.maybeGPULocked(context.Background(), now)
	}
	out := Snapshot{
		Cores:           runtime.NumCPU(),
		MemTotal:        s.last.MemTotal,
		SwapUsed:        s.last.SwapUsed,
		SwapTotal:       s.last.SwapTotal,
		Load:            s.last.Load,
		IntervalSeconds: s.opts.Interval.Seconds(),
		History:         append([]Sample{}, s.history...),
		GPUs:            append([]GPU{}, s.gpus...),
	}
	if n := len(out.History); n > 0 {
		cur := out.History[n-1]
		cur.GPU = busiest(s.gpus)
		out.Current = &cur
	}
	if s.err != nil {
		out.Error = s.err.Error()
	}
	return out
}

func rate(a, b uint64, dt float64) float64 {
	if b < a { // counter reset (interface went down, wrapped)
		return 0
	}
	return float64(b-a) / dt
}

func clampPct(v float64) float64 {
	return max(0, min(100, v))
}

func busiest(gpus []GPU) *float64 {
	var best *float64
	for _, g := range gpus {
		if g.Util != nil && (best == nil || *g.Util > *best) {
			v := *g.Util
			best = &v
		}
	}
	return best
}
