package clusterconfig

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
)

// SetupRequest carries secrets only in memory until the stopped server applies it.
type SetupRequest struct {
	Mode        string `json:"mode"`
	DatabaseURL string `json:"databaseUrl"`
	Token       string `json:"token"`
	Replace     bool   `json:"replace"`
}

func (s SetupRequest) URL() string {
	if s.DatabaseURL != "" {
		return s.DatabaseURL
	}
	t, _ := ParseJoinToken(s.Token)
	return t.DatabaseURL
}

func (s SetupRequest) Validate() error {
	if s.Mode != "create" && s.Mode != "join" {
		return errors.New("choose create or join")
	}
	if s.Mode == "join" {
		if _, err := ParseJoinToken(s.Token); err != nil {
			return err
		}
	}
	if !isPostgresURL(s.URL()) {
		return errors.New("enter a PostgreSQL connection URL")
	}
	return nil
}

// CheckSetup checks connectivity and eligibility without migrations, writes or
// consuming a join token. Apply repeats the authoritative checks after shutdown.
func CheckSetup(ctx context.Context, s SetupRequest) error {
	if err := s.Validate(); err != nil {
		return err
	}
	db, err := sql.Open(store.PostgresDriverName, s.URL())
	if err != nil {
		return errors.New("invalid PostgreSQL connection details")
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return errors.New("could not connect to PostgreSQL; check the address, credentials, TLS settings and network access")
	}
	var version string
	if err := db.QueryRowContext(ctx, `SHOW server_version_num`).Scan(&version); err != nil {
		return errors.New("could not read the PostgreSQL version")
	}
	v, _ := strconv.Atoi(version)
	if v < 140000 {
		return errors.New("PostgreSQL 14 or newer is required")
	}
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_type='BASE TABLE'`).Scan(&tables); err != nil {
		return errors.New("could not inspect the database schema")
	}
	if s.Mode == "create" {
		if tables != 0 {
			return errors.New("the database schema is not empty; choose an empty database or join its existing cluster")
		}
		return nil
	}
	if tables == 0 {
		return errors.New("this database has no Ether cluster; create the cluster on the first server")
	}
	tok, _ := ParseJoinToken(s.Token)
	return checkJoin(ctx, db, tok, time.Now())
}

func checkJoin(ctx context.Context, db *sql.DB, tok JoinToken, now time.Time) error {
	doc, found, err := NewShared(db).LoadShared(ctx)
	if err != nil || !found {
		return errors.New("could not read the cluster settings from this database")
	}
	if config.KeyFingerprint(tok.Key) != doc.KeyFingerprint {
		return ErrTokenInvalid
	}
	var hash string
	var expires, used int64
	if err := db.QueryRowContext(ctx, `SELECT secret_hash, expires_at, used_at FROM clusterconfig_join_tokens WHERE id=?`, tok.ID).Scan(&hash, &expires, &used); err != nil {
		return ErrTokenInvalid
	}
	if subtle.ConstantTimeCompare([]byte(hash), []byte(secretHash(tok.ID, tok.Secret))) != 1 {
		return ErrTokenInvalid
	}
	if used != 0 {
		return ErrTokenUsed
	}
	if now.Unix() >= expires {
		return ErrTokenExpired
	}
	return nil
}

// SetupError never includes connection strings or join-token material.
func SetupError(err error) string {
	if err == nil {
		return ""
	}
	for _, known := range []error{ErrTokenInvalid, ErrTokenUsed, ErrTokenExpired, store.ErrNotEmpty, config.ErrKeyMismatch} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	if strings.Contains(err.Error(), "already holds an Ether cluster") {
		return "This database already contains a cluster. Join it using a new token."
	}
	return "Cluster setup could not finish. The previous local settings were restored. Check the database connection and permissions, then try again with an empty database or a new join token."
}
