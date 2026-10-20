package notify

import (
	"context"
	"github.com/braedonsaunders/ether/internal/newznab"
	"time"
)

// indexerAlert is protected by healthMu. A new outage replaces its pointer,
// so an older in-flight write cannot overwrite a later recovery or outage.
type indexerAlert struct {
	state         string
	pending, sent bool
	retryAt       time.Time
}

// ObserveIndexer alerts once per outage, retrying failed queue writes at most
// once a minute. Database waits never hold the shared health mutex.
func (s *Service) ObserveIndexer(e newznab.Event) {
	state := ""
	if !e.Success {
		state = "outage"
		if e.BudgetExhausted {
			state = "budget"
		}
	}
	key := e.Indexer + "\x00" + e.Kind
	s.healthMu.Lock()
	if s.indexerAlerts == nil {
		s.indexerAlerts = map[string]*indexerAlert{}
	}
	if state == "" {
		delete(s.indexerAlerts, key)
		s.healthMu.Unlock()
		return
	}
	a := s.indexerAlerts[key]
	if a == nil || a.state != state {
		a = &indexerAlert{state: state}
		s.indexerAlerts[key] = a
	}
	if a.sent || a.pending || s.opts.Now().Before(a.retryAt) {
		s.healthMu.Unlock()
		return
	}
	a.pending = true
	s.healthMu.Unlock()
	kind := "indexer.outage"
	message := "An indexer operation failed. See server logs for details."
	if e.BudgetExhausted {
		kind = "indexer.budget"
		message = "The indexer's daily budget has been exhausted."
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := s.Emit(ctx, Event{Kind: kind, Title: e.Indexer, Message: message, At: e.At})
	s.healthMu.Lock()
	if s.indexerAlerts[key] == a {
		a.pending = false
		if err == nil {
			a.sent = true
		} else {
			a.retryAt = s.opts.Now().Add(time.Minute)
		}
	}
	s.healthMu.Unlock()
	if err != nil {
		s.indexerLog.Fail(err)
	} else {
		s.indexerLog.OK()
	}
}
