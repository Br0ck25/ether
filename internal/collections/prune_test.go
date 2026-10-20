package collections_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/collections"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestPruneDropsStaleLookupRows(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cat, err := catalog.New(db, nil, config.Catalog{Language: "en-US", Region: "US"}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	svc, err := collections.New(db, cat, slog.Default(), collections.WithoutDefaults(), collections.WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	day := int64(24 * 3600)
	n0 := now.Unix()
	for _, q := range []struct {
		sql string
		at  int64
	}{
		{`INSERT INTO collections_names(kind,query,id,name,at) VALUES('keyword','fresh',1,'x',?)`, n0 - 179*day},
		{`INSERT INTO collections_names(kind,query,id,name,at) VALUES('keyword','stale',2,'y',?)`, n0 - 181*day},
		{`INSERT INTO collections_external(source,ext,key,at) VALUES('imdb','tt1',1,?)`, n0 - 364*day},
		{`INSERT INTO collections_external(source,ext,key,at) VALUES('imdb','tt2',2,?)`, n0 - 366*day},
	} {
		if _, err := db.ExecContext(ctx, q.sql, q.at); err != nil {
			t.Fatal(err)
		}
	}
	n, err := svc.Prune(ctx)
	if err != nil || n != 2 {
		t.Fatalf("pruned %d, %v; want 2", n, err)
	}
	var names, ext int
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collections_names WHERE query='fresh'`).Scan(&names)
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collections_external WHERE ext='tt1'`).Scan(&ext)
	if names != 1 || ext != 1 {
		t.Fatalf("fresh rows lost: names=%d external=%d", names, ext)
	}
}
