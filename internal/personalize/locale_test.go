package personalize

import (
	"context"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/state"
	"github.com/braedonsaunders/ether/internal/store"
)

// A keyword starting with a multi-byte letter must name its cluster with
// valid UTF-8, not a split first byte.
func TestThemeWordsMultiByteFirstLetter(t *testing.T) {
	for in, want := range map[string]string{
		"ménage à trois": "Ménage-À-Trois",
		"émigré":         "Émigré",
		"time travel":    "Time-Travel",
		"coming of age":  "Coming-of-Age",
	} {
		got := themeWords(in)
		if !utf8.ValidString(got) || got != want {
			t.Errorf("themeWords(%q) = %q, want %q", in, got, want)
		}
	}
}

// Year in Review counts the server's calendar year: a film finished on New
// Year's Eve evening local time (already January 1 in UTC) belongs to the
// year that is ending.
func TestYearInReviewUsesServerLocalYear(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	est := time.FixedZone("EST", -5*3600)
	nye := time.Date(2025, 12, 31, 22, 0, 0, 0, est) // 2026-01-01 03:00 UTC
	cat := newFakeCatalog(testNow)
	st := state.New(db, state.Options{Now: func() time.Time { return nye }})
	key := movieKey(0, 1)
	if err := st.Scrobble(ctx, 1, key); err != nil {
		t.Fatal(err)
	}
	d := NewDiscovery(db, cat, st, nil)
	d.now = func() time.Time { return time.Date(2026, 3, 1, 12, 0, 0, 0, est) }
	h, err := d.YearInReview(ctx, &core.User{ID: 1}, 2025, 20)
	if err != nil || len(h.Items) != 1 {
		t.Fatalf("2025 review = %d items, %v; want the New Year's Eve film", len(h.Items), err)
	}
	h, err = d.YearInReview(ctx, &core.User{ID: 1}, 2026, 20)
	if err != nil || len(h.Items) != 0 {
		t.Fatalf("2026 review = %d items, %v; want none", len(h.Items), err)
	}
}
