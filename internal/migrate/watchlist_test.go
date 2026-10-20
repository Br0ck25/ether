package migrate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/watchlist"
)

type watchlistCatalog struct{ catalogFake }

func (watchlistCatalog) Get(_ context.Context, k core.RatingKey) (*core.Title, error) {
	return &core.Title{Key: k, Title: "Example", Type: core.TypeMovie, Year: 2024, IDs: core.IDs{TMDB: k.TMDB()}, Released: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
}

func TestImportedWatchlistIsVisibleAndExportTracksRemoval(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	if _, err := db.Exec(`INSERT INTO auth_users(id,plex_id,created_at,updated_at) VALUES(1,99,0,0)`); err != nil {
		t.Fatal(err)
	}
	catalog := watchlistCatalog{}
	svc := New(db, catalog)
	live := watchlist.New(db, watchlist.Options{Catalog: catalog})
	plan, err := svc.PreviewCSV(ctx, 1, "trakt", "watchlist", strings.NewReader("tmdb_id\n11\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Import(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	items, err := live.Items(ctx, &core.User{ID: 1})
	if err != nil || len(items) != 1 || items[0].Title.Key != core.MovieKey(11) {
		t.Fatalf("import invisible: %+v, %v", items, err)
	}
	other, err := live.Items(ctx, &core.User{ID: 2})
	if err != nil || len(other) != 0 {
		t.Fatalf("wrong viewer items: %+v, %v", other, err)
	}
	if err = live.Remind(ctx, &core.User{ID: 1}, core.MovieKey(22)); err != nil {
		t.Fatal(err)
	}
	if err = live.Remove(ctx, 1, core.MovieKey(11)); err != nil {
		t.Fatal(err)
	}
	// Repeating a completed plan must not resurrect an item the viewer removed.
	if _, err = svc.Import(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	var csv strings.Builder
	if err = svc.ExportCSV(ctx, 1, "trakt", "watchlist", &csv); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(csv.String(), ",11,") || !strings.Contains(csv.String(), ",22,") {
		t.Fatalf("export is not the live watchlist: %s", csv.String())
	}
}

func TestWatchlistAndImportBookkeepingRollBackTogether(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	svc := New(db, watchlistCatalog{})
	plan, err := svc.savePlan(ctx, Plan{Source: "test", Items: []Item{{Viewer: 1, Key: core.MovieKey(11), Watchlist: true}}, Lists: []List{{Kind: "invalid"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Import(ctx, plan.ID); err == nil {
		t.Fatal("invalid list accepted")
	}
	for _, query := range []string{`SELECT COUNT(*) FROM watchlist_items`, `SELECT COUNT(*) FROM migrate_plans WHERE imported=1`} {
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("partial import persisted: %s", query)
		}
	}
}
