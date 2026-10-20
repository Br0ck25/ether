package store

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// LockKey makes "read, then write" atomic across connections: concurrent
// increments through a read-modify-write never lose an update.
func TestLockKeySerialisesReadModifyWrite(t *testing.T) {
	ctx := context.Background()
	db := OpenTest(t)
	d := DialectOf(db)
	if _, err := db.ExecContext(ctx, `INSERT INTO storetest_links(id, label) VALUES(1, '0')`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var failed atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				failed.Add(1)
				return
			}
			defer tx.Rollback()
			if err := LockKey(ctx, d, tx, "storetest:counter"); err != nil {
				failed.Add(1)
				return
			}
			var label string
			if err := tx.QueryRowContext(ctx, `SELECT label FROM storetest_links WHERE id=1`).Scan(&label); err != nil {
				failed.Add(1)
				return
			}
			time.Sleep(5 * time.Millisecond) // widen the race window
			if _, err := tx.ExecContext(ctx, `UPDATE storetest_links SET label=? WHERE id=1`, label+"x"); err != nil {
				failed.Add(1)
				return
			}
			if err := tx.Commit(); err != nil {
				failed.Add(1)
			}
		}()
	}
	wg.Wait()
	if failed.Load() != 0 {
		t.Fatalf("%d transactions failed", failed.Load())
	}
	var label string
	if err := db.QueryRowContext(ctx, `SELECT label FROM storetest_links WHERE id=1`).Scan(&label); err != nil {
		t.Fatal(err)
	}
	if label != "0xxxxxxxx" {
		t.Fatalf("label = %q: lost updates", label)
	}
}

func TestLockKeyNilTxAndHashStable(t *testing.T) {
	if err := LockKey(context.Background(), Postgres, nil, "k"); err != nil {
		t.Fatal(err)
	}
	if lockHash("a") != lockHash("a") || lockHash("a") == lockHash("b") {
		t.Fatal("lock hash must be stable and key-specific")
	}
}

// SessionLock excludes a second holder of the same key until unlock, and
// is a no-op (nil, nil) on SQLite.
func TestSessionLockExcludesUntilUnlock(t *testing.T) {
	ctx := context.Background()
	db := OpenTest(t)
	unlock, err := SessionLock(ctx, db, "site:a")
	if err != nil {
		t.Fatal(err)
	}
	if DialectOf(db) != Postgres {
		if unlock != nil {
			t.Fatal("SQLite session lock should be nil")
		}
		return
	}
	acquired := make(chan func(), 1)
	go func() {
		u, err := SessionLock(ctx, db, "site:a")
		if err != nil {
			t.Error(err)
			return
		}
		acquired <- u
	}()
	select {
	case <-acquired:
		t.Fatal("second holder got the lock while the first held it")
	case <-time.After(300 * time.Millisecond):
	}
	// A different key is independent.
	other, err := SessionLock(ctx, db, "site:b")
	if err != nil {
		t.Fatal(err)
	}
	other()
	unlock()
	select {
	case u := <-acquired:
		u()
	case <-time.After(5 * time.Second):
		t.Fatal("second holder never got the lock after unlock")
	}
	// Waiting honours ctx.
	unlock, _ = SessionLock(ctx, db, "site:a")
	defer unlock()
	cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if _, err := SessionLock(cctx, db, "site:a"); err == nil {
		t.Fatal("waiting past the deadline should fail")
	}
}
