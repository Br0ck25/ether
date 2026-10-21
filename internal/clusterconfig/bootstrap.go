package clusterconfig

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
)

func discard(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func isPostgresURL(u string) bool {
	return config.Database{URL: u}.IsPostgres()
}

// openPostgres opens (and migrates) the cluster database.
func openPostgres(ctx context.Context, url string, log *slog.Logger) (*sql.DB, error) {
	if !isPostgresURL(url) {
		return nil, errors.New("clusterconfig: the cluster database URL must start with postgres:// or postgresql://")
	}
	db, _, err := store.OpenWith(ctx, store.Options{Driver: store.Postgres.String(), URL: url, Logger: log})
	return db, err
}

// Attach switches cfg to the cluster's shared settings in db, seeding them
// from cfg when the database holds none (the first node of a cluster). It
// is the boot step between opening the database and building components;
// see config.Store.AttachShared for the options.
func Attach(ctx context.Context, cfg *config.Store, db *sql.DB, o config.SharedOptions) error {
	return cfg.AttachShared(ctx, NewShared(db), o)
}

// InitOptions configure Init.
type InitOptions struct {
	// DataDir is the standalone server's data directory (config.json,
	// .credentials-key and ether.db).
	DataDir string
	// DatabaseURL is the empty PostgreSQL database the cluster will use.
	DatabaseURL string
	Logger      *slog.Logger
}

// Init turns a stopped standalone server into the first node of a cluster:
// it copies every table of the local SQLite database into the empty
// PostgreSQL database (a logical export/import, as backups use), seeds the
// shared settings from config.json and points config.json at PostgreSQL.
// It refuses a database that already holds data.
func Init(ctx context.Context, o InitOptions) error {
	log := discard(o.Logger)
	cfgStore, err := config.Open(o.DataDir)
	if err != nil {
		return fmt.Errorf("open settings: %w", err)
	}
	if cfgStore.Get().Database.IsPostgres() {
		return errors.New("this server already uses PostgreSQL; add further nodes with `ether cluster join`")
	}
	if !isPostgresURL(o.DatabaseURL) {
		return errors.New("a postgres:// database URL is required (--database-url or ETHER_DATABASE_URL)")
	}
	pg, err := openPostgres(ctx, o.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer pg.Close()
	if v, err := NewShared(pg).SharedVersion(ctx); err != nil {
		return err
	} else if v != 0 {
		return errors.New("the PostgreSQL database already holds an Ether cluster; add this server with `ether cluster join <token>` instead")
	}
	if _, err := os.Stat(filepath.Join(o.DataDir, "ether.db")); err == nil {
		lite, err := store.Open(ctx, o.DataDir)
		if err != nil {
			return fmt.Errorf("open local database: %w", err)
		}
		err = copyDatabase(ctx, lite, pg)
		lite.Close()
		if err != nil {
			if errors.Is(err, store.ErrNotEmpty) {
				return fmt.Errorf("the PostgreSQL database is not empty (%w); create a new, empty database for the cluster", err)
			}
			return fmt.Errorf("copy local database to PostgreSQL: %w", err)
		}
		log.Info("cluster init: copied the local database to PostgreSQL")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := Attach(ctx, cfgStore, pg, config.SharedOptions{PollInterval: -1, Logger: log}); err != nil {
		return fmt.Errorf("seed shared settings: %w", err)
	}
	return cfgStore.Update(func(c *config.Config) error {
		c.Database = config.Database{Driver: config.DatabasePostgres, URL: o.DatabaseURL}
		return nil
	})
}

// copyDatabase streams a logical export of src into dst.
func copyDatabase(ctx context.Context, src, dst *sql.DB) error {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := store.ExportLogical(ctx, src, pw)
		pw.CloseWithError(err)
		done <- err
	}()
	err := store.ImportLogical(ctx, dst, pr)
	pr.CloseWithError(errors.New("import stopped"))
	exportErr := <-done
	if err != nil {
		return err
	}
	return exportErr
}

// JoinOptions configure Join.
type JoinOptions struct {
	// DataDir is the new node's data directory.
	DataDir string
	// Token is the join token string from an existing node.
	Token string
	// DatabaseURL overrides the URL in the token (e.g. the database is
	// reached under another name from this node).
	DatabaseURL string
	// Force moves an already configured server's settings aside.
	Force bool
	// NodeName is recorded as the token's user (default: host name).
	NodeName string
	Logger   *slog.Logger
	Now      func() time.Time
}

// JoinResult reports what Join did.
type JoinResult struct {
	// MovedAside lists files renamed out of the way.
	MovedAside []string
	// MachineIdentifier is the cluster's server identity.
	MachineIdentifier string
}

// Join makes DataDir a node of the cluster the token belongs to: it checks
// the token's credential key against the cluster, consumes the token, then
// installs the key and writes a config.json holding node-local defaults (or
// this server's existing node-local settings) and the database URL. The
// node then boots with the cluster's shared settings.
func Join(ctx context.Context, o JoinOptions) (JoinResult, error) {
	var res JoinResult
	log := discard(o.Logger)
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	tok, err := ParseJoinToken(o.Token)
	if err != nil {
		return res, err
	}
	url := tok.DatabaseURL
	if o.DatabaseURL != "" {
		url = o.DatabaseURL
	}
	if o.NodeName == "" {
		o.NodeName, _ = os.Hostname()
	}

	// Inspect what is already in the data directory.
	if err := os.MkdirAll(o.DataDir, 0o755); err != nil {
		return res, err
	}
	cfgPath := filepath.Join(o.DataDir, "config.json")
	keyPath := filepath.Join(o.DataDir, config.CredentialKeyFile)
	local := config.Defaults(o.DataDir)
	oldKey, keyErr := config.ReadCredentialKey(o.DataDir)
	sameKey := keyErr == nil && bytes.Equal(oldKey, tok.Key)
	_, cfgErr := os.Stat(cfgPath)
	if cfgErr == nil {
		existing, err := config.Open(o.DataDir)
		switch {
		case err != nil && !o.Force:
			return res, fmt.Errorf("this data directory holds settings that cannot be read (%v); use --force to move them aside", err)
		case err == nil:
			cur := existing.Get()
			if !sameKey && (cur.SetupComplete || cur.Plex.Token != "") && !o.Force {
				return res, errors.New("this data directory holds a configured server; joining replaces its settings and identity with the cluster's. Use --force to move them aside, or use a fresh data directory")
			}
			local = cur // keep this node's port, paths and other node-local settings
		}
	}

	db, err := openPostgres(ctx, url, log)
	if err != nil {
		return res, err
	}
	defer db.Close()
	doc, found, err := NewShared(db).LoadShared(ctx)
	if err != nil {
		return res, err
	}
	if !found {
		return res, errors.New("the database holds no Ether cluster yet; run `ether cluster init` on the first node")
	}
	if config.KeyFingerprint(tok.Key) != doc.KeyFingerprint {
		return res, fmt.Errorf("%w: its credential key does not match the cluster's", ErrTokenInvalid)
	}
	if err := ConsumeJoinToken(ctx, db, tok, o.NodeName, now()); err != nil {
		return res, err
	}

	// Install the cluster key, moving a different one (and the settings it
	// sealed) aside.
	if !sameKey {
		suffix := ".pre-join-" + strconv.FormatInt(now().Unix(), 10)
		for _, p := range []string{cfgPath, keyPath} {
			if _, err := os.Lstat(p); err == nil {
				if err := os.Rename(p, p+suffix); err != nil {
					return res, err
				}
				res.MovedAside = append(res.MovedAside, p+suffix)
			}
		}
		if err := config.InstallCredentialKey(o.DataDir, tok.Key); err != nil {
			return res, err
		}
	}
	cfgStore, err := config.Open(o.DataDir)
	if err != nil {
		return res, err
	}
	if err := cfgStore.Update(func(c *config.Config) error {
		merged, err := config.MergeShared(local, config.SharedJSON(*c))
		if err != nil {
			return err
		}
		merged.Database = config.Database{Driver: config.DatabasePostgres, URL: url}
		*c = merged
		return nil
	}); err != nil {
		return res, err
	}
	// Write the shared settings into the local mirror now, so config.json
	// already shows the cluster's identity.
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := cfgStore.AttachShared(actx, NewShared(db), config.SharedOptions{NoSeed: true, PollInterval: -1, Logger: log}); err != nil {
		return res, err
	}
	res.MachineIdentifier = cfgStore.Get().Server.MachineIdentifier
	return res, nil
}

// NewToken creates a join token for the cluster whose database is db,
// embedding databaseURL and cfg's credential key.
func NewToken(ctx context.Context, db *sql.DB, cfg *config.Store, databaseURL string, ttl time.Duration) (JoinToken, TokenInfo, error) {
	doc, found, err := NewShared(db).LoadShared(ctx)
	if err != nil {
		return JoinToken{}, TokenInfo{}, err
	}
	if !found {
		return JoinToken{}, TokenInfo{}, errors.New("clustering is not enabled: the database holds no shared settings")
	}
	if doc.KeyFingerprint != cfg.KeyFingerprint() {
		return JoinToken{}, TokenInfo{}, config.ErrKeyMismatch
	}
	return CreateJoinToken(ctx, db, databaseURL, cfg.CredentialKey(), ttl, time.Now())
}

// TokenWarning is shown with every new token.
const TokenWarning = "This token contains the cluster database password and the key that decrypts every stored credential. Keep it secret permanently: expiration, use and revocation only stop Ether enrollment; they do not invalidate the embedded credentials. If exposed, rotate the database password and any stored credentials the recipient could have read. Do not paste it into chats or tickets."
