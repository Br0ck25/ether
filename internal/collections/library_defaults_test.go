package collections

import (
	"context"
	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"testing"
	"time"
)

func TestLibraryDefaultsStayLocalAndDeletedDefaultsStayDeleted(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	now := time.Now()
	cat, err := catalog.New(db, nil, config.Catalog{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, typ := range []core.MediaType{core.TypeMovie, core.TypeShow} {
		k := core.MovieKey(1)
		if typ == core.TypeShow {
			k = core.ShowKey(2)
		}
		tt := core.Title{Key: k, Type: typ, Title: "Local", Year: 2000, Released: now.AddDate(-10, 0, 0), Genres: []string{"Drama"}, Studios: []string{"HBO", "Warner Bros."}, Popularity: float64(i + 10)}
		if err = cat.UpsertTitle(ctx, tt, true); err != nil {
			t.Fatal(err)
		}
	}
	makeService := func() *Service {
		s, err := New(db, cat, nil, WithNow(func() time.Time { return now }))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := makeService()
	specs, err := s.ListSpecs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var hbo string
	for _, sp := range specs {
		if sp.BuiltIn && sp.Targets.CollectionsTab {
			t.Fatal("discovery source in collections", sp.Name)
		}
		if sp.Name == "HBO" {
			hbo = sp.ID
			if !sp.LocalOnly || !sp.Targets.CollectionsTab || sp.Targets.HomeRow || sp.ItemCount != 1 {
				t.Fatalf("default: %+v", sp)
			}
		}
	}
	if hbo == "" {
		t.Fatal("network default missing")
	}
	if err = s.DeleteSpec(ctx, hbo); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	s = makeService()
	specs, _ = s.ListSpecs(ctx)
	for _, sp := range specs {
		if sp.Name == "HBO" {
			t.Fatal("deleted default resurrected")
		}
	}
}
