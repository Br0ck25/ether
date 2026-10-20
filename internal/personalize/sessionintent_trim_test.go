package personalize

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Re-opening a title refreshes its vote; when the per-viewer cap trims the
// list, the oldest votes go, never the one just refreshed.
func TestIntentRefreshedOpenSurvivesTrim(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	u := &core.User{ID: 1, Username: "alice"}
	open := func(key core.RatingKey) {
		h.e.RecordDetailOpen(ctx, u, &core.Title{Key: key, Type: core.TypeMovie, Genres: []string{"Horror"}, Year: 1994})
	}
	refreshed := movieKey(9, 0)
	open(refreshed)
	for i := 1; i < intentMaxOpens; i++ {
		h.clock.Set(testNow.Add(time.Duration(i) * time.Second))
		open(movieKey(9, i))
	}
	h.clock.Set(testNow.Add(time.Minute))
	open(refreshed) // refreshed: now the newest vote
	h.clock.Set(testNow.Add(2 * time.Minute))
	open(movieKey(9, intentMaxOpens)) // over the cap: the oldest goes

	hs := h.e.hookState()
	hs.mu.Lock()
	defer hs.mu.Unlock()
	opens := hs.intent[u.ID].opens
	if len(opens) != intentMaxOpens {
		t.Fatalf("opens = %d, want %d", len(opens), intentMaxOpens)
	}
	has := map[core.RatingKey]bool{}
	for _, o := range opens {
		has[o.key] = true
	}
	if !has[tasteKey(refreshed)] {
		t.Error("the refreshed open was trimmed")
	}
	if has[tasteKey(movieKey(9, 1))] {
		t.Error("the oldest open survived the trim")
	}
}
