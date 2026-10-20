package livetv

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// pagedCatalog serves n movies and, like the real catalogue, at most 500
// per Browse call.
type pagedCatalog struct {
	core.Catalog
	n int
}

func (c pagedCatalog) Browse(_ context.Context, q core.BrowseQuery) (core.Page, error) {
	limit := min(q.Limit, 500)
	var items []core.Title
	for i := q.Offset; i < c.n && len(items) < limit; i++ {
		items = append(items, core.Title{Key: core.MovieKey(int64(i + 1)), Type: core.TypeMovie, Title: "T", Runtime: time.Hour})
	}
	return core.Page{Items: items, Total: c.n}, nil
}

// A filter channel takes up to 2000 titles even though one Browse page is
// capped lower.
func TestFilterChannelExpandsPastOneBrowsePage(t *testing.T) {
	s := testService(t, Options{Catalog: pagedCatalog{n: 1200}})
	c := Channel{Kind: "virtual", Filter: &core.Filter{Type: core.TypeMovie}}
	if err := s.expand(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Items) != 1200 {
		t.Fatalf("expanded %d items, want 1200", len(c.Items))
	}
	big := Channel{Kind: "virtual", Filter: &core.Filter{Type: core.TypeMovie}}
	s.o.Catalog = pagedCatalog{n: 5000}
	if err := s.expand(context.Background(), &big); err != nil {
		t.Fatal(err)
	}
	if len(big.Items) != 2000 {
		t.Fatalf("expanded %d items, want the 2000 cap", len(big.Items))
	}
}
