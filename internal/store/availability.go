package store

import (
	"sync"
	"time"
)

// Availability is a node's shared view of whether the database is
// reachable. Writers that can defer their work (Outbox) and the cluster
// heartbeat report outcomes to it; once one of them sees an outage the
// others queue at once instead of each waiting out its own timeout, and
// all of them flush as soon as any write succeeds again.
//
// A nil *Availability is always available and ignores reports.
type Availability struct {
	now func() time.Time

	mu      sync.Mutex
	down    bool
	since   time.Time
	lastErr string
	up      chan struct{} // closed while available; replaced on each outage
	subs    map[int]func(bool)
	next    int
}

// NewAvailability returns a tracker that starts out available. now may be
// nil (time.Now).
func NewAvailability(now func() time.Time) *Availability {
	if now == nil {
		now = time.Now
	}
	up := make(chan struct{})
	close(up)
	return &Availability{now: now, up: up, subs: map[int]func(bool){}}
}

// AvailabilityState is a snapshot for health reports.
type AvailabilityState struct {
	Reachable bool `json:"reachable"`
	// DownSince is when the current outage was first observed.
	DownSince time.Time `json:"downSince,omitzero"`
	LastError string    `json:"lastError,omitempty"`
}

// Report records the outcome of a database operation: nil marks the
// database reachable; an error that IsUnavailable marks it unreachable;
// any other error says nothing about reachability. It returns whether err
// was an outage.
func (a *Availability) Report(err error) bool {
	if err == nil {
		a.OK()
		return false
	}
	if !IsUnavailable(err) {
		return false
	}
	a.Fail(err)
	return true
}

// Fail marks the database unreachable.
func (a *Availability) Fail(err error) {
	if a == nil {
		return
	}
	a.mu.Lock()
	if err != nil {
		a.lastErr = err.Error()
	}
	if a.down {
		a.mu.Unlock()
		return
	}
	a.down, a.since = true, a.now()
	a.up = make(chan struct{})
	fns := a.subsLocked()
	a.mu.Unlock()
	for _, fn := range fns {
		fn(false)
	}
}

// OK marks the database reachable again.
func (a *Availability) OK() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if !a.down {
		a.mu.Unlock()
		return
	}
	a.down, a.since, a.lastErr = false, time.Time{}, ""
	close(a.up)
	fns := a.subsLocked()
	a.mu.Unlock()
	for _, fn := range fns {
		fn(true)
	}
}

func (a *Availability) subsLocked() []func(bool) {
	out := make([]func(bool), 0, len(a.subs))
	for _, fn := range a.subs {
		out = append(out, fn)
	}
	return out
}

// Down reports whether the database is currently believed unreachable.
func (a *Availability) Down() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.down
}

// Up returns a channel that is closed while (or as soon as) the database
// is reachable.
func (a *Availability) Up() <-chan struct{} {
	if a == nil {
		c := make(chan struct{})
		close(c)
		return c
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.up
}

// State snapshots the tracker.
func (a *Availability) State() AvailabilityState {
	if a == nil {
		return AvailabilityState{Reachable: true}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return AvailabilityState{Reachable: !a.down, DownSince: a.since, LastError: a.lastErr}
}

// Subscribe calls fn(reachable) on every transition, synchronously from
// the reporting goroutine (keep it quick).
func (a *Availability) Subscribe(fn func(reachable bool)) (unsubscribe func()) {
	if a == nil || fn == nil {
		return func() {}
	}
	a.mu.Lock()
	id := a.next
	a.next++
	a.subs[id] = fn
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		delete(a.subs, id)
		a.mu.Unlock()
	}
}
