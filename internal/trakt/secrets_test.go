package trakt

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
)

func sealerFor(t *testing.T, upgrade bool) *config.Store {
	t.Helper()
	s, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if upgrade {
		if err := s.UpgradeCredentials(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func rawTokens(t *testing.T, db *sql.DB, user int64) (string, string) {
	t.Helper()
	var a, r string
	if err := db.QueryRow(`SELECT access_token, refresh_token FROM trakt_tokens WHERE user_id=?`, user).Scan(&a, &r); err != nil {
		t.Fatal(err)
	}
	return a, r
}

func TestStoredTokensSealedAndMigrated(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// A row written by a release that predates sealing.
	if _, err := db.Exec(`INSERT INTO trakt_tokens(user_id, access_token, refresh_token, expires_at, username, updated_at) VALUES(1,'legacy-access','legacy-refresh',?, 'u1', 0)`, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	sealer := sealerFor(t, false)
	c := New(Options{Config: config.Trakt{Enabled: true, ClientID: "id", ClientSecret: "s"}, DB: db, Sealer: sealer, Logger: quietLog()})
	// Before the upgrade nothing is sealed, so older nodes keep working.
	if n, err := c.MigrateSecrets(ctx); err != nil || n != 0 {
		t.Fatalf("pre-upgrade migration: %d %v", n, err)
	}
	if a, _ := rawTokens(t, db, 1); a != "legacy-access" {
		t.Fatal("sealed before the upgrade")
	}
	if err := sealer.UpgradeCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := c.MigrateSecrets(ctx); err != nil || n != 1 {
		t.Fatalf("migration: %d %v", n, err)
	}
	a, r := rawTokens(t, db, 1)
	if !config.IsSealed(a) || !config.IsSealed(r) || strings.Contains(a+r, "legacy") {
		t.Fatalf("stored tokens not sealed: %q %q", a, r)
	}
	if n, err := c.MigrateSecrets(ctx); err != nil || n != 0 {
		t.Fatalf("second migration: %d %v", n, err)
	}
	tok, ok, err := c.Tokens(ctx, 1)
	if err != nil || !ok || tok.AccessToken != "legacy-access" || tok.RefreshToken != "legacy-refresh" || tok.Username != "u1" {
		t.Fatalf("tokens after migration: %+v %v", tok, err)
	}
	// New tokens are written sealed.
	if err := c.saveTokens(ctx, Tokens{UserID: 2, AccessToken: "fresh-access", RefreshToken: "fresh-refresh", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if a, r := rawTokens(t, db, 2); strings.Contains(a+r, "fresh") {
		t.Fatal("new tokens stored in plaintext")
	}
}

func TestStoredTokensBoundToUserAndKey(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sealer := sealerFor(t, true)
	c := New(Options{Config: config.Trakt{Enabled: true, ClientID: "id", ClientSecret: "s"}, DB: db, Sealer: sealer, Logger: quietLog()})
	for _, u := range []int64{1, 2} {
		if err := c.saveTokens(ctx, Tokens{UserID: u, AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	// User 1's sealed token copied into user 2's row does not open.
	a1, r1 := rawTokens(t, db, 1)
	if _, err := db.Exec(`UPDATE trakt_tokens SET access_token=?, refresh_token=? WHERE user_id=2`, a1, r1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Tokens(ctx, 2); err == nil {
		t.Fatal("token replayed into another user's row opened")
	}
	// The failure propagates and never disconnects the user or rewrites the row.
	if _, err := c.accessToken(ctx, 2); err == nil {
		t.Fatal("accessToken ignored an unreadable row")
	}
	if n, err := c.MigrateSecrets(ctx); err == nil || n != 0 {
		t.Fatalf("migration over an unreadable row: %d %v", n, err)
	}
	if a, r := rawTokens(t, db, 2); a != a1 || r != r1 {
		t.Fatal("unreadable row rewritten")
	}
	// Another credential key (a copied database) cannot open any row.
	other := New(Options{Config: config.Trakt{Enabled: true, ClientID: "id", ClientSecret: "s"}, DB: db, Sealer: sealerFor(t, true), Logger: quietLog()})
	if _, _, err := other.Tokens(ctx, 1); err == nil {
		t.Fatal("wrong key opened a token")
	}
	if _, err := other.MigrateSecrets(ctx); err == nil {
		t.Fatal("wrong key migration reported success")
	}
	if a, _ := rawTokens(t, db, 1); a != a1 {
		t.Fatal("wrong key rewrote a row")
	}
}
