package store

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Outbox defaults.
const (
	DefaultOutboxCap     = 10000
	DefaultOutboxTimeout = 2 * time.Second
	defaultOutboxMin     = time.Second
	defaultOutboxMax     = 30 * time.Second
)

// OutboxOptions configure an Outbox. Apply is required.
type OutboxOptions[K comparable, V any] struct {
	// Name identifies the outbox in logs and status ("watch progress").
	Name string
	// Apply performs one write. It must be safe to run again for the same
	// key (a write whose outcome was lost to a timeout is retried).
	Apply func(ctx context.Context, key K, v V) error
	// Merge combines a queued value with a newer one for the same key;
	// nil keeps only the newer value.
	Merge func(old, new V) V
	// Cap bounds the number of queued keys (DefaultOutboxCap); when full
	// the oldest key is dropped.
	Cap int
	// Timeout bounds every write attempt (DefaultOutboxTimeout), so a
	// caller never waits on an outage longer than this.
	Timeout time.Duration
	// MinBackoff / MaxBackoff space retries while the database is
	// unreachable (1s doubling to 30s).
	MinBackoff, MaxBackoff time.Duration
	// Availability is shared with the node's other outboxes and the
	// cluster heartbeat; nil gives this outbox a private tracker.
	Availability *Availability
	Logger       *slog.Logger
}

// Outbox makes deferrable writes survive a database outage. A write goes
// straight to the database while it is reachable. When the database cannot
// be reached (an error IsUnavailable, or the attempt outlives Timeout) the
// write is kept in memory instead — only the latest value per key, merged
// with Merge — and replayed oldest key first, with backoff, once the
// database answers again. While the database is known to be down new
// writes are queued without being attempted, so callers (HTTP handlers)
// return at once.
//
// Errors that are not outages (a constraint violation, a bug) are returned
// to the caller from Write, or logged and dropped during a replay.
type Outbox[K comparable, V any] struct {
	o     OutboxOptions[K, V]
	avail *Availability
	log   *slog.Logger

	mu      sync.Mutex
	order   *list.List // of *outboxEntry[K, V], oldest first
	entries map[K]*list.Element
	dropped int64
	lastErr string
	closed  bool
	seq     uint64 // orders Write calls
	// latest is the newest Write seq per key, kept while a direct write for
	// the key is in flight (inflight counts them): a direct write failing
	// after a newer value for its key was queued and already replayed must
	// not be queued again on top of it.
	latest   map[K]uint64
	inflight map[K]int

	kick chan struct{}
	stop chan struct{}
	done chan struct{}
}

type outboxEntry[K comparable, V any] struct {
	key    K
	val    V
	gen    uint64 // bumped on every merge, so a replay never drops a newer value
	seq    uint64 // newest Write merged into val
	queued time.Time
}

// NewOutbox starts an outbox's replay goroutine; Close stops it.
func NewOutbox[K comparable, V any](o OutboxOptions[K, V]) *Outbox[K, V] {
	if o.Apply == nil {
		panic("store: outbox needs Apply")
	}
	if o.Cap <= 0 {
		o.Cap = DefaultOutboxCap
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultOutboxTimeout
	}
	if o.MinBackoff <= 0 {
		o.MinBackoff = defaultOutboxMin
	}
	if o.MaxBackoff < o.MinBackoff {
		o.MaxBackoff = max(defaultOutboxMax, o.MinBackoff)
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Availability == nil {
		o.Availability = NewAvailability(nil)
	}
	ob := &Outbox[K, V]{
		o: o, avail: o.Availability, log: o.Logger.With("outbox", o.Name),
		order: list.New(), entries: map[K]*list.Element{}, latest: map[K]uint64{}, inflight: map[K]int{},
		kick: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go ob.loop()
	return ob
}

// Write applies v for key now, or queues it when the database is
// unreachable. queued reports the latter; err is a non-outage failure of
// an immediate write.
func (ob *Outbox[K, V]) Write(ctx context.Context, key K, v V) (queued bool, err error) {
	ob.mu.Lock()
	ob.seq++
	seq := ob.seq
	if ob.inflight[key] > 0 {
		ob.latest[key] = seq
	}
	_, pending := ob.entries[key]
	closed := ob.closed
	if !closed && (pending || ob.avail.Down()) {
		// Keep per-key order: a value already waiting is replayed first.
		ob.enqueueLocked(key, v, seq)
		ob.mu.Unlock()
		ob.wake()
		return true, nil
	}
	ob.inflight[key]++
	ob.latest[key] = max(ob.latest[key], seq)
	ob.mu.Unlock()

	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ob.o.Timeout)
	err = ob.o.Apply(actx, key, v)
	cancel()
	ob.mu.Lock()
	newer := ob.latest[key] > seq
	if ob.inflight[key]--; ob.inflight[key] <= 0 {
		delete(ob.inflight, key)
		delete(ob.latest, key)
	}
	ob.mu.Unlock()
	if err == nil {
		ob.avail.OK()
		return false, nil
	}
	if !IsUnavailable(err) || closed {
		return false, err // after Close nothing would replay it
	}
	ob.avail.Fail(err)
	ob.mu.Lock()
	ob.lastErr = err.Error()
	// A later Write for key may have been queued while this one was in
	// flight; enqueueLocked keeps that newer value on top. If it was
	// already replayed (no entry left), this older value is obsolete.
	if _, pending := ob.entries[key]; newer && !pending {
		ob.mu.Unlock()
		return true, nil
	}
	ob.enqueueLocked(key, v, seq)
	ob.mu.Unlock()
	ob.wake()
	return true, nil
}

// enqueueLocked holds v, the value of Write number seq, for key. Writes are
// merged in call order: a direct write that failed after a later Write for
// the same key was queued is merged underneath that newer value.
func (ob *Outbox[K, V]) enqueueLocked(key K, v V, seq uint64) {
	if el, ok := ob.entries[key]; ok {
		e := el.Value.(*outboxEntry[K, V])
		switch {
		case seq >= e.seq && ob.o.Merge != nil:
			e.val, e.seq = ob.o.Merge(e.val, v), seq
		case seq >= e.seq:
			e.val, e.seq = v, seq
		case ob.o.Merge != nil:
			e.val = ob.o.Merge(v, e.val)
		default:
			return // the queued value is newer
		}
		e.gen++
		return
	}
	for ob.order.Len() >= ob.o.Cap {
		front := ob.order.Front()
		e := front.Value.(*outboxEntry[K, V])
		ob.order.Remove(front)
		delete(ob.entries, e.key)
		ob.dropped++
		if ob.dropped == 1 || ob.dropped%1000 == 0 {
			ob.log.Warn("store: outbox full; dropping the oldest held write", "cap", ob.o.Cap, "dropped", ob.dropped)
		}
	}
	ob.entries[key] = ob.order.PushBack(&outboxEntry[K, V]{key: key, val: v, seq: seq, queued: time.Now()})
}

func (ob *Outbox[K, V]) wake() {
	select {
	case ob.kick <- struct{}{}:
	default:
	}
}

// loop replays queued writes: immediately when the database is reported
// reachable, otherwise after a backoff.
func (ob *Outbox[K, V]) loop() {
	defer close(ob.done)
	backoff := ob.o.MinBackoff
	var timer *time.Timer
	for {
		var tc <-chan time.Time
		var up <-chan struct{}
		if ob.Depth() > 0 {
			if timer == nil {
				timer = time.NewTimer(backoff)
			}
			tc = timer.C
			if ob.avail.Down() {
				up = ob.avail.Up() // another writer saw it come back
			}
		}
		select {
		case <-ob.stop:
			if timer != nil {
				timer.Stop()
			}
			return
		case <-ob.kick:
			if ob.avail.Down() {
				continue // keep waiting out the backoff
			}
		case <-tc:
			timer = nil
		case <-up:
		}
		if timer != nil {
			timer.Stop()
			timer = nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-ob.stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		outage := ob.flush(ctx)
		cancel()
		if outage {
			backoff = min(backoff*2, ob.o.MaxBackoff)
		} else {
			backoff = ob.o.MinBackoff
		}
	}
}

// flush replays queued writes oldest first until the queue is empty or the
// database is unreachable again (reported as true).
func (ob *Outbox[K, V]) flush(ctx context.Context) (outage bool) {
	replayed := 0
	start := time.Now()
	defer func() {
		if replayed > 0 && !outage {
			ob.log.Info("store: database reachable; held writes written", "writes", replayed, "took", time.Since(start).Round(time.Millisecond))
		}
	}()
	for ctx.Err() == nil {
		ob.mu.Lock()
		front := ob.order.Front()
		if front == nil {
			ob.mu.Unlock()
			return false
		}
		e := front.Value.(*outboxEntry[K, V])
		key, val, gen := e.key, e.val, e.gen
		ob.mu.Unlock()

		actx, cancel := context.WithTimeout(ctx, ob.o.Timeout)
		err := ob.o.Apply(actx, key, val)
		cancel()
		if err != nil && ctx.Err() != nil {
			// Stopped mid-write (shutdown): keep the write for Close's
			// final attempt rather than dropping it as rejected.
			return false
		}
		if err != nil && (IsUnavailable(err) || errors.Is(err, context.DeadlineExceeded)) {
			if ctx.Err() == nil {
				ob.avail.Fail(err)
			}
			ob.mu.Lock()
			ob.lastErr = err.Error()
			ob.mu.Unlock()
			return true
		}
		if err != nil {
			ob.log.Warn("store: dropping a held write the database rejected", "err", err)
		} else {
			ob.avail.OK()
			replayed++
		}
		ob.mu.Lock()
		if el, ok := ob.entries[key]; ok && el == front && e.gen == gen {
			ob.order.Remove(front)
			delete(ob.entries, key)
		} else if ok && el == front {
			// Merged with a newer value while writing: move it to the back
			// so one busy key cannot starve the rest.
			ob.order.MoveToBack(front)
		}
		ob.mu.Unlock()
	}
	return false
}

// Depth is the number of queued keys.
func (ob *Outbox[K, V]) Depth() int {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	return ob.order.Len()
}

// OutboxStatus is an outbox's state for health reports.
type OutboxStatus struct {
	Name    string `json:"name"`
	Depth   int    `json:"depth"`
	Dropped int64  `json:"dropped,omitempty"`
	// Oldest is when the oldest held write was queued.
	Oldest    time.Time `json:"oldest,omitzero"`
	LastError string    `json:"lastError,omitempty"`
}

// Status snapshots the outbox.
func (ob *Outbox[K, V]) Status() OutboxStatus {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	st := OutboxStatus{Name: ob.o.Name, Depth: ob.order.Len(), Dropped: ob.dropped}
	if st.Depth > 0 {
		st.LastError = ob.lastErr
		for el := ob.order.Front(); el != nil; el = el.Next() {
			if q := el.Value.(*outboxEntry[K, V]).queued; st.Oldest.IsZero() || q.Before(st.Oldest) {
				st.Oldest = q
			}
		}
	}
	return st
}

// Close stops the replay goroutine, then makes one last attempt (bounded
// by ctx) to write what is still held. It returns an error naming how many
// writes were lost.
func (ob *Outbox[K, V]) Close(ctx context.Context) error {
	ob.mu.Lock()
	if ob.closed {
		ob.mu.Unlock()
		return nil
	}
	ob.closed = true
	ob.mu.Unlock()
	close(ob.stop)
	<-ob.done
	if ob.Depth() == 0 {
		return nil
	}
	ob.flush(ctx)
	if n := ob.Depth(); n > 0 {
		return fmt.Errorf("store: %s: %d held writes could not be written before shutdown: %w", ob.o.Name, n, errOutboxLost)
	}
	return nil
}

var errOutboxLost = errors.New("database unreachable")
