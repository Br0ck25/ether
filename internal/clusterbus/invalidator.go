package clusterbus

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// maxHints is how many distinct hints are queued before an invalidation
// degrades to "reload everything".
const maxHints = 64

// ApplyFunc reloads or clears a component's caches. all is true after a bus
// resync, an oversized burst or an event without a hint; otherwise hints
// lists the keys other nodes reported changing.
type ApplyFunc func(ctx context.Context, hints []string, all bool)

// Invalidator keeps one component's in-memory caches consistent with
// writes made by other nodes: after a local write the component calls
// Notify; every other node's Invalidator for the same component runs apply
// on a worker goroutine (bursts are coalesced; bus callbacks never block).
// Its own events are ignored. After a bus resync apply runs with all=true.
//
// A nil *Invalidator is valid and does nothing, which is what a standalone
// server uses.
type Invalidator struct {
	bus    Bus
	topic  string
	origin string
	log    *slog.Logger
	apply  ApplyFunc
	unsubs []func()

	mu      sync.Mutex
	hints   map[string]bool
	all     bool
	kick    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	closed  bool
	applied int // completed apply runs (tests)
}

// NewInvalidator subscribes to "invalidate:<component>" on bus. A nil bus
// returns nil.
func NewInvalidator(bus Bus, component string, log *slog.Logger, apply ApplyFunc) *Invalidator {
	if bus == nil || apply == nil {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	iv := &Invalidator{bus: bus, topic: "invalidate:" + component, origin: NewOrigin(), log: log, apply: apply,
		hints: map[string]bool{}, kick: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	iv.unsubs = []func(){
		bus.Subscribe(iv.topic, iv.onEvent),
		bus.Subscribe(TopicResync, func([]byte) { iv.queue("", true) }),
	}
	go iv.run()
	return iv
}

// Notify tells the other nodes that this node changed the component's data
// (hint optionally names what changed; "" means everything). It publishes
// synchronously with a short timeout; failures are logged, not returned:
// the write itself succeeded, and peers also reload on a bus resync.
func (iv *Invalidator) Notify(hint string) {
	if iv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), PublishTimeout)
	defer cancel()
	payload := Tag(iv.origin, []byte(hint))
	if len(payload) > MaxPayload {
		payload = Tag(iv.origin, nil)
	}
	if err := iv.bus.Publish(ctx, iv.topic, payload); err != nil {
		iv.log.Warn("cluster: publish cache invalidation", "topic", iv.topic, "err", err)
	}
}

func (iv *Invalidator) onEvent(msg []byte) {
	origin, hint := Untag(msg)
	if origin == iv.origin {
		return // our own write: the local cache is already current
	}
	iv.queue(string(hint), len(hint) == 0)
}

func (iv *Invalidator) queue(hint string, all bool) {
	iv.mu.Lock()
	if all || len(iv.hints) >= maxHints {
		iv.all = true
		iv.hints = map[string]bool{}
	} else if !iv.all {
		iv.hints[hint] = true
	}
	iv.mu.Unlock()
	select {
	case iv.kick <- struct{}{}:
	default:
	}
}

func (iv *Invalidator) run() {
	defer close(iv.done)
	for {
		select {
		case <-iv.stop:
			return
		case <-iv.kick:
		}
		iv.mu.Lock()
		all := iv.all
		hints := make([]string, 0, len(iv.hints))
		for h := range iv.hints {
			hints = append(hints, h)
		}
		iv.all, iv.hints = false, map[string]bool{}
		iv.mu.Unlock()
		if !all && len(hints) == 0 {
			continue
		}
		sort.Strings(hints)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		func() {
			defer func() {
				if r := recover(); r != nil {
					iv.log.Error("cluster: cache invalidation panicked", "topic", iv.topic, "panic", r)
				}
			}()
			iv.apply(ctx, hints, all)
		}()
		cancel()
		iv.mu.Lock()
		iv.applied++
		iv.mu.Unlock()
	}
}

// Applied reports how many apply runs have completed (tests).
func (iv *Invalidator) Applied() int {
	if iv == nil {
		return 0
	}
	iv.mu.Lock()
	defer iv.mu.Unlock()
	return iv.applied
}

// Close unsubscribes and stops the worker after any apply in progress.
func (iv *Invalidator) Close() {
	if iv == nil {
		return
	}
	iv.mu.Lock()
	if iv.closed {
		iv.mu.Unlock()
		return
	}
	iv.closed = true
	iv.mu.Unlock()
	for _, u := range iv.unsubs {
		u()
	}
	close(iv.stop)
	<-iv.done
}
