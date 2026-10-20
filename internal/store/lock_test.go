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
