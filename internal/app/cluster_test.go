package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/clusterconfig"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
)

// clusterSchemaURL is an empty schema on the ETHER_TEST_POSTGRES server,
// standing in for an owner's dedicated cluster database.
func clusterSchemaURL(t *testing.T) string {
	t.Helper()
	base := os.Getenv(store.TestPostgresEnv)
	if base == "" {
		t.Skip("set " + store.TestPostgresEnv + " to run cluster boot tests")
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
	u, _ := url.Parse(base)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func TestClusterBootSharesIdentity(t *testing.T) {
	ctx := context.Background()
	pgURL := clusterSchemaURL(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Setenv(config.DatabaseURLEnv, "")

	// First node: a fresh install pointed at the empty cluster database
	// seeds the shared settings.
	dirA := t.TempDir()
	cfgA, err := config.Open(dirA)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfgA.Update(func(c *config.Config) error {
		c.Transcode.FFmpegPath = "/not-installed/ffmpeg"
		c.Server.FriendlyName = "Cluster"
		c.SetupComplete = true
		c.Database = config.Database{Driver: config.DatabasePostgres, URL: pgURL}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a, err := New(ctx, Options{DataDir: dirA, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !a.clustered || !a.cfg.Clustered() || a.cfg.SharedVersion() == 0 {
		t.Fatal("first node did not attach shared settings")
	}
	tok, _, err := clusterconfig.NewToken(ctx, a.db, a.cfg, pgURL, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// Second node joins and boots with the same identity.
	dirB := t.TempDir()
	if _, err := clusterconfig.Join(ctx, clusterconfig.JoinOptions{DataDir: dirB, Token: tok.String()}); err != nil {
		t.Fatal(err)
	}
	cfgB, _ := config.Open(dirB)
	if err := cfgB.Update(func(c *config.Config) error { c.Transcode.FFmpegPath = "/not-installed/ffmpeg"; return nil }); err != nil {
		t.Fatal(err)
	}
	b, err := New(ctx, Options{DataDir: dirB, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.cfg.Get().Server.MachineIdentifier != a.cfg.Get().Server.MachineIdentifier || b.cfg.Get().Server.FriendlyName != "Cluster" {
		t.Fatal("second node has another identity")
	}
	if err := a.cfg.Update(func(c *config.Config) error { c.Server.FriendlyName = "Renamed"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := b.cfg.RefreshShared(ctx); err != nil {
		t.Fatal(err)
	}
	if b.cfg.Get().Server.FriendlyName != "Renamed" {
		t.Fatal("rename did not reach the second node")
	}

	// A node with its own credential key refuses to start.
	dirC := t.TempDir()
	cfgC, _ := config.Open(dirC)
	_ = cfgC.Update(func(c *config.Config) error {
		c.Transcode.FFmpegPath = "/not-installed/ffmpeg"
		c.Database = config.Database{Driver: config.DatabasePostgres, URL: pgURL}
		return nil
	})
	if c, err := New(ctx, Options{DataDir: dirC, Logger: quiet}); !errors.Is(err, config.ErrKeyMismatch) {
		if c != nil {
			c.Close()
		}
		t.Fatalf("mismatched key: %v", err)
	}
}
