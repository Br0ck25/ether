package usenet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ether-media/ether/internal/config"
	"github.com/ether-media/ether/internal/core"
	"github.com/ether-media/ether/internal/yenc"
)

// pool holds the connections of one provider. At most cfg.Connections
// connections exist at any time: every connection is either idle or held by
// the request that owns one of the priority semaphore's slots, and new
// connections are only dialled by a slot holder when no idle one is left.
type pool struct {
	key string // unique per pool instance (provider id + generation)
	cfg config.UsenetProvider
	o   *Options
	log *slog.Logger
	sem *prioSem
	brk *breaker

	mu     sync.Mutex
	warmMu sync.Mutex
	idle   []*Conn // LIFO: the most recently used connection is reused first
	live   int     // idle + in use + being dialled
	closed bool
	// size is the configured connection count; limitedUntil is set while
	// the semaphore runs smaller because the server refused connections.
	size         int
	limitedUntil time.Time

	stop chan struct{}
	wg   sync.WaitGroup

	bytes         atomic.Int64
	articles      atomic.Int64
	misses        atomic.Int64
	errs          atomic.Int64
	dials         atomic.Int64
	connSeq       atomic.Int64
	tcpTime       atomic.Int64
	tlsTime       atomic.Int64
	greetingTime  atomic.Int64
	authTime      atomic.Int64
	statBatches   atomic.Int64
	statBatchTime atomic.Int64

	latMu    sync.Mutex
	latency  ewma // command round trip
	fetching ewma // whole article
}

type ewma struct {
	v float64
	n int64
}

func (e *ewma) add(d time.Duration) {
	const alpha = 0.1
	if e.n == 0 {
		e.v = float64(d)
	} else {
		e.v += alpha * (float64(d) - e.v)
	}
	e.n++
}

func newPool(key string, cfg config.UsenetProvider, o *Options, log *slog.Logger) *pool {
	size := cfg.Connections
	if size <= 0 {
		size = DefaultConnections
	}
	p := &pool{
		size: size,
		key:  key,
		cfg:  cfg,
		o:    o,
		log:  log.With("provider", providerLabel(cfg)),
		sem:  newPrioSem(size, prefetchSlots(size, o.PrefetchFraction)),
		brk:  newBreaker(o.BackoffMin, o.BackoffMax, o.FailThreshold, time.Now),
		stop: make(chan struct{}),
	}
	p.wg.Add(1)
	go p.janitor()
	return p
}

func providerLabel(cfg config.UsenetProvider) string {
	if cfg.Name != "" {
		return cfg.Name
	}
	if cfg.ID != "" {
		return cfg.ID
	}
	return cfg.Host
}

func (p *pool) dialOptions() DialOptions {
	connID := p.connSeq.Add(1)
	return DialOptions{
		Host:           p.cfg.Host,
		Port:           p.cfg.Port,
		TLS:            p.cfg.TLS,
		Username:       p.cfg.Username,
		Password:       p.cfg.Password,
		TLSConfig:      p.o.TLSConfig,
		Timeout:        p.o.DialTimeout,
		CommandTimeout: p.o.CommandTimeout,
		DialContext:    p.o.DialContext,
		BytesRead:      &p.bytes,
		OnPhase: func(phase string, d time.Duration) {
			switch phase {
			case "tcp":
				p.tcpTime.Add(int64(d))
			case "tls":
				p.tlsTime.Add(int64(d))
			case "greeting":
				p.greetingTime.Add(int64(d))
			case "auth":
				p.authTime.Add(int64(d))
			}
		},
		OnStatus: func(line string) {
			if p.o.TraceProtocol {
				p.log.Debug("nntp status", "connection", connID, "line", line)
			}
		},
		OnCommand: func(line string) {
			if p.o.TraceProtocol {
				p.log.Debug("nntp command", "connection", connID, "line", line)
			}
		},
	}
}

// get acquires a slot at prio and returns a connection for it. The caller
// must hand the connection back with put using the same prio.
func (p *pool) get(ctx context.Context, prio core.Priority) (*Conn, error) {
	for try := 0; ; try++ {
		if err := p.brk.allow(); err != nil {
			return nil, err
		}
		if err := p.sem.Acquire(ctx, prio); err != nil {
			return nil, err
		}
		c, err := p.conn(ctx)
		if err == nil {
			return c, nil
		}
		p.sem.Release(prio)
		if !errors.Is(err, errAtLimit) || try >= 3 {
			return nil, err
		}
		// The pool just shrank to what the server allows: wait for one of
		// the connections that do exist instead of failing.
	}
}

// errAtLimit marks a dial refused because the account is at its server-side
// connection limit while other connections of this pool are working.
var errAtLimit = errors.New("usenet: provider connection limit reached")

// shrinkTo lowers the pool's effective size to what the server accepted,
// for LimitCooloff, after which the configured size is tried again.
func (p *pool) shrinkTo(n int) {
	n = max(n, 1)
	p.mu.Lock()
	p.limitedUntil = time.Now().Add(p.o.LimitCooloff)
	p.mu.Unlock()
	if p.sem.setSize(n, p.o.PrefetchFraction) {
		p.log.Info("usenet: server allows fewer connections than configured; adapting", "configured", p.size, "using", n, "retry_after", p.o.LimitCooloff)
	}
}

func (p *pool) conn(ctx context.Context) (*Conn, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, ErrPoolClosed
		}
		if n := len(p.idle); n > 0 {
			c := p.idle[n-1]
			p.idle[n-1] = nil
			p.idle = p.idle[:n-1]
			p.mu.Unlock()
			if time.Since(c.idleSince) > p.o.IdleTimeout {
				p.discard(c)
				continue
			}
			return c, nil
		}
		p.live++
		p.mu.Unlock()
		break
	}
	// Re-check the breaker: another request may have tripped it while we
	// waited for a slot.
	if err := p.brk.allow(); err != nil {
		p.dropLive()
		return nil, err
	}
	c, err := Dial(ctx, p.dialOptions())
	if err != nil {
		p.dropLive()
		switch {
		case ctxErr(ctx) != nil:
			return nil, ctxErr(ctx)
		case errors.Is(err, ErrAuthFailed):
			p.errs.Add(1)
			p.brk.authFailure(err)
			p.log.Error("usenet: provider rejected credentials; disabled until its settings change", "err", err)
		case errors.Is(err, ErrServiceUnavailable) && p.liveCount() > 0:
			// The server is up and serving our other connections; the
			// account is just at its real connection limit. Shrink to it
			// rather than taking the provider out of rotation.
			p.brk.noteError(err)
			p.shrinkTo(p.liveCount())
			return nil, fmt.Errorf("%w: %w", errAtLimit, err)
		default:
			p.errs.Add(1)
			if d := p.brk.connFailure(err); d > 0 {
				p.log.Warn("usenet: connect failed; backing off", "err", err, "backoff", d)
			} else {
				p.log.Warn("usenet: connect failed", "err", err)
			}
		}
		return nil, err
	}
	p.brk.success()
	p.dials.Add(1)
	return c, nil
}

func (p *pool) liveCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.live
}

func (p *pool) dropLive() {
	p.mu.Lock()
	p.live--
	p.mu.Unlock()
}

// put returns a connection and its slot. Broken connections are closed.
func (p *pool) put(c *Conn, prio core.Priority) {
	if c.Broken() {
		p.discard(c)
	} else {
		p.mu.Lock()
		if p.closed {
			p.live--
			p.mu.Unlock()
			go c.Quit()
		} else {
			c.idleSince = time.Now()
			p.idle = append(p.idle, c)
			p.mu.Unlock()
		}
	}
	p.sem.Release(prio)
}

func (p *pool) discard(c *Conn) {
	_ = c.Close()
	p.dropLive()
}

// discardIdle closes every idle connection. Used when a reused connection
// turns out to have been dropped by the server: its idle siblings are very
// likely dead too, and redialling lazily is cheaper than failing on each.
func (p *pool) discardIdle() {
	p.mu.Lock()
	idle := p.idle
	p.idle = nil
	p.live -= len(idle)
	p.mu.Unlock()
	for _, c := range idle {
		_ = c.Close()
	}
}

// janitor closes connections idle beyond IdleTimeout and pings ones idle
// beyond KeepAlive so servers do not drop them silently.
func (p *pool) janitor() {
	defer p.wg.Done()
	every := p.o.IdleTimeout / 2
	if p.o.KeepAlive > 0 && p.o.KeepAlive/2 < every {
		every = p.o.KeepAlive / 2
	}
	every = max(every, 50*time.Millisecond)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.sweep()
		}
	}
}

func (p *pool) sweep() {
	now := time.Now()
	p.mu.Lock()
	restore := !p.limitedUntil.IsZero() && now.After(p.limitedUntil)
	if restore {
		p.limitedUntil = time.Time{}
	}
	p.mu.Unlock()
	if restore && p.sem.setSize(p.size, p.o.PrefetchFraction) {
		p.log.Info("usenet: trying the configured connection count again", "connections", p.size)
	}
	var expired []*Conn
	p.mu.Lock()
	keep := p.idle[:0]
	for _, c := range p.idle {
		if now.Sub(c.idleSince) > p.o.IdleTimeout {
			expired = append(expired, c)
		} else {
			keep = append(keep, c)
		}
	}
	clear(p.idle[len(keep):])
	p.idle = keep
	p.live -= len(expired)
	p.mu.Unlock()
	for _, c := range expired {
		_ = c.Quit()
	}
	if p.o.KeepAlive <= 0 {
		return
	}
	for i := 0; i < 4; i++ {
		if !p.keepAliveOne(now) {
			return
		}
	}
}

// keepAliveOne pings the longest-idle connection if it has been idle beyond
// KeepAlive (pings do not reset the idle timeout). A ping needs a slot like any other use of a connection, so the
// connection limit holds; when the pool is busy it is skipped (busy pools
// keep their connections alive with traffic). It reports whether it pinged.
func (p *pool) keepAliveOne(now time.Time) bool {
	if !p.sem.TryAcquire(core.PriorityReadahead) {
		return false
	}
	defer p.sem.Release(core.PriorityReadahead)
	p.mu.Lock()
	// idle is a LIFO stack, so the longest-idle connection is at index 0.
	if p.closed || len(p.idle) == 0 || now.Sub(p.idle[0].used) <= p.o.KeepAlive {
		p.mu.Unlock()
		return false
	}
	c := p.idle[0]
	p.idle = slices.Delete(p.idle, 0, 1)
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), p.o.CommandTimeout)
	_, err := c.Date(ctx)
	cancel()
	var pe *ProtocolError
	if err != nil && errors.As(err, &pe) && !c.Broken() {
		err = nil // DATE unsupported; the round trip still kept it alive
	}
	if err != nil {
		p.log.Debug("usenet: keepalive failed; closing idle connection", "err", err)
		p.discard(c)
		return true
	}
	p.mu.Lock()
	if p.closed {
		p.live--
		p.mu.Unlock()
		_ = c.Close()
		return true
	}
	// Just pinged, but still the least recently used for requests: keep it
	// at the bottom of the stack.
	p.idle = slices.Insert(p.idle, 0, c)
	p.mu.Unlock()
	return true
}

// close stops the pool. Waiters fail with ErrPoolClosed, idle connections
// are closed now and busy ones when they are returned.
func (p *pool) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.live -= len(idle)
	p.mu.Unlock()
	p.sem.Close()
	close(p.stop)
	p.wg.Wait()
	for _, c := range idle {
		go c.Quit()
	}
}

// body fetches and decodes one article on one connection.
func (p *pool) body(ctx context.Context, id string, groups []string, prio core.Priority) (*yenc.Part, error) {
	c, err := p.get(ctx, prio)
	if err != nil {
		return nil, err
	}
	reused := c.uses > 0
	c.uses++
	start := time.Now()
	if p.o.UseGroup && len(groups) > 0 && !slices.Contains(groups, c.CurrentGroup()) {
		if gerr := c.Group(ctx, groups[0]); gerr != nil && !errors.Is(gerr, ErrNoSuchGroup) {
			p.put(c, prio)
			return nil, p.failed(gerr, reused, ctx)
		}
	}
	part, err := c.BodyDecode(ctx, id, &yenc.DecodeOptions{MaxSize: p.o.MaxArticleSize})
	rtt := c.lastRTT
	p.put(c, prio)
	if err != nil {
		return nil, p.failed(err, reused, ctx)
	}
	p.articles.Add(1)
	p.latMu.Lock()
	p.latency.add(rtt)
	p.fetching.add(time.Since(start))
	p.latMu.Unlock()
	return part, nil
}

// stat runs STAT on one connection.
func (p *pool) stat(ctx context.Context, id string) (bool, error) {
	c, err := p.get(ctx, core.PriorityLive)
	if err != nil {
		return false, err
	}
	reused := c.uses > 0
	c.uses++
	ok, err := c.Stat(ctx, id)
	rtt := c.lastRTT
	p.put(c, core.PriorityLive)
	if err != nil {
		return false, p.failed(err, reused, ctx)
	}
	if !ok {
		p.misses.Add(1)
	}
	p.latMu.Lock()
	p.latency.add(rtt)
	p.latMu.Unlock()
	return ok, nil
}

func (p *pool) statMany(ctx context.Context, ids []string) (map[string]bool, error) {
	result := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	c, err := p.get(ctx, core.PriorityLive)
	if err != nil {
		return nil, err
	}
	reused := c.uses > 0
	c.uses++
	started := time.Now()
	result, err = c.StatMany(ctx, ids)
	p.statBatches.Add(1)
	p.statBatchTime.Add(int64(time.Since(started)))
	rtt := c.lastRTT
	p.put(c, core.PriorityLive)
	if err != nil {
		return nil, p.failed(err, reused, ctx)
	}
	for _, ok := range result {
		if !ok {
			p.misses.Add(1)
		}
	}
	p.latMu.Lock()
	p.latency.add(rtt)
	p.latMu.Unlock()
	return result, nil
}

// warm opens and authenticates one connection and returns it to the idle pool.
func (p *pool) warm(ctx context.Context, n int) error {
	// Multiple concurrent health checks may all request a warm pool. Serialize
	// each provider's warming pass so they share the same bounded idle set.
	p.warmMu.Lock()
	defer p.warmMu.Unlock()
	n = min(n, 4, p.size)
	if n <= 0 {
		return nil
	}
	type acquired struct {
		c   *Conn
		err error
	}
	ready := make(chan acquired, n)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := p.get(ctx, core.PriorityLive)
			ready <- acquired{c: c, err: err}
			if c != nil {
				<-release
				p.put(c, core.PriorityLive)
			}
		}()
	}
	var first error
	for i := 0; i < n; i++ {
		got := <-ready
		if got.err != nil && first == nil {
			first = got.err
		}
	}
	close(release)
	wg.Wait()
	return first
}

// failed classifies and records an error from a request on this pool.
func (p *pool) failed(err error, reused bool, ctx context.Context) error {
	switch {
	case ctxErr(ctx) != nil:
		return ctxErr(ctx)
	case errors.Is(err, ErrNoSuchArticle):
		p.misses.Add(1)
	case errors.Is(err, ErrInvalidMessageID):
	default:
		p.errs.Add(1)
		p.brk.noteError(err)
		if reused && isNetworkError(err) {
			p.log.Debug("usenet: idle connection was dropped by the server; discarding idle connections", "err", err)
			p.discardIdle()
		}
		if errors.Is(err, ErrCorrupt) {
			p.log.Warn("usenet: corrupt article", "err", err)
		}
	}
	return err
}

// ProviderStats is a snapshot of one provider for the admin dashboard.
type ProviderStats struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Priority int    `json:"priority"`
	Backfill bool   `json:"backfill"`

	// State is StateOK, StateBackoff or StateAuthFail.
	State        string    `json:"state"`
	BackoffUntil time.Time `json:"backoffUntil,omitempty"`
	LastError    string    `json:"lastError,omitempty"`
	LastErrorAt  time.Time `json:"lastErrorAt,omitempty"`

	// MaxConnections is the effective limit; it drops below
	// ConfiguredConnections for a while when the server refuses more.
	MaxConnections        int `json:"maxConnections"`
	ConfiguredConnections int `json:"configuredConnections"`
	LiveConnections       int `json:"liveConnections"`   // open sockets
	ActiveConnections     int `json:"activeConnections"` // slots in use
	IdleConnections       int `json:"idleConnections"`
	PrefetchActive        int `json:"prefetchActive"`
	WaitingLive           int `json:"waitingLive"`
	WaitingReadahead      int `json:"waitingReadahead"`
	WaitingPrefetch       int `json:"waitingPrefetch"`

	Bytes    int64 `json:"bytes"` // received off the wire
	Articles int64 `json:"articles"`
	Misses   int64 `json:"misses"`
	Errors   int64 `json:"errors"`
	Dials    int64 `json:"dials"`

	AvgLatency       time.Duration `json:"avgLatency"`   // command round trip (EWMA)
	AvgFetchTime     time.Duration `json:"avgFetchTime"` // whole article (EWMA)
	TCPDialTime      time.Duration `json:"tcpDialTime"`
	TLSHandshakeTime time.Duration `json:"tlsHandshakeTime"`
	GreetingTime     time.Duration `json:"greetingTime"`
	AuthTime         time.Duration `json:"authTime"`
	STATBatches      int64         `json:"statBatches"`
	STATBatchTime    time.Duration `json:"statBatchTime"`
}

func (p *pool) stats() ProviderStats {
	st := ProviderStats{
		ID: p.cfg.ID, Name: p.cfg.Name, Host: p.cfg.Host,
		Priority: p.cfg.Priority, Backfill: p.cfg.Backfill,
	}
	state, until, lastErr, lastAt := p.brk.state()
	st.State = state
	if state == StateBackoff {
		st.BackoffUntil = until
	}
	if lastErr != nil {
		st.LastError, st.LastErrorAt = lastErr.Error(), lastAt
	}
	ss := p.sem.stats()
	st.MaxConnections = ss.Size
	st.ConfiguredConnections = p.size
	st.ActiveConnections = ss.InUse
	st.PrefetchActive = ss.Prefetching
	st.WaitingLive = ss.Waiting[core.PriorityLive]
	st.WaitingReadahead = ss.Waiting[core.PriorityReadahead]
	st.WaitingPrefetch = ss.Waiting[core.PriorityPrefetch]
	p.mu.Lock()
	st.LiveConnections = p.live
	st.IdleConnections = len(p.idle)
	if p.closed {
		st.State = StateClosed
	}
	p.mu.Unlock()
	st.Bytes = p.bytes.Load()
	st.Articles = p.articles.Load()
	st.Misses = p.misses.Load()
	st.Errors = p.errs.Load()
	st.Dials = p.dials.Load()
	st.TCPDialTime = time.Duration(p.tcpTime.Load())
	st.TLSHandshakeTime = time.Duration(p.tlsTime.Load())
	st.GreetingTime = time.Duration(p.greetingTime.Load())
	st.AuthTime = time.Duration(p.authTime.Load())
	st.STATBatches = p.statBatches.Load()
	st.STATBatchTime = time.Duration(p.statBatchTime.Load())
	p.latMu.Lock()
	st.AvgLatency = time.Duration(p.latency.v)
	st.AvgFetchTime = time.Duration(p.fetching.v)
	p.latMu.Unlock()
	return st
}
