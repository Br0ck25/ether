package catalog

import (
	"context"
	"errors"
	"fmt"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/tmdb"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type chartClient struct {
	TMDB
	calls atomic.Int32
	fail  atomic.Bool
}

func (c *chartClient) TrendingMovies(ctx context.Context, w string, p int) (*tmdb.Paged[tmdb.Movie], error) {
	c.calls.Add(1)
	if c.fail.Load() {
		return nil, errors.New("offline")
	}
	out := &tmdb.Paged[tmdb.Movie]{TotalPages: 10}
	for n := 1; n <= 20; n++ {
		id := int64((p-1)*20 + n)
		out.Results = append(out.Results, tmdb.Movie{ID: id, Title: fmt.Sprint("Chart ", id), ReleaseDate: "2000-01-01", Popularity: 10})
	}
	return out, nil
}
func TestTrendingDurableDeepSharedAndOutage(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	client := &chartClient{}
	makeCat := func() *Catalog {
		c, err := New(db, client, config.Catalog{}, nil, WithNow(func() time.Time { return now }))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := makeCat()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ts, err := c.List(ctx, "trending_day", core.TypeMovie, 200)
			if err != nil || len(ts) != 200 {
				t.Errorf("chart: %d %v", len(ts), err)
			}
		}()
	}
	wg.Wait()
	if n := client.calls.Load(); n != 10 {
		t.Fatalf("shared chart fetched %d pages, want 10", n)
	}
	c = makeCat()
	ts, err := c.List(ctx, "trending_day", core.TypeMovie, 200)
	if err != nil || len(ts) != 200 || client.calls.Load() != 10 {
		t.Fatal("restart lost chart", len(ts), err)
	}
	now = now.Add(2 * time.Hour)
	client.fail.Store(true)
	if _, err = c.refreshTrend(ctx, "trending_day", core.TypeMovie); err == nil {
		t.Fatal("outage accepted")
	}
	if ts, err = c.List(ctx, "trending_day", core.TypeMovie, 200); err != nil || len(ts) != 200 {
		t.Fatal("outage erased chart", len(ts), err)
	}
	if c.TrendStatus(ctx)[0].State != "stale" {
		t.Fatal("outage not visible")
	}
	n := client.calls.Load()
	_, _ = c.refreshTrend(ctx, "trending_day", core.TypeMovie)
	if client.calls.Load() != n {
		t.Fatal("retry backoff ignored")
	}
	now = now.Add(8 * 24 * time.Hour)
	ts, _ = c.List(ctx, "trending_day", core.TypeMovie, 200)
	if len(ts) != 0 {
		t.Fatal("expired chart advertised as current")
	}
}
