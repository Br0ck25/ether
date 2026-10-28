package app

import (
	"context"
	"github.com/braedonsaunders/ether/internal/availability"
	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/resolver"
	"github.com/braedonsaunders/ether/internal/store"
	"log/slog"
	"testing"
	"time"
)

type premiereNoNetwork struct{ t *testing.T }

func (f premiereNoNetwork) FeedSources() []core.ReleaseFeed {
	return []core.ReleaseFeed{{ID: "f", Name: "Fake", Generation: "one"}}
}
func (f premiereNoNetwork) Recent(context.Context, string, int, int) ([]core.Release, error) {
	f.t.Fatal("unexpected feed request")
	return nil, nil
}

func TestPremiereVisibilityBeforePaginationAndSearch(t *testing.T) {
	db := store.OpenTest(t)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	cfg := config.Defaults(t.TempDir())
	cfg.Usenet.Providers = []config.UsenetProvider{{Host: "fake", Enabled: true}}
	c, err := catalog.New(db, nil, cfg.Catalog, slog.New(slog.DiscardHandler), catalog.WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"A Empty", "B Future Child", "C Unknown Child", "D Premiere", "E Classic"} {
		date := now.Add(-time.Hour)
		if i == 4 {
			date = now.Add(-48 * time.Hour)
		}
		title := core.Title{Key: core.ShowKey(int64(i + 1)), Type: core.TypeShow, Title: name, Released: date, Language: "en"}
		if err = c.UpsertTitle(t.Context(), title, true); err != nil {
			t.Fatal(err)
		}
	}
	// Preserve unknown/future children in the local metadata-only query;
	// neither may be treated as an unmirrored S01E01 premiere.
	for _, id := range []int64{2, 3, 4} {
		date := now.Add(-time.Hour)
		if id == 2 {
			date = now.Add(time.Hour)
		}
		if id == 3 {
			date = time.Time{}
		}
		ep := core.Title{Key: core.EpisodeKey(id, 1, 1), Type: core.TypeEpisode, ShowKey: core.ShowKey(id), ShowTitle: []string{"", "", "B Future Child", "C Unknown Child", "D Premiere"}[id], Season: 1, Episode: 1, Released: date}
		if err = c.UpsertTitle(t.Context(), ep, false); err != nil {
			t.Fatal(err)
		}
	}
	r := resolver.New(nil, nil, nil, cfg, resolver.Options{})
	defer r.Close()
	a, err := availability.New(availability.Deps{DB: db, Config: func() config.Config { return cfg }, Feeds: premiereNoNetwork{t}, Catalog: c, Validator: r, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ep, err := c.CachedTitle(t.Context(), core.EpisodeKey(4, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	a.RecordSearch(t.Context(), ep, []core.Release{{ID: "proof", Name: "D.Premiere.S01E01.1080p.WEB-DL", Indexer: "Fake"}})
	wrapped := availabilityCatalog{c, a}
	for offset, want := range []core.RatingKey{core.ShowKey(4), core.ShowKey(5)} {
		p, e := wrapped.Browse(t.Context(), core.BrowseQuery{Filter: core.Filter{Type: core.TypeShow}, Sort: core.SortTitle, Limit: 1, Offset: offset})
		if e != nil || p.Total != 2 || len(p.Items) != 1 || p.Items[0].Key != want {
			t.Fatalf("page %d: %+v %v", offset, p, e)
		}
	}
	got, err := wrapped.Search(t.Context(), "Empty", []core.MediaType{core.TypeShow}, 20)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty premiere in search: %+v %v", got, err)
	}
	children, err := c.CachedDiscoveryEpisodes(t.Context(), []core.RatingKey{core.ShowKey(2), core.ShowKey(3)})
	if err != nil || len(children[core.ShowKey(2)]) != 1 || len(children[core.ShowKey(3)]) != 1 {
		t.Fatalf("lost unreleased metadata: %+v %v", children, err)
	}
}
