package cluster

import (
	"context"
	"log/slog"
	"sync"
)

// leadership is this node's leader state and its change subscribers.
type leadership struct {
	mu     sync.Mutex
	leader bool
	epoch  int64
	term   uint64 // incremented on every acquisition (local)
	next   int
	subs   map[int]func(bool)
}

func newLeadership() *leadership { return &leadership{subs: map[int]func(bool){}} }

func (l *leadership) get() (leader bool, term uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.leader, l.term
}

func (l *leadership) isLeader() bool {
	v, _ := l.get()
	return v
}

func (l *leadership) currentEpoch() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.leader {
		return 0
	}
	return l.epoch
}

// set records a leadership change and notifies subscribers; it is a no-op
// when nothing changed.
func (l *leadership) set(leader bool, epoch int64) {
	l.mu.Lock()
	if l.leader == leader {
		l.mu.Unlock()
		return
	}
	l.leader = leader
	if leader {
		l.epoch = epoch
		l.term++
	} else {
		l.epoch = 0
	}
	fns := make([]func(bool), 0, len(l.subs))
	for _, fn := range l.subs {
		fns = append(fns, fn)
	}
	l.mu.Unlock()
	for _, fn := range fns {
		fn(leader)
	}
}

func (l *leadership) subscribe(fn func(bool)) func() {
	if fn == nil {
		return func() {}
	}
	l.mu.Lock()
	l.next++
	id := l.next
	l.subs[id] = fn
	l.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			delete(l.subs, id)
			l.mu.Unlock()
		})
	}
}

// runAsLeader implements Cluster.RunAsLeader over a leadership.
func runAsLeader(ctx context.Context, log *slog.Logger, name string, l *leadership, fn func(context.Context)) {
	wake := make(chan struct{}, 1)
	defer l.subscribe(func(bool) {
		select {
		case wake <- struct{}{}:
		default:
		}
	})()

	var (
		cancel  context.CancelFunc
		done    chan struct{}
		runTerm uint64 // term the running (or finished) fn started in
		ran     bool   // fn already ran in runTerm
	)
	stop := func() {
		if cancel != nil {
			cancel()
			<-done
			cancel, done = nil, nil
		}
	}
	defer stop()
	for {
		if ctx.Err() != nil {
			return
		}
		lead, term := l.get()
		if cancel != nil && (!lead || term != runTerm) {
			log.Info("cluster: leadership lost; stopping leader job", "job", name)
			stop()
		}
		if lead && cancel == nil && !(ran && term == runTerm) {
			jctx, c := context.WithCancel(ctx)
			d := make(chan struct{})
			cancel, done, runTerm, ran = c, d, term, true
			log.Info("cluster: starting leader job", "job", name)
			go func() {
				defer close(d)
				fn(jctx)
			}()
		}
		var finished <-chan struct{}
		if done != nil {
			finished = done
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-finished:
			// fn returned while still leading: leave it stopped until
			// leadership is lost and regained.
			cancel()
			cancel, done = nil, nil
		}
	}
}
