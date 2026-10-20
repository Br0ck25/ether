package migrate

import (
	"bytes"
	"context"
	"testing"

	"github.com/braedonsaunders/ether/internal/store"
)

func TestAppliedMigrationSchemaRestoresIntoFreshDatabase(t *testing.T) {
	ctx := context.Background()
	src, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	// A database that already applied migrate v1 has this table, even
	// though live watchlists now handle all imports directly.
	if _, err := src.Exec(`CREATE TABLE IF NOT EXISTS migrate_watchlist(viewer INTEGER NOT NULL,key INTEGER NOT NULL,PRIMARY KEY(viewer,key))`); err != nil {
		t.Fatal(err)
	}
	var dump bytes.Buffer
	if err := store.ExportLogical(ctx, src, &dump); err != nil {
		t.Fatal(err)
	}
	// OpenTest covers both a fresh SQLite restore and cluster conversion
	// into PostgreSQL when ETHER_TEST_POSTGRES is set.
	dst := store.OpenTest(t)
	if err := store.ImportLogical(ctx, dst, &dump); err != nil {
		t.Fatal(err)
	}
}
