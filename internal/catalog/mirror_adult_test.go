package catalog_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// Adult titles are hidden everywhere the catalogue serves titles (Browse,
// Get, lists); the mirror reads behind collections must hide them too, or
// a catalogue sync with adult titles enabled leaks them into collections.
func TestMirrorReadsExcludeAdultTitles(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c, err := catalog.New(db, nil, config.Catalog{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	rel := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
	plain := core.Title{Key: core.MovieKey(700), Type: core.TypeMovie, IDs: core.IDs{TMDB: 700}, Title: "Plain", Released: rel, Popularity: 50}
	adult := core.Title{Key: core.MovieKey(701), Type: core.TypeMovie, IDs: core.IDs{TMDB: 701}, Title: "Adult", Released: rel, Popularity: 60, Adult: true}
	for _, ti := range []core.Title{plain, adult} {
		if err := c.UpsertTitle(ctx, ti, true); err != nil {
			t.Fatal(err)
		}
	}
	m := catalog.NewMirror(db, nil)
	snap, err := m.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap) != 1 || snap[0].Title.Key != plain.Key {
		t.Fatalf("snapshot = %+v, want only the non-adult title", snap)
	}
	fp, err := m.Fingerprint(ctx)
	if err != nil || fp.Count != 1 {
		t.Fatalf("fingerprint = %+v %v", fp, err)
	}
	ts, err := m.Titles(ctx, []core.RatingKey{adult.Key, plain.Key})
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 || ts[0].Key != plain.Key {
		t.Fatalf("titles = %+v, want only the non-adult title", ts)
	}
}
