package ratings

import (
	"context"
	"github.com/braedonsaunders/ether/internal/store"
	"testing"
)

func TestSettingsSnapshotIsRequestScoped(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db, Options{})
	snap := s.WithSettingsSnapshot(ctx)
	// Updating the shared database must affect the next request, not the
	// render already using the snapshot. This also exercises default settings.
	_, err = db.ExecContext(ctx, `INSERT INTO ratings_settings(id,value) VALUES(1,'{"enabled":true}')`)
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.settings(snap)
	if err != nil || old.Enabled {
		t.Fatalf("snapshot changed: %+v %v", old, err)
	}
	fresh, err := s.settings(s.WithSettingsSnapshot(ctx))
	if err != nil || !fresh.Enabled {
		t.Fatalf("new request stale: %+v %v", fresh, err)
	}
	// A closed database proves repeated access to the same snapshot performs
	// no database query; the ordinary path must still surface the failure.
	db.Close()
	if _, err := s.settings(s.WithSettingsSnapshot(snap)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.settings(ctx); err == nil {
		t.Fatal("ordinary request hid database failure")
	}
	other := New(db, Options{})
	if _, err := other.settings(snap); err == nil {
		t.Fatal("snapshot leaked to another service")
	}
}
