package app

import (
	"context"
	"testing"

	"github.com/braedonsaunders/ether/internal/store"
)

// Databases upgraded from a release with invitations keep that schema; a
// fresh server must match it so their logical backups restore.
func TestRetiredInvitesSchemaStaysRegistered(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	have, err := store.AppliedMigrations(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if have["invites"] != 2 {
		t.Fatalf("invites schema version %d, want 2", have["invites"])
	}
	for _, table := range []string{"invites_links", "invites_claims", "invites_pins"} {
		if _, err := db.ExecContext(ctx, `SELECT COUNT(*) FROM `+table); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
	}
}
