package ratings

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func rawSettings(t *testing.T, db *sql.DB) string {
	t.Helper()
	var raw string
	if err := db.QueryRow(`SELECT value FROM ratings_settings WHERE id=1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAPIKeySealedBoundToProvider(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "owner-ratings-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"Response":"True","Ratings":[{"Source":"Internet Movie Database","Value":"8/10"}]}`))
	}))
	defer srv.Close()
	// Settings written before sealing.
	legacy := New(db, Options{OMDbBaseURL: srv.URL})
	if err := legacy.SaveSettings(ctx, Settings{Enabled: true, Provider: "omdb", APIKey: "owner-ratings-key", Audience: "imdb", CacheHours: 1}); err != nil {
		t.Fatal(err)
	}
	sealer, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sealer.UpgradeCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	s := New(db, Options{OMDbBaseURL: srv.URL, Sealer: sealer})
	if n, err := s.MigrateSecrets(ctx); err != nil || n != 1 {
		t.Fatalf("migration: %d %v", n, err)
	}
	if raw := rawSettings(t, db); strings.Contains(raw, "owner-ratings-key") || !strings.Contains(raw, "ether-sealed:v2:") {
		t.Fatalf("stored settings: %s", raw)
	}
	if n, err := s.MigrateSecrets(ctx); err != nil || n != 0 {
		t.Fatalf("rerun: %d %v", n, err)
	}
	// The provider still authenticates with the opened key.
	if _, err := s.Refresh(ctx, &core.Title{Key: core.MovieKey(1), IDs: core.IDs{IMDB: "tt0000001"}}); err != nil {
		t.Fatalf("refresh with sealed key: %v", err)
	}
	// The placeholder is not carried to the other provider.
	if err := s.SaveSettings(ctx, Settings{Enabled: true, Provider: "mdblist", APIKey: Redacted, CacheHours: 1}); err == nil {
		t.Fatal("key carried to another provider")
	}
	if err := s.SaveSettings(ctx, Settings{Enabled: true, Provider: "omdb", APIKey: Redacted, Audience: "imdb", CacheHours: 2}); err != nil {
		t.Fatal(err)
	}
	// Repointing the stored provider by hand leaves the key unusable.
	raw := rawSettings(t, db)
	tampered := strings.Replace(raw, `"provider":"omdb"`, `"provider":"mdblist"`, 1)
	if _, err := db.Exec(`UPDATE ratings_settings SET value=? WHERE id=1`, tampered); err != nil {
		t.Fatal(err)
	}
	if _, err := s.settings(ctx); err == nil {
		t.Fatal("key opened for another provider")
	}
	if _, err := s.MigrateSecrets(ctx); err == nil || rawSettings(t, db) != tampered {
		t.Fatal("unreadable settings rewritten or hidden")
	}
}
