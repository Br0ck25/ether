package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"hash/fnv"
	"time"
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

// SessionLock holds the lock for key across every node sharing a
// PostgreSQL database until unlock is called, for work that spans more
// than one transaction (a site login, for example). It uses the same key
// space as LockKey on a connection reserved for the lock; if releasing
// fails, that connection is discarded, which drops the lock with the
// session. Waiting honours ctx. On SQLite it returns nil, nil: there is
// one node, so callers keep their in-process lock.
func SessionLock(ctx context.Context, db *sql.DB, key string) (unlock func(), err error) {
	if db == nil || DialectOf(db) != Postgres {
		return nil, nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	id := lockHash(key)
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(CAST(? AS BIGINT))`, id); err != nil {
		conn.Close()
		return nil, err
	}
	return func() {
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(uctx, `SELECT pg_advisory_unlock(CAST(? AS BIGINT))`, id); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		conn.Close()
	}, nil
}
