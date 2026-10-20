package notify

import (
	"context"
	"github.com/braedonsaunders/ether/internal/newznab"
	"time"
)

// ObserveIndexer alerts once when an indexer's searches or grabs start
// failing (or exhaust its budget), like SampleHealth: every failed request
// of an outage would otherwise queue its own notification. A success
// re-arms the alert.
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
	if s.indexerStates == nil {
		s.indexerStates = map[string]string{}
	}
	old := s.indexerStates[key]
	if state == "" {
		delete(s.indexerStates, key)
	} else {
		s.indexerStates[key] = state
	}
	s.healthMu.Unlock()
	if state == "" || state == old {
		return
	}
	kind := "indexer.outage"
	message := "An indexer operation failed. See server logs for details."
	if e.BudgetExhausted {
		kind = "indexer.budget"
		message = "The indexer's daily budget has been exhausted."
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.Emit(ctx, Event{Kind: kind, Title: e.Indexer, Message: message, At: e.At})
}
