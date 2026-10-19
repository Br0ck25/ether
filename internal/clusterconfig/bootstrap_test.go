package clusterconfig

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
)

// pgSchemaURL creates an empty schema on the ETHER_TEST_POSTGRES server
// and returns a URL whose connections use it, as an owner's dedicated
// cluster database would be. The schema is dropped when the test ends.
func pgSchemaURL(t *testing.T) string {
	t.Helper()
	base := os.Getenv(store.TestPostgresEnv)
	if base == "" {
		t.Skip("set " + store.TestPostgresEnv + " to run PostgreSQL bootstrap tests")
	}
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	schema := "ether_test_" + strconv.FormatInt(time.Now().Unix(), 10) + "_" + hex.EncodeToString(rnd[:])
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`) })
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func openPG(t *testing.T, u string) *sql.DB {
	t.Helper()
	db, _, err := store.OpenWith(context.Background(), store.Options{Driver: "postgres", URL: u})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// standalone builds a configured standalone server's data directory with
// a SQLite database holding one row.
func standalone(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	s, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(c *config.Config) error {
		c.SetupComplete = true
		c.Plex.Token = "owner-token"
		c.Server.FriendlyName = "Original"
		c.Server.Port = 32499
		c.Indexers = []config.Indexer{{ID: "ix", APIKey: "ix-key"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO clusterconfig_secrets(name, version, value, updated_at) VALUES('marker', 1, 'x', 1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	return dir
}

func TestInitAndJoinPostgres(t *testing.T) {
	ctx := context.Background()
	pgURL := pgSchemaURL(t)
	dirA := standalone(t)

	if err := Init(ctx, InitOptions{DataDir: dirA, DatabaseURL: "sqlite://nope"}); err == nil {
		t.Fatal("init accepted a non-postgres URL")
	}
	if err := Init(ctx, InitOptions{DataDir: dirA, DatabaseURL: pgURL}); err != nil {
		t.Fatal(err)
	}
	pg := openPG(t, pgURL)
	var marker int
	if err := pg.QueryRow(`SELECT COUNT(*) FROM clusterconfig_secrets WHERE name='marker'`).Scan(&marker); err != nil || marker != 1 {
		t.Fatalf("local data not copied: %d %v", marker, err)
	}
	cfgA, err := config.Open(dirA)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfgA.Get().Database.Effective(nil); got.Driver != config.DatabasePostgres || got.URL != pgURL {
		t.Fatalf("config not pointed at PostgreSQL: %+v", got.Redacted())
	}
	raw, _ := os.ReadFile(filepath.Join(dirA, "config.json"))
	if strings.Contains(string(raw), "ether:ether@") {
		t.Fatal("database URL stored unsealed")
	}
	if err := Init(ctx, InitOptions{DataDir: dirA, DatabaseURL: pgURL}); err == nil || !strings.Contains(err.Error(), "already uses PostgreSQL") {
		t.Fatalf("re-init: %v", err)
	}
	if err := Init(ctx, InitOptions{DataDir: standalone(t), DatabaseURL: pgURL}); err == nil || !strings.Contains(err.Error(), "already holds an Ether cluster") {
		t.Fatalf("init into a used database: %v", err)
	}

	// Boot node A the way the app does and mint a token.
	if err := Attach(ctx, cfgA, pg, config.SharedOptions{PollInterval: -1}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cfgA.Close)
	tok, _, err := NewToken(ctx, pg, cfgA, pgURL, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// Join a fresh node.
	dirB := t.TempDir()
	res, err := Join(ctx, JoinOptions{DataDir: dirB, Token: tok.String(), NodeName: "node-b"})
	if err != nil {
		t.Fatal(err)
	}
	if res.MachineIdentifier != cfgA.Get().Server.MachineIdentifier {
		t.Fatal("joined node has another identity")
	}
	keyA, _ := config.ReadCredentialKey(dirA)
	keyB, _ := config.ReadCredentialKey(dirB)
	if string(keyA) != string(keyB) {
		t.Fatal("credential key not installed")
	}
	cfgB, err := config.Open(dirB)
	if err != nil {
		t.Fatal(err)
	}
	if cfgB.Get().Server.Port != 32400 || cfgB.Get().Cache.Dir != filepath.Join(dirB, "cache") {
		t.Fatalf("node-local defaults not written: %+v", cfgB.Get().Server)
	}
	if err := Attach(ctx, cfgB, openPG(t, cfgB.Get().Database.URL), config.SharedOptions{PollInterval: -1}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cfgB.Close)
	if cfgB.Get().Plex.Token != "owner-token" || cfgB.Get().Server.FriendlyName != "Original" || cfgB.Get().Indexers[0].APIKey != "ix-key" {
		t.Fatal("joined node does not see the shared settings")
	}
	list, _ := ListJoinTokens(ctx, pg)
	if len(list) != 1 || list[0].UsedBy != "node-b" {
		t.Fatalf("token not consumed: %+v", list)
	}

	// The token cannot be used twice.
	if _, err := Join(ctx, JoinOptions{DataDir: t.TempDir(), Token: tok.String()}); !errors.Is(err, ErrTokenUsed) {
		t.Fatalf("replay: %v", err)
	}
	// A configured standalone server is not overwritten without --force,
	// and its local settings survive a forced join.
	tok2, _, err := NewToken(ctx, pg, cfgA, pgURL, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dirC := standalone(t)
	if _, err := Join(ctx, JoinOptions{DataDir: dirC, Token: tok2.String()}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("join over a configured server: %v", err)
	}
	res, err = Join(ctx, JoinOptions{DataDir: dirC, Token: tok2.String(), Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.MovedAside) != 2 {
		t.Fatalf("moved aside %v", res.MovedAside)
	}
	cfgC, _ := config.Open(dirC)
	if cfgC.Get().Server.Port != 32499 || cfgC.Get().Server.MachineIdentifier != cfgA.Get().Server.MachineIdentifier {
		t.Fatalf("forced join: port %d", cfgC.Get().Server.Port)
	}
	// A token sealed with another key is refused before it is consumed.
	stranger, _ := config.Open(t.TempDir())
	forged, _, err := CreateJoinToken(ctx, pg, pgURL, stranger.CredentialKey(), time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Join(ctx, JoinOptions{DataDir: t.TempDir(), Token: forged.String()}); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("forged key: %v", err)
	}
}

func TestInitRefusesNonEmptyPostgres(t *testing.T) {
	ctx := context.Background()
	pgURL := pgSchemaURL(t)
	pg := openPG(t, pgURL)
	if _, err := pg.Exec(`INSERT INTO clusterconfig_join_tokens(id, secret_hash, created_at, expires_at) VALUES('x', 'h', 1, 2)`); err != nil {
		t.Fatal(err)
	}
	dir := standalone(t)
	err := Init(ctx, InitOptions{DataDir: dir, DatabaseURL: pgURL})
	if !errors.Is(err, store.ErrNotEmpty) {
		t.Fatalf("err = %v", err)
	}
	cfg, _ := config.Open(dir)
	if cfg.Get().Database.IsPostgres() {
		t.Fatal("failed init changed the local config")
	}
}
