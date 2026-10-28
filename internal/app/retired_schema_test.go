package app

import (
	"bytes"
	"context"
	"path/filepath"
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

// The backup and cluster-move contract: a database holding invites rows
// (an install from before the removal) exports and restores into a fresh
// server, including SQLite into PostgreSQL when ETHER_TEST_POSTGRES is set.
func TestRetiredInvitesRowsSurviveLogicalRestore(t *testing.T) {
	ctx := context.Background()
	src, err := store.OpenPath(ctx, filepath.Join(t.TempDir(), "src.db")) // always SQLite
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := src.ExecContext(ctx, `INSERT INTO invites_links(token_hash,label,expires,max_uses,policy,sections) VALUES('h','Cousins',1,3,'{}','[1,2]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := src.ExecContext(ctx, `INSERT INTO invites_pins(handle_hash,invite_id,pin_id,expires) VALUES('p',1,'pin',1)`); err != nil {
		t.Fatal(err)
	}
	var dump bytes.Buffer
	if err := store.ExportLogical(ctx, src, &dump); err != nil {
		t.Fatal(err)
	}
	dst := store.OpenTest(t) // fresh: PostgreSQL when configured
	if err := store.ImportLogical(ctx, dst, &dump); err != nil {
		t.Fatalf("restore into a fresh server: %v", err)
	}
	var label string
	if err := dst.QueryRowContext(ctx, `SELECT label FROM invites_links WHERE token_hash='h'`).Scan(&label); err != nil || label != "Cousins" {
		t.Fatalf("restored invite %q: %v", label, err)
	}
}
