package collections_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func setup(t *testing.T) (*collections.Service, *catalog.Catalog) {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := config.Catalog{Language: "en-US", Region: "US", MinPopularity: 5, MinVotes: 100, ShowUnreleased: true}
	cat, err := catalog.New(db, nil, cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	seed := []core.Title{
		{Key: core.MovieKey(1), Type: core.TypeMovie, IDs: core.IDs{TMDB: 1}, Title: "Alien Invasion", Genres: []string{"Science Fiction"},
			Year: 1985, Released: time.Date(1985, 6, 1, 0, 0, 0, 0, time.UTC), Runtime: 100 * time.Minute, Rating: 8.5, Popularity: 50, VoteCount: 5000,
			ContentRating: "PG-13", CollectionID: 10, CollectionName: "Saga Collection", PosterPath: "/a.jpg"},
		{Key: core.MovieKey(2), Type: core.TypeMovie, IDs: core.IDs{TMDB: 2}, Title: "Saga Returns", Genres: []string{"Science Fiction"},
			Year: 1987, Released: time.Date(1987, 6, 1, 0, 0, 0, 0, time.UTC), Runtime: 110 * time.Minute, Rating: 8.1, Popularity: 40, VoteCount: 4000,
			ContentRating: "R", CollectionID: 10, CollectionName: "Saga Collection"},
		{Key: core.MovieKey(3), Type: core.TypeMovie, IDs: core.IDs{TMDB: 3}, Title: "Modern Long Sci-Fi", Genres: []string{"Science Fiction"},
			Year: 2020, Released: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Runtime: 180 * time.Minute, Rating: 7.0, Popularity: 60, VoteCount: 900,
			ContentRating: "PG-13"},
		{Key: core.MovieKey(4), Type: core.TypeMovie, IDs: core.IDs{TMDB: 4}, Title: "Lonely Franchise Film", Genres: []string{"Drama"},
			Year: 2001, Released: time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC), Rating: 9.0, Popularity: 30, CollectionID: 99, CollectionName: "Lonely"},
		{Key: core.ShowKey(50), Type: core.TypeShow, IDs: core.IDs{TMDB: 50}, Title: "Great Show", Genres: []string{"Drama"},
			Year: 2021, Released: time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), Rating: 9.1, Popularity: 70, ContentRating: "TV-MA"},
	}
	for _, title := range seed {
		if err := cat.UpsertTitle(ctx, title, true); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := collections.New(db, cat, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return svc, cat
}

func TestSeededSmartEvaluation(t *testing.T) {
	ctx := context.Background()
	svc, _ := setup(t)
	list, err := svc.List(ctx, nil, core.TypeMovie)
	if err != nil {
		t.Fatal(err)
	}
	var eighties *core.Collection
	for i := range list {
		if list[i].Title == "80s Sci-Fi Under 2 Hours" {
			eighties = &list[i]
		}
	}
	if eighties == nil {
		t.Fatalf("seeded smart missing in %v", list)
	}
	page, err := svc.Items(ctx, nil, eighties.Key, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 { // Alien Invasion + Saga Returns; modern film excluded by decade/runtime
		t.Fatalf("80s smart: %+v", page)
	}
	// Load the second title runtime check: both < 2h.
	for _, it := range page.Items {
		if it.Runtime > 2*time.Hour || it.Year < 1980 || it.Year > 1989 {
			t.Fatalf("smart item out of rule: %+v", it)
		}
	}
}

func TestCreateAndDeleteSmart(t *testing.T) {
	ctx := context.Background()
	svc, _ := setup(t)
	user := &core.User{ID: 7, Username: "kid", MaxContentRating: "PG-13"}
	created, err := svc.CreateSmart(ctx, core.Collection{
		Title: "My Sci-Fi", Type: core.TypeMovie, Sort: core.SortRating, OwnerUserID: 7,
		Filter: &core.Filter{Type: core.TypeMovie, Genres: []string{"Science Fiction"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Key.Kind() != core.KindSmart || created.Kind != "smart" {
		t.Fatalf("created: %+v", created)
	}
	// Owner sees it with rating limit (R excluded -> 2 of 3).
	page, err := svc.Items(ctx, user, created.Key, 0, 10)
	if err != nil || page.Total != 2 {
		t.Fatalf("user smart items: %+v %v", page, err)
	}
	// Another user cannot see it.
	other := &core.User{ID: 8}
	if _, err := svc.Get(ctx, other, created.Key); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("other user Get: %v", err)
	}
	if _, err := svc.Items(ctx, other, created.Key, 0, 10); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("other user Items: %v", err)
	}
	list, err := svc.List(ctx, other, core.TypeMovie)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.Key == created.Key {
			t.Fatal("other user sees private smart in list")
		}
	}
	// Validation.
	if _, err := svc.CreateSmart(ctx, core.Collection{Filter: &core.Filter{}}); err == nil {
		t.Fatal("titleless smart should fail")
	}
	if _, err := svc.CreateSmart(ctx, core.Collection{Title: "x"}); err == nil {
		t.Fatal("filterless smart should fail")
	}
	if err := svc.DeleteSmart(ctx, created.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, user, created.Key); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := svc.DeleteSmart(ctx, created.Key); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("double delete: %v", err)
	}
	// Builtins cannot be deleted.
	if err := svc.DeleteSmart(ctx, collections.BuiltinKey("trending-week-movie")); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("builtin delete: %v", err)
	}
}

func TestFranchiseCollections(t *testing.T) {
	ctx := context.Background()
	svc, _ := setup(t)
	list, err := svc.List(ctx, nil, core.TypeMovie)
	if err != nil {
		t.Fatal(err)
	}
	var saga *core.Collection
	for i := range list {
		if list[i].Kind == "franchise" {
			if list[i].Title == "Lonely" {
				t.Fatal("single-member franchise must not be listed")
			}
			saga = &list[i]
		}
	}
	if saga == nil || saga.Key != core.CollectionKey(10) || saga.ChildCount != 2 {
		t.Fatalf("saga: %+v (list %v)", saga, list)
	}
	page, err := svc.Items(ctx, nil, saga.Key, 0, 10)
	if err != nil || page.Total != 2 || page.Items[0].Title != "Alien Invasion" {
		t.Fatalf("saga items: %+v %v", page, err) // release order
	}
	// Rating limit applies: PG-13 user loses the R sequel.
	kid := &core.User{ID: 3, MaxContentRating: "PG-13"}
	page, err = svc.Items(ctx, kid, saga.Key, 0, 10)
	if err != nil || page.Total != 1 {
		t.Fatalf("saga kid items: %+v %v", page, err)
	}
	got, err := svc.Get(ctx, kid, saga.Key)
	if err != nil || got.ChildCount != 1 {
		t.Fatalf("saga get kid: %+v %v", got, err)
	}
}

func TestBuiltinLiveCollections(t *testing.T) {
	ctx := context.Background()
	svc, _ := setup(t)
	key := collections.BuiltinKey("top-rated-movie")
	if key == 0 || key.Kind() != core.KindSmart {
		t.Fatalf("builtin key %v", key)
	}
	c, err := svc.Get(ctx, nil, key)
	if err != nil || c.Title != "Top Rated Movies" {
		t.Fatalf("builtin get: %+v %v", c, err)
	}
	// Offline catalog.List falls back to Browse rating-desc: Lonely (9.0) first.
	page, err := svc.Items(ctx, nil, key, 0, 2)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("builtin items: %+v %v", page, err)
	}
	if page.Items[0].Title != "Lonely Franchise Film" {
		t.Fatalf("top rated order: %+v", page.Items)
	}
	// List by type partitions builtins.
	shows, err := svc.List(ctx, nil, core.TypeShow)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range shows {
		if c.Type == core.TypeMovie {
			t.Fatalf("movie collection in show list: %+v", c)
		}
	}
	if _, err := svc.Get(ctx, nil, core.MovieKey(1)); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("movie key Get: %v", err)
	}
}

func TestSmartNeverStale(t *testing.T) {
	ctx := context.Background()
	svc, cat := setup(t)
	created, err := svc.CreateSmart(ctx, core.Collection{
		Title: "All Sci-Fi", Type: core.TypeMovie,
		Filter: &core.Filter{Type: core.TypeMovie, Genres: []string{"Science Fiction"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, _ := svc.Items(ctx, nil, created.Key, 0, 10)
	if page.Total != 3 {
		t.Fatalf("before add: %+v", page)
	}
	// A new matching title appears with no collection rebuild.
	if err := cat.UpsertTitle(ctx, core.Title{Key: core.MovieKey(9), Type: core.TypeMovie, IDs: core.IDs{TMDB: 9},
		Title: "New Sci-Fi", Genres: []string{"Science Fiction"}, Popularity: 99,
		Released: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}, true); err != nil {
		t.Fatal(err)
	}
	page, _ = svc.Items(ctx, nil, created.Key, 0, 10)
	if page.Total != 4 || page.Items[0].Title != "New Sci-Fi" {
		t.Fatalf("after add: %+v", page)
	}
}
