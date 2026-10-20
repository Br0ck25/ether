// Package usenet is Ether's Usenet layer: an NNTP client, per-provider
// connection pools with priority scheduling and circuit breakers, and a
// multi-provider fetcher that implements core.ArticleFetcher with
// primary-first ordering and backfill from block accounts.
package usenet

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/yenc"
)

// Defaults for Options.
const (
	DefaultConnections      = 8
	DefaultMinIdle          = 2
	DefaultPrefetchFraction = 0.25
	DefaultIdleTimeout      = 90 * time.Second
	DefaultKeepAlive        = 30 * time.Second
	DefaultMissTTL          = 2 * time.Minute
	DefaultMissCacheSize    = 100_000
	DefaultRetries          = 2
	DefaultBackoffMin       = time.Second
	DefaultBackoffMax       = 2 * time.Minute
	DefaultFailThreshold    = 2
	DefaultLimitCooloff     = 5 * time.Minute
	DefaultAuthRetryMin     = 15 * time.Minute
	DefaultAuthRetryMax     = 6 * time.Hour
)

// Options tune the fetcher. Zero values take the defaults above.
type Options struct {
	Logger *slog.Logger
	// PrefetchFraction is the share of each provider's connections that
	// PriorityPrefetch requests may hold at once (at least one).
	PrefetchFraction float64
	// DialTimeout bounds connect + TLS + greeting + login.
	DialTimeout time.Duration
	// CommandTimeout bounds one command, including reading an article body.
	CommandTimeout time.Duration
	// IdleReadTimeout bounds stalled command/body reads; default 5 seconds.
	IdleReadTimeout time.Duration
	// MinIdle keeps this many primary connections warm after use; negative disables.
	MinIdle int
	// IdleTimeout closes excess connections unused for this long.
	IdleTimeout time.Duration
	// KeepAlive pings (DATE) connections idle for this long so servers do not
	// drop them; negative disables pings.
	KeepAlive time.Duration
	// MissTTL is how long a 430 from a provider is remembered per article;
	// negative disables the miss cache.
	MissTTL       time.Duration
	MissCacheSize int
	// Retries is how many extra attempts a provider gets, each on another
	// connection, after transient (network/protocol) errors.
	Retries int
	// Circuit breaker: after FailThreshold consecutive failed connects a
	// provider is skipped for BackoffMin, doubling up to BackoffMax.
	BackoffMin    time.Duration
	BackoffMax    time.Duration
	FailThreshold int
	// AuthRetryMin/Max: a provider that rejected the credentials (481/482)
	// is re-probed with one login after AuthRetryMin, doubling per failed
	// probe up to AuthRetryMax, so transient auth outages self-heal.
	AuthRetryMin time.Duration
	AuthRetryMax time.Duration
	// LimitCooloff: when a server refuses connections beyond some number
	// while others work, the pool shrinks to that number for this long.
	LimitCooloff time.Duration
	// TLSConfig is the base TLS client configuration (custom roots, e.g. in
	// tests). Nil uses the system roots.
	TLSConfig *tls.Config
	// DialContext overrides the network dialer.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// UseGroup issues GROUP (first of the segment's groups) before BODY. Most
	// providers serve BODY <message-id> without it.
	UseGroup bool
	// MaxArticleSize caps a decoded article; 0 = yenc.DefaultMaxSize.
	MaxArticleSize int64
	// TraceProtocol emits raw NNTP status lines at debug level.
	TraceProtocol bool
}

func (o *Options) withDefaults() {
	if o.MinIdle == 0 {
		o.MinIdle = DefaultMinIdle
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.PrefetchFraction <= 0 || o.PrefetchFraction > 1 {
		o.PrefetchFraction = DefaultPrefetchFraction
	}
	if o.DialTimeout <= 0 {
		o.DialTimeout = DefaultDialTimeout
	}
	if o.CommandTimeout <= 0 {
		o.CommandTimeout = DefaultCommandTimeout
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = DefaultIdleTimeout
	}
	if o.KeepAlive == 0 {
		o.KeepAlive = DefaultKeepAlive
	}
	if o.MissTTL == 0 {
		o.MissTTL = DefaultMissTTL
	}
	if o.MissCacheSize <= 0 {
		o.MissCacheSize = DefaultMissCacheSize
	}
	if o.Retries < 0 {
		o.Retries = 0
	} else if o.Retries == 0 {
		o.Retries = DefaultRetries
	}
	if o.BackoffMin <= 0 {
		o.BackoffMin = DefaultBackoffMin
	}
	if o.BackoffMax < o.BackoffMin {
		o.BackoffMax = max(DefaultBackoffMax, o.BackoffMin)
	}
	if o.FailThreshold <= 0 {
		o.FailThreshold = DefaultFailThreshold
	}
	if o.LimitCooloff <= 0 {
		o.LimitCooloff = DefaultLimitCooloff
	}
	if o.AuthRetryMin <= 0 {
		o.AuthRetryMin = DefaultAuthRetryMin
	}
	if o.AuthRetryMax < o.AuthRetryMin {
		o.AuthRetryMax = max(DefaultAuthRetryMax, o.AuthRetryMin)
	}
	if o.MaxArticleSize <= 0 {
		o.MaxArticleSize = yenc.DefaultMaxSize
	}
}

// Fetcher implements core.ArticleFetcher over the owner's providers.
//
// Ordering: non-backfill providers are tried in ascending Priority; providers
// sharing a Priority form a tier and share load (the least busy is tried
// first). Only when every non-backfill provider answered 430 are backfill
// (block) providers tried, in the same way. Transient errors are retried on
// fresh connections and then on the next provider.
type Fetcher struct {
	o   Options
	log *slog.Logger

	mu     sync.RWMutex
	pools  []*pool   // every enabled provider, in config order
	tiers  [][]*pool // try order
	closed bool
	// share maps a provider's configured connections to this node's part
	// of them (clustered); nil uses every configured connection.
	share func(configured int) int
	gen   atomic.Uint64
	rr    atomic.Uint64

	misses *missCache
}

var _ core.ArticleFetcher = (*Fetcher)(nil)

// New builds a fetcher for providers. Connections are dialled lazily.
func New(providers []config.UsenetProvider, opts Options) *Fetcher {
	opts.withDefaults()
	f := &Fetcher{
		o:      opts,
		log:    opts.Logger,
		misses: newMissCache(opts.MissTTL, opts.MissCacheSize),
	}
	f.Update(providers)
	return f
}

// Update swaps in a new provider list. Providers whose settings are
// unchanged keep their pool, connections, statistics and circuit-breaker
// state; changed providers get a fresh pool (which also clears an
// authentication lockout); removed or disabled ones are closed gracefully:
// queued requests move on to other providers and busy connections close
// when their current article finishes.
func (f *Fetcher) Update(providers []config.UsenetProvider) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	old := f.pools
	var next []*pool
	reused := make(map[*pool]bool)
	for _, cfg := range providers {
		if !cfg.Enabled || cfg.Host == "" {
			continue
		}
		var p *pool
		for _, op := range old {
			if op.cfg == cfg && !reused[op] {
				p = op
				break
			}
		}
		if p == nil {
			key := cmp.Or(cfg.ID, cfg.Host) + "#" + strconv.FormatUint(f.gen.Add(1), 10)
			p = newPool(key, cfg, &f.o, f.log)
			p.setShare(shareFor(f.share, p.size))
		}
		reused[p] = true
		next = append(next, p)
	}
	f.pools = next
	f.tiers = buildTiers(next)
	f.mu.Unlock()

	for _, p := range old {
		if !reused[p] {
			p.close()
		}
	}
	f.log.Debug("usenet: providers updated", "enabled", len(next))
}

func buildTiers(pools []*pool) [][]*pool {
	sorted := slices.Clone(pools)
	slices.SortStableFunc(sorted, func(a, b *pool) int {
		if a.cfg.Backfill != b.cfg.Backfill {
			if a.cfg.Backfill {
				return 1
			}
			return -1
		}
		return cmp.Compare(a.cfg.Priority, b.cfg.Priority)
	})
	var tiers [][]*pool
	for i, p := range sorted {
		if i > 0 {
			q := sorted[i-1]
			if q.cfg.Backfill == p.cfg.Backfill && q.cfg.Priority == p.cfg.Priority {
				tiers[len(tiers)-1] = append(tiers[len(tiers)-1], p)
				continue
			}
		}
		tiers = append(tiers, []*pool{p})
	}
	return tiers
}

// order returns the providers to try, tier by tier, least loaded first
// within a tier (ties rotate so equal providers share work).
func (f *Fetcher) order() []*pool {
	f.mu.RLock()
	tiers := f.tiers
	f.mu.RUnlock()
	var out []*pool
	rot := int(f.rr.Add(1))
	for _, tier := range tiers {
		if len(tier) == 1 {
			out = append(out, tier[0])
			continue
		}
		t := make([]*pool, len(tier))
		for i := range tier {
			t[i] = tier[(i+rot)%len(tier)]
		}
		loads := make(map[*pool]float64, len(t))
		for _, p := range t {
			s := p.sem.stats()
			w := s.Waiting[0] + s.Waiting[1] + s.Waiting[2]
			loads[p] = float64(s.InUse+w) / float64(s.Size)
		}
		slices.SortStableFunc(t, func(a, b *pool) int { return cmp.Compare(loads[a], loads[b]) })
		out = append(out, t...)
	}
	return out
}

// Body returns the decoded payload of one article.
func (f *Fetcher) Body(ctx context.Context, messageID string, groups []string, prio core.Priority) ([]byte, error) {
	data, _, err := f.BodyPart(ctx, messageID, groups, prio)
	return data, err
}

// BodyPart returns the decoded payload and its yEnc part header. It returns
// an error wrapping core.ErrArticleMissing only when every provider able to
// answer said 430. Providers locked out after an authentication failure
// cannot answer and are not counted; providers in connection backoff are.
func (f *Fetcher) BodyPart(ctx context.Context, messageID string, groups []string, prio core.Priority) ([]byte, core.ArticlePart, error) {
	var ap core.ArticlePart
	id, err := normalizeMessageID(messageID)
	if err != nil {
		return nil, ap, err
	}
	var part *yenc.Part
	err = f.each(ctx, id, "body", func(p *pool) error {
		var e error
		part, e = p.body(ctx, id, groups, prio)
		return e
	})
	if err != nil {
		return nil, ap, err
	}
	h := part.Header
	ap = core.ArticlePart{Name: h.Name, FileSize: h.Size, Part: h.Part, Total: h.Total, Begin: h.Begin, End: h.End}
	if ap.Part == 0 {
		ap.Part, ap.Total = 1, max(h.Total, 1)
	}
	return part.Data, ap, nil
}

// Stat reports whether any provider has the article (STAT, at live
// priority since health checks gate playback). It returns (false, nil) when
// every provider able to answer said 430, and an error when that could not
// be established.
func (f *Fetcher) Stat(ctx context.Context, messageID string) (bool, error) {
	id, err := normalizeMessageID(messageID)
	if err != nil {
		return false, err
	}
	err = f.each(ctx, id, "stat", func(p *pool) error {
		ok, e := p.stat(ctx, id)
		if e == nil && !ok {
			return ErrNoSuchArticle
		}
		return e
	})
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, core.ErrArticleMissing):
		return false, nil
	}
	return false, err
}

// StatMany checks a set of articles while pipelining commands over several
// connections to the first available provider. Missing articles are retried
// through the normal provider chain, preserving block-account backfill.
func (f *Fetcher) StatMany(ctx context.Context, messageIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(messageIDs))
	if len(messageIDs) == 0 {
		return result, nil
	}
	ids := make([]string, 0, len(messageIDs))
	for _, id := range messageIDs {
		normalized, err := normalizeMessageID(id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, normalized)
	}
	pools := f.order()
	if len(pools) == 0 {
		return nil, ErrNoProviders
	}
	unresolved := append([]string(nil), ids...)
	var firstErr error
	for _, p := range pools {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(unresolved) == 0 {
			break
		}
		workers := min(4, p.target(), len(unresolved))
		if workers < 1 {
			workers = 1
		}
		chunks := make([][]string, workers)
		for i, id := range unresolved {
			chunks[i%workers] = append(chunks[i%workers], id)
		}
		type answer struct {
			values map[string]bool
			err    error
		}
		answers := make([]answer, len(chunks))
		var wg sync.WaitGroup
		for i, chunk := range chunks {
			if len(chunk) == 0 {
				continue
			}
			wg.Add(1)
			go func(i int, chunk []string) {
				defer wg.Done()
				answers[i].values, answers[i].err = p.statMany(ctx, chunk)
			}(i, chunk)
		}
		wg.Wait()
		for _, a := range answers {
			if a.err != nil {
				if firstErr == nil {
					firstErr = a.err
				}
				continue
			}
			for id, ok := range a.values {
				if ok {
					result[id] = true
				} else {
					f.misses.add(id, p.key)
				}
			}
		}
		next := unresolved[:0]
		for _, id := range unresolved {
			if !result[id] {
				next = append(next, id)
			}
		}
		unresolved = next
		// A provider error means a negative from another provider cannot prove
		// global absence. Preserve uncertainty after all provider attempts.
	}
	if len(unresolved) > 0 && firstErr != nil {
		return byCallerID(messageIDs, ids, result), firstErr
	}
	return byCallerID(messageIDs, ids, result), nil
}

// byCallerID re-keys results from normalized "<id>" form to the message-ids
// exactly as the caller passed them (NZBs carry them without brackets).
func byCallerID(callerIDs, normalized []string, result map[string]bool) map[string]bool {
	out := make(map[string]bool, len(callerIDs))
	for i, id := range callerIDs {
		out[id] = result[normalized[i]]
	}
	return out
}

// Warm dials, negotiates TLS, and authenticates idle connections before a
// playback request needs them. perProvider is clamped to each provider pool.
func (f *Fetcher) Warm(ctx context.Context, perProvider int) error {
	if perProvider < 1 {
		return nil
	}
	f.mu.RLock()
	pools := append([]*pool(nil), f.pools...)
	f.mu.RUnlock()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	for _, p := range pools {
		wg.Add(1)
		go func(p *pool) {
			defer wg.Done()
			if err := p.warm(ctx, perProvider); err != nil {
				mu.Lock()
				if first == nil {
					first = fmt.Errorf("usenet: warm %s: %w", providerLabel(p.cfg), err)
				}
				mu.Unlock()
			}
		}(p)
	}
	wg.Wait()
	return first
}

// each runs op against providers in order until one succeeds, and turns the
// collected failures into the right error.
func (f *Fetcher) each(ctx context.Context, id, kind string, op func(*pool) error) error {
	var swapped bool
	err := f.eachOnce(ctx, id, kind, op, &swapped)
	if swapped && err != nil && !errors.Is(err, core.ErrArticleMissing) && ctxErr(ctx) == nil {
		// A pool was replaced by Update while we were using it: go again
		// over the current providers.
		err = f.eachOnce(ctx, id, kind, op, &swapped)
	}
	return err
}

// eachOnce is one pass over the providers. It sets *swapped when a pool
// turned out to be closed by a concurrent Update.
func (f *Fetcher) eachOnce(ctx context.Context, id, kind string, op func(*pool) error, swapped *bool) error {
	pools := f.order()
	if len(pools) == 0 {
		return ErrNoProviders
	}
	known := f.misses.get(id)
	var (
		answered, missing int
		lastErr, corrupt  error
	)
	for _, p := range pools {
		if slices.Contains(known, p.key) {
			answered++
			missing++
			continue
		}
		if err := p.brk.allow(); errors.Is(err, ErrProviderDisabled) {
			lastErr = fmt.Errorf("%s: %w", providerLabel(p.cfg), err)
			continue
		}
		answered++
	attempts:
		for attempt := 0; attempt <= f.o.Retries; attempt++ {
			err := op(p)
			if err == nil {
				if kind == "body" && p.cfg.Backfill && missing > 0 {
					p.backfillSaves.Add(1)
				}
				return nil
			}
			if cerr := ctxErr(ctx); cerr != nil {
				return cerr
			}
			switch {
			case errors.Is(err, ErrNoSuchArticle):
				missing++
				f.misses.add(id, p.key)
				break attempts
			case errors.Is(err, ErrInvalidMessageID):
				return err
			case errors.Is(err, ErrCorrupt):
				corrupt = fmt.Errorf("%s: %w", providerLabel(p.cfg), err)
				break attempts
			case errors.Is(err, ErrProviderDisabled), errors.Is(err, ErrAuthFailed):
				// Locked out mid-request (bad credentials): cannot answer.
				answered--
				lastErr = fmt.Errorf("%s: %w", providerLabel(p.cfg), err)
				break attempts
			case errors.Is(err, ErrPoolClosed):
				*swapped = true
				lastErr = fmt.Errorf("%s: %w", providerLabel(p.cfg), err)
				break attempts
			case errors.Is(err, ErrProviderBackoff):
				lastErr = fmt.Errorf("%s: %w", providerLabel(p.cfg), err)
				break attempts
			default:
				lastErr = fmt.Errorf("%s: %w", providerLabel(p.cfg), err)
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					break attempts
				}
				f.log.Debug("usenet: transient error; retrying", "provider", providerLabel(p.cfg), "msgid", id, "attempt", attempt+1, "err", err)
			}
		}
	}
	switch {
	case answered > 0 && missing == answered:
		return fmt.Errorf("%s: %w", id, core.ErrArticleMissing)
	case answered == 0:
		return fmt.Errorf("%w: %w", ErrNoProviders, lastErr)
	case corrupt != nil && lastErr == nil:
		return corrupt
	case corrupt != nil:
		return errors.Join(corrupt, lastErr)
	}
	return lastErr
}

// Stats returns a snapshot per enabled provider, in configuration order.
func (f *Fetcher) Stats() []ProviderStats {
	f.mu.RLock()
	pools := f.pools
	f.mu.RUnlock()
	out := make([]ProviderStats, 0, len(pools))
	for _, p := range pools {
		out = append(out, p.stats())
	}
	return out
}

// MaxConnections is the sum of all enabled primary providers' connection
// limits on this node: the configured counts, or this node's shares of
// them when SetConnectionShare divides them among cluster nodes.
func (f *Fetcher) MaxConnections() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	n := 0
	for _, p := range f.pools {
		if !p.cfg.Backfill {
			n += p.target()
		}
	}
	return n
}

// SetConnectionShare divides provider connection limits among cluster
// nodes: share receives a provider's configured connection count (the
// account-wide limit) and returns this node's part of it, clamped to
// 1..configured. Nil restores the configured counts (a standalone server).
// Pools resize in place and gracefully (see pool.setShare); providers
// added later by Update get their share as they are created. Call it again
// whenever membership changes.
func (f *Fetcher) SetConnectionShare(share func(configured int) int) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.share = share
	pools := slices.Clone(f.pools)
	f.mu.Unlock()
	for _, p := range pools {
		p.setShare(shareFor(share, p.size))
	}
}

func shareFor(share func(int) int, configured int) int {
	if share == nil {
		return 0
	}
	return min(max(share(configured), 1), configured)
}

// Close closes every pool. Further requests fail with ErrNoProviders.
func (f *Fetcher) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	pools := f.pools
	f.pools, f.tiers = nil, nil
	f.mu.Unlock()
	for _, p := range pools {
		p.close()
	}
	return nil
}

// CheckBody verifies actual BODY availability without downloading a full article.
// A partial response retires its connection so unread bytes cannot poison reuse.
func (f *Fetcher) CheckBody(ctx context.Context, messageID string, groups []string, prio core.Priority) error {
	id, err := normalizeMessageID(messageID)
	if err != nil {
		return err
	}
	return f.each(ctx, id, "body", func(p *pool) error {
		c, err := p.get(ctx, prio)
		if err != nil {
			return err
		}
		reused := c.uses > 0
		c.uses++
		if p.o.UseGroup && len(groups) > 0 && !slices.Contains(groups, c.CurrentGroup()) {
			if err = c.Group(ctx, groups[0]); err != nil && !errors.Is(err, ErrNoSuchGroup) {
				p.put(c, prio)
				return p.failed(err, reused, ctx)
			}
		}
		reader, err := c.Body(ctx, id)
		if err == nil {
			// A single buffered byte proves the BODY is readable. Bypass the
			// line reader so a malicious unterminated line cannot consume an article.
			n, e := io.CopyN(io.Discard, c.r, 1)
			if e != nil && !errors.Is(e, io.EOF) {
				err = e
			} else if n == 0 {
				err = ErrCorrupt
			}
			if !reader.done {
				_ = c.Close()
				reader.finish(nil)
			}
		}
		p.put(c, prio)
		if err != nil {
			if errors.Is(err, ErrNoSuchArticle) {
				p.bodyMisses.Add(1)
				var pe *ProtocolError
				if errors.As(err, &pe) && pe.Code == 451 {
					p.bodyTakedowns.Add(1)
				}
			}
			return p.failed(err, reused, ctx)
		}
		p.brk.success()
		return nil
	})
}
