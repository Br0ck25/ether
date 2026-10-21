package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/cluster"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/notify"
	"github.com/braedonsaunders/ether/internal/ratings"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/trakt"
	"github.com/braedonsaunders/ether/internal/updater"
)

func credentialApp(t *testing.T) (*App, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Update(func(c *config.Config) error { c.Plex.Token = "plex-token-value"; return nil }); err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, db: db, log: quietLog(),
		cluster: cluster.NewSolo(cluster.Local{Identity: cluster.Identity{NodeID: "self", Name: "self"}}, nil)}
	a.trakt = trakt.New(trakt.Options{DB: db, Sealer: cfg, Logger: quietLog()})
	a.notifications = notify.New(db, notify.Options{Sealer: cfg, Logger: quietLog()})
	a.ratings = ratings.New(db, ratings.Options{Sealer: cfg})
	// Rows as a release before sealing wrote them.
	if _, err := db.Exec(`INSERT INTO trakt_tokens(user_id, access_token, refresh_token, expires_at, username, updated_at) VALUES(1,'legacy-access','legacy-refresh',0,'u',0)`); err != nil {
		t.Fatal(err)
	}
	if err := a.ratings.SaveSettings(ctx, ratings.Settings{Provider: "omdb", APIKey: "legacy-ratings-key", CacheHours: 1}); err != nil {
		t.Fatal(err)
	}
	return a, db
}

func traktRow(t *testing.T, db *sql.DB, user int64) string {
	t.Helper()
	var access string
	if err := db.QueryRow(`SELECT access_token FROM trakt_tokens WHERE user_id=?`, user).Scan(&access); err != nil {
		t.Fatal(err)
	}
	return access
}

func hookChannel(id string) notify.Channel {
	return notify.Channel{ID: id, Name: id, Kind: "webhook", Enabled: true, Events: []string{"test"}, Endpoint: "https://hooks.test/hook-secret-path"}
}

func channelRow(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var raw string
	if err := db.QueryRow(`SELECT config FROM notify_channels WHERE id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func configBytes(t *testing.T, a *App) string {
	t.Helper()
	b, err := os.ReadFile(a.cfg.Path())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCredentialUpgradeWaitsForUpdaterAndCluster(t *testing.T) {
	ctx := context.Background()
	a, db := credentialApp(t)
	updates := func(confirming bool) *updateState {
		boot := updater.StartupResult{}
		if confirming {
			boot.Confirming = &updater.Pending{From: "1.0.0", To: "1.1.0"}
		}
		return &updateState{mgr: updater.NewManager(updater.ManagerOptions{Config: func() config.Updates { return config.Updates{} }, Boot: boot})}
	}
	assertDeferred := func(why string) {
		t.Helper()
		var waiting string
		if d := a.credentialPass(ctx, &waiting); d != credentialCheckEvery || !strings.Contains(waiting, why) {
			t.Fatalf("pass ran: delay %v, reason %q", d, waiting)
		}
		if a.cfg.CredentialsUpgraded() || strings.Contains(configBytes(t, a), "ether-sealed:v2:") {
			t.Fatal("config.json upgraded while an older release may read it")
		}
		if traktRow(t, db, 1) != "legacy-access" {
			t.Fatal("database sealed while an older release may read it")
		}
		// New rows stay readable by older releases too.
		if err := a.notifications.Save(ctx, hookChannel("before")); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(channelRow(t, db, "before"), "hook-secret-path") {
			t.Fatal("new row sealed before the upgrade")
		}
	}

	// A self-update still confirming may roll back to the previous binary.
	a.updates = updates(true)
	assertDeferred("not confirmed")

	// A rolling update: a live node without the credentials migration.
	a.updates = updates(false)
	fm := &fakeMembers{Solo: cluster.NewSolo(cluster.Local{Identity: cluster.Identity{NodeID: "self", Name: "self"}}, nil)}
	fm.nodes = []cluster.Node{
		{ID: "self", Name: "self", Version: "1.1.0", Live: true, Migrations: map[string]int{credentialsMigration: 1}},
		{ID: "old", Name: "old", Version: "1.0.0", Live: true, Migrations: map[string]int{"trakt": 3}},
		{ID: "gone", Name: "gone", Version: "0.9.0", Live: false},
	}
	a.cluster = fm
	assertDeferred("old (1.0.0)")

	// The older node updated: the upgrade runs.
	fm.nodes[1].Migrations = map[string]int{credentialsMigration: 1}
	var waiting string
	if d := a.credentialPass(ctx, &waiting); d != credentialPassEvery {
		t.Fatalf("pass deferred: %q", waiting)
	}
	raw := configBytes(t, a)
	if !a.cfg.CredentialsUpgraded() || !strings.Contains(raw, "ether-sealed:v2:") || strings.Contains(raw, "plex-token-value") {
		t.Fatal("config.json not upgraded")
	}
	if !config.IsSealed(traktRow(t, db, 1)) {
		t.Fatal("trakt row not sealed")
	}
	if strings.Contains(channelRow(t, db, "before"), "hook-secret-path") {
		t.Fatal("row written during the deferral not sealed")
	}
	var ratingsRaw string
	_ = db.QueryRow(`SELECT value FROM ratings_settings WHERE id=1`).Scan(&ratingsRaw)
	if strings.Contains(ratingsRaw, "legacy-ratings-key") {
		t.Fatal("ratings key not sealed")
	}
	var format int
	if err := db.QueryRow(`SELECT version FROM credentials_format WHERE id=1`).Scan(&format); err != nil || format != 2 {
		t.Fatalf("credentials_format: %d %v", format, err)
	}
	// After the upgrade every new row is sealed.
	if err := a.notifications.Save(ctx, hookChannel("after")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(channelRow(t, db, "after"), "hook-secret-path") {
		t.Fatal("new row stored in plaintext after the upgrade")
	}
	// Rerunning changes nothing.
	if n, err := a.migrateStoredSecrets(ctx); err != nil || n != 0 {
		t.Fatalf("rerun: %d %v", n, err)
	}
}

func TestCredentialPassSurfacesUnreadableRows(t *testing.T) {
	ctx := context.Background()
	a, db := credentialApp(t)
	// A row sealed under another key (a database copied between installs).
	other, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := other.UpgradeCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	foreign, _ := other.SealField("foreign-access", "trakt_tokens", "access_token", "9")
	if _, err := db.Exec(`INSERT INTO trakt_tokens(user_id, access_token, refresh_token, expires_at, username, updated_at) VALUES(9,?, '', 0, 'u9', 0)`, foreign); err != nil {
		t.Fatal(err)
	}
	if err := a.cfg.UpgradeCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := a.migrateStoredSecrets(ctx)
	if err == nil || !strings.Contains(err.Error(), "trakt") {
		t.Fatalf("unreadable row hidden: %d %v", n, err)
	}
	if strings.Contains(err.Error(), "foreign-access") {
		t.Fatal("error leaks a secret")
	}
	if traktRow(t, db, 9) != foreign || !config.IsSealed(traktRow(t, db, 1)) {
		t.Fatal("readable rows not sealed or unreadable row rewritten")
	}
}

func TestCheckCredentialKeyDetectsLostKey(t *testing.T) {
	ctx := context.Background()
	a, db := credentialApp(t)
	if !a.cfg.KeyCreated() {
		t.Fatal("fresh data directory should report a created key")
	}
	if err := a.checkCredentialKey(ctx); err != nil {
		t.Fatalf("plaintext rows with a new key: %v", err)
	}
	if _, err := db.Exec(`UPDATE trakt_tokens SET access_token='ether-sealed:v2:AAAA' WHERE user_id=1`); err != nil {
		t.Fatal(err)
	}
	if err := a.checkCredentialKey(ctx); err == nil {
		t.Fatal("database sealed with another key accepted with a new key")
	}
	// The same data directory reopened: its key was not just created.
	reopened, err := config.Open(filepath.Dir(a.cfg.Path()))
	if err != nil {
		t.Fatal(err)
	}
	a.cfg = reopened
	if err := a.checkCredentialKey(ctx); err != nil {
		t.Fatalf("existing key: %v", err)
	}
}
