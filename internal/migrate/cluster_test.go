package migrate

import (
	"context"
	"sync"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// OpenTest also runs this against an isolated PostgreSQL schema when
// ETHER_TEST_POSTGRES is provided. SQLite's BEGIN IMMEDIATE serializes writes;
// PostgreSQL needs the import transaction's shared advisory lock.
func TestConcurrentImportsKeepOneListPerSource(t *testing.T) {
	for _, samePlan := range []bool{true, false} {
		name := "separate_previews"
		if samePlan {
			name = "same_preview"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := store.OpenTest(t)
			nodes := []*Service{New(db, nil), New(db, nil)}
			plan := Plan{Source: "test-server", Lists: []List{{External: "10", Name: "Favourites", Kind: "playlist", Viewer: 1, Keys: []core.RatingKey{core.MovieKey(11)}}}}
			first, err := nodes[0].savePlan(ctx, plan)
			if err != nil {
				t.Fatal(err)
			}
			second := first
			if !samePlan {
				second, err = nodes[1].savePlan(ctx, plan)
				if err != nil {
					t.Fatal(err)
				}
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for i, id := range []string{first.ID, second.ID} {
				wg.Add(1)
				go func(i int, id string) {
					defer wg.Done()
					<-start
					_, err := nodes[i].Import(ctx, id)
					results <- err
				}(i, id)
			}
			close(start)
			wg.Wait()
			close(results)
			for err := range results {
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, table := range []string{"state_playlists", "state_playlist_items", "migrate_objects"} {
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Fatalf("%s has %d rows, want 1", table, count)
				}
			}
			var pending int
			if err := db.QueryRow(`SELECT COUNT(*) FROM migrate_plans WHERE imported=0`).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if pending != 0 {
				t.Fatalf("%d plans remain pending", pending)
			}
		})
	}
}
