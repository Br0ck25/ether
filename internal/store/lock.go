package store

import (
	"context"
	"database/sql"
	"hash/fnv"
)

// LockKey serialises transactions that use the same key until tx ends: a
// check-then-insert ("count a user's streams, then reserve one") done
// inside the lock is atomic across every node sharing the database. d is
// the database's dialect (DialectOf; *sql.Tx does not expose its driver).
//
// On PostgreSQL it takes pg_advisory_xact_lock on a 64-bit hash of key
// (the single-key space, which never overlaps the cluster's two-key leader
// lock); the lock is released at commit or rollback. On SQLite it is a
// no-op: Ether opens SQLite with _txlock=immediate, so every transaction
// already holds the database's write lock from BEGIN.
func LockKey(ctx context.Context, d Dialect, tx *sql.Tx, key string) error {
	if tx == nil || d != Postgres {
		return nil
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(CAST(? AS BIGINT))`, lockHash(key))
	return err
}

// lockHash maps key to a signed 64-bit advisory lock id.
func lockHash(key string) int64 {
	h := fnv.New64a()
	h.Write([]byte("ether:" + key))
	return int64(h.Sum64())
}
