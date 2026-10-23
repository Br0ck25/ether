package catalog

import (
	"fmt"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"testing"
	"time"
)

func TestVisibilityExclusionsPrecedePaginationAndCount(t *testing.T) {
	c, err := New(store.OpenTest(t), nil, config.Catalog{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 7; i++ {
		title := core.Title{Key: core.MovieKey(i), Type: core.TypeMovie, Title: fmt.Sprint(i), SortTitle: fmt.Sprint(i), Released: time.Now().Add(-time.Hour)}
		if err := c.upsert(t.Context(), title, upsertOpts{browsable: true}); err != nil {
			t.Fatal(err)
		}
	}
	q := core.BrowseQuery{Filter: core.Filter{Type: core.TypeMovie, ExcludeKeys: []core.RatingKey{core.MovieKey(1), core.MovieKey(3)}}, Sort: core.SortTitle, Limit: 2, Offset: 1}
	for range 2 {
		p, err := c.Browse(t.Context(), q)
		if err != nil || p.Total != 5 || len(p.Items) != 2 || p.Items[0].Key != core.MovieKey(4) || p.Items[1].Key != core.MovieKey(5) {
			t.Fatalf("%+v %v", p, err)
		}
	}
	q.Filter.ExcludeKeys = nil
	p, err := c.Browse(t.Context(), q)
	if err != nil || p.Total != 7 || p.Items[0].Key != core.MovieKey(2) {
		t.Fatal("visibility polluted unfiltered cache", p, err)
	}
}
