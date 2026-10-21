package personalize

import (
	"context"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/state"
)

// Play tracking: watch-state events become plays (start, furthest point,
// completion, abandon), each attributed to the row the viewer most
// plausibly started it from.
const (
	// playGap ends a play: progress after this much silence is a new play.
	playGap = 30 * time.Minute
	// attributionWindow is how recently a title must have been shown for a
	// play of it to count for that row.
	attributionWindow = 48 * time.Hour
	// contextWindow is how recently the viewer's device context must have
	// been seen to label a play with it.
	contextWindow = 6 * time.Hour
)

type playKey struct {
	user int64
	key  core.RatingKey
}

type activePlay struct {
	rec  playRecord
	last time.Time
}

// onEvent is the state subscription: it must not block the writer, so
// events are queued for the event loop.
func (e *Engine) onEvent(ev state.Event) {
	select {
	case e.events <- ev:
	default:
		e.log.Debug("personalize: event queue full; dropping", "kind", ev.Kind, "user", ev.UserID)
	}
}

// eventLoop processes watch-state events in order.
func (e *Engine) eventLoop(ctx context.Context) {
	defer e.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-e.events:
			e.handleEvent(ctx, ev)
		case done := <-e.evSync:
			// Everything queued before the barrier is handled first.
			for {
				select {
				case ev := <-e.events:
					e.handleEvent(ctx, ev)
					continue
				default:
				}
				break
			}
			close(done)
		}
	}
}

// syncEvents returns once every event queued so far has been handled.
func (e *Engine) syncEvents(ctx context.Context) {
	done := make(chan struct{})
	select {
	case e.evSync <- done:
	case <-ctx.Done():
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// handleEvent updates play tracking and schedules a model rebuild when the
// event changes what we know about the viewer's taste.
func (e *Engine) handleEvent(ctx context.Context, ev state.Event) {
	if ev.At.IsZero() {
		ev.At = e.now()
	}
	switch ev.Kind {
	case state.EventProgress:
		e.trackProgress(ctx, ev)
		if ev.Completed || ev.State == "stopped" {
			e.scheduleRebuild(ev.UserID)
		}
	case state.EventScrobble, state.EventUnscrobble, state.EventRated:
		e.scheduleRebuild(ev.UserID)
	}
}

func (e *Engine) trackProgress(ctx context.Context, ev state.Event) {
	pk := playKey{ev.UserID, ev.Key}
	e.playMu.Lock()
	defer e.playMu.Unlock()
	// Forget plays that went quiet without a "stopped".
	for k, ap := range e.active {
		if ev.At.Sub(ap.last) > playGap {
			delete(e.active, k)
		}
	}
	ap := e.active[pk]
	if ap == nil {
		rec := playRecord{
			userID: ev.UserID, key: ev.Key, tasteKey: tasteKey(ev.Key),
			started: ev.At, updated: ev.At, duration: ev.Duration,
			startFraction: ev.Fraction, maxFraction: ev.Fraction,
		}
		if ev.Completed {
			rec.startFraction = 0
		}
		keys := []core.RatingKey{ev.Key}
		if tk := tasteKey(ev.Key); tk != ev.Key {
			keys = append(keys, tk, ev.Key.Parent())
		}
		// Make sure the impressions served just before this play are on
		// disk before looking for them.
		fctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		e.rec.flush(fctx)
		cancel()
		if a, ok, err := e.store.attribute(ctx, ev.UserID, keys, ev.At.Add(-attributionWindow), ev.At); err == nil && ok {
			rec.hubID, rec.kind = a.hubID, a.kind
		} else if err != nil {
			e.log.Debug("personalize: attribution failed", "err", err)
		}
		e.mu.Lock()
		hc, ok := e.lastCtx[ev.UserID]
		e.mu.Unlock()
		if ok && ev.At.Sub(hc.Now) <= contextWindow && ev.At.Sub(hc.Now) >= -time.Minute {
			rec.device = hc.Device
		}
		local := ev.At.In(e.loc)
		rec.daypart, rec.weekend = core.DaypartAt(local), core.IsWeekend(local)
		if err := e.store.insertPlay(ctx, &rec); err != nil {
			e.log.Warn("personalize: recording play failed", "err", err)
			return
		}
		e.rankerLabelPlay(ctx, &rec) // label the serve it came from (servelog.go)
		ap = &activePlay{rec: rec}
		e.active[pk] = ap
	}
	ap.last = ev.At
	ap.rec.updated = ev.At
	if ev.Duration > 0 {
		ap.rec.duration = ev.Duration
	}
	if ev.Fraction > ap.rec.maxFraction {
		ap.rec.maxFraction = ev.Fraction
	}
	if ev.Completed {
		ap.rec.completed = true
		ap.rec.abandoned = false
	}
	if ev.State == "stopped" {
		ap.rec.abandoned = !ap.rec.completed && ap.rec.maxFraction < abandonFraction
		delete(e.active, pk)
		if ap.rec.abandoned {
			e.recordAbandonBounce(ctx, ev.UserID, ap.rec.tasteKey, ev.At)
		}
	}
	if err := e.store.updatePlay(ctx, &ap.rec); err != nil {
		e.log.Warn("personalize: updating play failed", "err", err)
	}
}

// scheduleRebuild rebuilds a viewer's model shortly after their state
// changes, coalescing bursts (a season scrobble, a flurry of stops).
func (e *Engine) scheduleRebuild(userID int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	if t, ok := e.debounce[userID]; ok {
		t.Reset(e.opts.RebuildDebounce)
		return
	}
	e.debounce[userID] = time.AfterFunc(e.opts.RebuildDebounce, func() {
		e.mu.Lock()
		delete(e.debounce, userID)
		e.mu.Unlock()
		e.request(userID)
	})
}
