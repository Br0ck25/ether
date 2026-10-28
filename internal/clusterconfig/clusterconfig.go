// Package clusterconfig keeps the settings and identity material that the
// nodes of an Ether cluster share in the owner's PostgreSQL database:
//
//   - the shared settings document (Shared, a config.SharedBackend),
//   - sealed identity files such as the plex.direct certificate (Secrets),
//   - one-time join tokens (jointoken.go),
//
// and the bootstrap steps that turn a standalone server into the first
// cluster node (Init) and add further nodes (Join). Everything stored is
// sealed with the cluster's credential key, which travels only inside join
// tokens and never sits in the database in the clear.
//
// The tables exist on SQLite too (migrations are dialect-neutral) but a
// standalone server never uses them.
package clusterconfig

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	store.Register("clusterconfig", 1, `
CREATE TABLE clusterconfig_shared(
	id INTEGER PRIMARY KEY CHECK(id=1),
	version INTEGER NOT NULL,
	doc TEXT NOT NULL,
	key_fingerprint TEXT NOT NULL,
	updated_at INTEGER NOT NULL);
CREATE TABLE clusterconfig_secrets(
	name TEXT PRIMARY KEY,
	version INTEGER NOT NULL,
	value TEXT NOT NULL,
	updated_at INTEGER NOT NULL);
CREATE TABLE clusterconfig_join_tokens(
	id TEXT PRIMARY KEY,
	secret_hash TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	used_at INTEGER NOT NULL DEFAULT 0,
	used_by TEXT NOT NULL DEFAULT '');`)
}

// Shared stores the shared settings document in clusterconfig_shared (one
// row, versioned for optimistic concurrency).
type Shared struct {
	db  *sql.DB
	now func() time.Time
}

// NewShared returns the database-backed config.SharedBackend.
func NewShared(db *sql.DB) *Shared { return &Shared{db: db, now: time.Now} }

var _ config.SharedBackend = (*Shared)(nil)

// LoadShared implements config.SharedBackend.
func (s *Shared) LoadShared(ctx context.Context) (config.SharedDoc, bool, error) {
	var d config.SharedDoc
	err := s.db.QueryRowContext(ctx, `SELECT version, doc, key_fingerprint FROM clusterconfig_shared WHERE id=1`).Scan(&d.Version, &d.Doc, &d.KeyFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return config.SharedDoc{}, false, nil
	}
	return d, err == nil, err
}

// SaveShared implements config.SharedBackend.
func (s *Shared) SaveShared(ctx context.Context, d config.SharedDoc, expect int64) (int64, error) {
	now := s.now().Unix()
	var res sql.Result
	var err error
	if expect == 0 {
		res, err = s.db.ExecContext(ctx, `INSERT INTO clusterconfig_shared(id, version, doc, key_fingerprint, updated_at) VALUES(1, 1, ?, ?, ?) ON CONFLICT(id) DO NOTHING`, d.Doc, d.KeyFingerprint, now)
	} else {
		res, err = s.db.ExecContext(ctx, `UPDATE clusterconfig_shared SET version=version+1, doc=?, key_fingerprint=?, updated_at=? WHERE id=1 AND version=?`, d.Doc, d.KeyFingerprint, now, expect)
	}
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, config.ErrSharedConflict
	}
	return expect + 1, nil
}

// SharedVersion implements config.SharedBackend.
func (s *Shared) SharedVersion(ctx context.Context) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT version FROM clusterconfig_shared WHERE id=1`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v, err
}

// Sealer encrypts values with the cluster credential key (*config.Store).
type Sealer interface {
	Seal(name string, plain []byte) (string, error)
	Unseal(name, sealed string) ([]byte, error)
}

// Secrets stores named, sealed blobs (identity files) shared by all nodes.
// Each name has a version that increases on every write.
type Secrets struct {
	db     *sql.DB
	sealer Sealer
	now    func() time.Time
}

// NewSecrets returns a blob store sealing values with sealer.
func NewSecrets(db *sql.DB, sealer Sealer) *Secrets {
	return &Secrets{db: db, sealer: sealer, now: time.Now}
}

// Get returns the blob stored under name and its version; version 0 means
// nothing is stored.
func (s *Secrets) Get(ctx context.Context, name string) ([]byte, int64, error) {
	var v int64
	var sealed string
	err := s.db.QueryRowContext(ctx, `SELECT version, value FROM clusterconfig_secrets WHERE name=?`, name).Scan(&v, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	plain, err := s.sealer.Unseal(name, sealed)
	if err != nil {
		return nil, 0, err
	}
	return plain, v, nil
}

// Put stores data under name and returns the new version.
func (s *Secrets) Put(ctx context.Context, name string, data []byte) (int64, error) {
	sealed, err := s.sealer.Seal(name, data)
	if err != nil {
		return 0, err
	}
	var v int64
	err = s.db.QueryRowContext(ctx, `INSERT INTO clusterconfig_secrets(name, version, value, updated_at) VALUES(?, 1, ?, ?)
		ON CONFLICT(name) DO UPDATE SET version=clusterconfig_secrets.version+1, value=excluded.value, updated_at=excluded.updated_at
		RETURNING version`, name, sealed, s.now().Unix()).Scan(&v)
	return v, err
}

// Version is the stored version of name (0 when absent).
func (s *Secrets) Version(ctx context.Context, name string) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT version FROM clusterconfig_secrets WHERE name=?`, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v, err
}
