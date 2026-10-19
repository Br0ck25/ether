package notify

import (
	"context"
	"github.com/braedonsaunders/ether/internal/newznab"
	"time"
)

func (s *Service) ObserveIndexer(e newznab.Event) {
	if e.Success {
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
