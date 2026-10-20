package stats

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

func TestPruneKeepsTwoYearsOfDailyCountersAndAllPlays(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	svc := New(db, Options{Now: func() time.Time { return now }})
	for _, day := range []string{"2026-10-19", "2024-10-20", "2024-10-18", "2020-01-01"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO stats_indexer_days(indexer,day) VALUES('i',?)`, day); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO stats_provider_days(provider,day,bytes,articles,misses,errors) VALUES('p',?,1,1,0,0)`, day); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO stats_plays(id,user_id,rating_key,username,title,platform,device,decision,started,updated) VALUES('old',1,1,'u','t','p','d','direct',0,0)`); err != nil {
		t.Fatal(err)
	}
	n, err := svc.Prune(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("pruned %d, want 4", n)
	}
	for _, table := range []string{"stats_indexer_days", "stats_provider_days"} {
		var c int
		db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&c)
		if c != 2 {
			t.Fatalf("%s rows = %d, want 2", table, c)
		}
	}
	var plays int
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stats_plays`).Scan(&plays)
	if plays != 1 {
		t.Fatal("plays must never be pruned")
	}
}
