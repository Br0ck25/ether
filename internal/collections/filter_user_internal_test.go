package collections

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// A member missing from the index has no known content rating; a viewer
// with a rating limit must not see it (unknown ratings exceed every limit).
func TestFilterForUserHidesUnknownMembersFromRestrictedViewers(t *testing.T) {
	s := &Service{log: slog.Default(), now: time.Now}
	known, unknown := core.MovieKey(1), core.MovieKey(2)
	snap := &snapshot{entries: map[core.RatingKey]*entry{known: {key: known, crating: "PG"}}}
	keys := []core.RatingKey{known, unknown}
	kid := &core.User{ID: 2, MaxContentRating: "PG-13"}
	if got := s.filterForUser(context.Background(), keys, personalRules{}, kid, snap); len(got) != 1 || got[0] != known {
		t.Fatalf("restricted viewer sees %v, want only %v", got, known)
	}
	if got := s.filterForUser(context.Background(), keys, personalRules{}, &core.User{ID: 3}, snap); len(got) != 2 {
		t.Fatalf("unrestricted viewer sees %v", got)
	}
}
