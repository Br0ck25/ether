package catalog

import (
	"context"
	"encoding/json"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/libraryfilter"
	"github.com/braedonsaunders/ether/internal/store"
	"testing"
	"time"
)

func TestLibraryFilterPreviewBrowseAndPolicyAgree(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err := New(db, nil, config.Catalog{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	titles := []core.Title{
		{Type: core.TypeMovie, Countries: []string{"India"}, Language: "en", Rating: 7, Genres: []string{"Drama"}},
		{Type: core.TypeShow, Countries: []string{"IN", "US"}, Language: "en", Rating: 8},
		{Type: core.TypeMovie, Countries: []string{"United States of America"}, Language: "hi", Rating: 4},
		{Type: core.TypeMovie, Language: "hi", Rating: 5},
		{Type: core.TypeMovie, Countries: []string{"CA"}, Language: "en", Rating: 8},
	}
	for i := range titles {
		tt := &titles[i]
		tt.Key = core.MovieKey(int64(i + 1))
		if tt.Type == core.TypeShow {
			tt.Key = core.ShowKey(int64(i + 1))
		}
		tt.Title = "Sample"
		tt.Year = 2000
		tt.Released = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		if err = c.UpsertTitle(ctx, *tt, true); err != nil {
			t.Fatal(err)
		}
	}
	rule := func(f, op, v string) core.CollectionRule {
		return core.CollectionRule{Field: f, Op: op, Value: json.RawMessage(v)}
	}
	for _, specs := range [][]core.LibraryExclusion{
		nil,
		{{Name: "No matches", MediaType: "mixed", Match: "all", Rules: []core.CollectionRule{rule("year", "gte", `2100`)}}},
		{{Name: "Country", MediaType: "mixed", Match: "all", Rules: []core.CollectionRule{rule("country", "in", `["IN"]`)}}},
		{{Name: "Movie country + rating", MediaType: "movie", Match: "all", Rules: []core.CollectionRule{rule("country", "in", `["IN"]`), rule("rating", "gte", `6`)}}},
		{{Name: "Countries", MediaType: "movie", Match: "any", Rules: []core.CollectionRule{rule("country", "is", "\"US\""), rule("country", "is", "\"CA\"")}}, {Name: "TV", MediaType: "show", Match: "all", Rules: []core.CollectionRule{rule("rating", "between", `[7,9]`)}}},
		{{Name: "Known non-India", MediaType: "mixed", Match: "all", Rules: []core.CollectionRule{rule("country", "notIn", `["IN"]`)}}},
	} {
		f, err := libraryfilter.Compile(specs)
		if err != nil {
			t.Fatal(err)
		}
		hidden := 0
		for i := range titles {
			if f.Hidden(&titles[i]) {
				hidden++
			}
		}
		c.UpdateConfig(config.Catalog{Exclusions: specs})
		p, err := c.Browse(ctx, core.BrowseQuery{})
		if err != nil || p.Total != len(titles)-hidden {
			t.Fatalf("SQL/evaluator differ: total=%d hidden=%d err=%v", p.Total, hidden, err)
		}
		raw, err := c.PreviewVisibility(ctx, specs)
		if err != nil {
			t.Fatal(err)
		}
		preview := raw.(VisibilityPreview)
		if preview.Items == nil {
			t.Fatal("empty preview must encode items as [], not null")
		}
		if preview.Hidden != hidden || preview.UnknownCountry != 1 {
			t.Fatalf("preview=%+v", preview)
		}
		for _, tt := range p.Items {
			if !c.Visible(&tt) {
				t.Fatal("hidden title served")
			}
		}
	}
	c.UpdateConfig(config.Catalog{})
	p, _ := c.Browse(ctx, core.BrowseQuery{})
	if p.Total != 5 {
		t.Fatal("clearing rules left cached exclusions", p.Total)
	}
}
