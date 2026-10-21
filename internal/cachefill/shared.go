package cachefill

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	store.Register("cachefill", 1, `CREATE TABLE cachefill_shared(id INTEGER PRIMARY KEY CHECK(id=1), doc TEXT NOT NULL DEFAULT '{}', status TEXT NOT NULL DEFAULT '{}', paused INTEGER NOT NULL DEFAULT 0, epoch INTEGER NOT NULL DEFAULT 0, owner TEXT NOT NULL DEFAULT '', expires INTEGER NOT NULL DEFAULT 0, next_run INTEGER NOT NULL DEFAULT 0);`)
}

// SharedStore keeps the shelf in the server database and fences every manifest/pin
// mutation with a renewable epoch. A former worker cannot unpin a new worker's
// shelf after losing its lease. The NAS holds media bytes only.
type SharedStore struct {
	db    *sql.DB
	node  string
	epoch atomic.Int64
}

func NewSharedStore(db *sql.DB, node string) *SharedStore { return &SharedStore{db: db, node: node} }

var errLease = errors.New("another cluster node coordinates shared preloading")

func (s *SharedStore) now(tx *sql.Tx, ctx context.Context) (int64, error) {
	q := `SELECT CAST(strftime('%s','now') AS INTEGER)`
	if store.DialectOf(s.db) == store.Postgres {
		q = `SELECT CAST(EXTRACT(EPOCH FROM clock_timestamp()) AS BIGINT)`
	}
	var n int64
	err := tx.QueryRowContext(ctx, q).Scan(&n)
	return n, err
}
func (s *SharedStore) tx(ctx context.Context, fn func(*sql.Tx, int64) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = store.LockKey(ctx, store.DialectOf(s.db), tx, "cachefill:shared"); err != nil {
		return err
	}
	now, err := s.now(tx, ctx)
	if err != nil {
		return err
	}
	if err = fn(tx, now); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SharedStore) acquire(ctx context.Context) error {
	var epoch int64
	err := s.tx(ctx, func(tx *sql.Tx, now int64) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO cachefill_shared(id) VALUES(1) ON CONFLICT(id) DO NOTHING`); err != nil {
			return err
		}
		var expires, next int64
		var owner string
		if err := tx.QueryRowContext(ctx, `SELECT epoch,owner,expires,next_run FROM cachefill_shared WHERE id=1`).Scan(&epoch, &owner, &expires, &next); err != nil {
			return err
		}
		if (owner != "" && expires > now) || (owner == "" && next > now) {
			return errLease
		}
		epoch++
		_, err := tx.ExecContext(ctx, `UPDATE cachefill_shared SET epoch=?,owner=?,expires=? WHERE id=1`, epoch, s.node, now+60)
		return err
	})
	if err == nil {
		s.epoch.Store(epoch)
	}
	return err
}
func (s *SharedStore) guard(ctx context.Context, tx *sql.Tx, now int64) error {
	var epoch, expires int64
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT epoch,owner,expires FROM cachefill_shared WHERE id=1`).Scan(&epoch, &owner, &expires); err != nil {
		return err
	}
	if epoch != s.epoch.Load() || owner != s.node || expires <= now {
		return errLease
	}
	return nil
}
func (s *SharedStore) renew(ctx context.Context) error {
	return s.tx(ctx, func(tx *sql.Tx, now int64) error {
		if err := s.guard(ctx, tx, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE cachefill_shared SET expires=? WHERE id=1`, now+60)
		return err
	})
}
func (s *SharedStore) release() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.db.ExecContext(ctx, `UPDATE cachefill_shared SET owner='',expires=0 WHERE id=1 AND owner=? AND epoch=?`, s.node, s.epoch.Load())
}
func (s *SharedStore) load(ctx context.Context) ([]byte, Status, error) {
	var doc, status string
	var paused int
	err := s.db.QueryRowContext(ctx, `SELECT doc,status,paused FROM cachefill_shared WHERE id=1`).Scan(&doc, &status, &paused)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Status{State: "idle"}, nil
	}
	if err != nil {
		return nil, Status{}, err
	}
	var st Status
	if err = json.Unmarshal([]byte(status), &st); err != nil {
		return nil, st, err
	}
	st.Paused = paused != 0
	return []byte(doc), st, nil
}
func (s *SharedStore) save(ctx context.Context, doc []byte, st Status) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx, now int64) error {
		if err := s.guard(ctx, tx, now); err != nil {
			return err
		}
		if doc == nil {
			_, err = tx.ExecContext(ctx, `UPDATE cachefill_shared SET status=? WHERE id=1`, string(b))
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE cachefill_shared SET doc=?,status=? WHERE id=1`, string(doc), string(b))
		}
		return err
	})
}
func (s *SharedStore) pause(v bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.tx(ctx, func(tx *sql.Tx, _ int64) error {
		n := 0
		if v {
			n = 1
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO cachefill_shared(id,paused) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET paused=excluded.paused`, n)
		return err
	})
}

type transactionalPins interface {
	SharedPinTx(context.Context, *sql.Tx, string, string, bool) error
}

func (s *SharedStore) pin(ctx context.Context, c Cache, id string, add bool) error {
	p, ok := c.(transactionalPins)
	if !ok {
		return errors.New("shared preloader requires transactional cache pins")
	}
	return s.tx(ctx, func(tx *sql.Tx, now int64) error {
		if err := s.guard(ctx, tx, now); err != nil {
			return err
		}
		return p.SharedPinTx(ctx, tx, id, pinReason, add)
	})
}
func (m *Manager) runPass(ctx context.Context) error {
	shared := m.deps.Shared
	if shared == nil {
		return m.pass(ctx)
	}
	if err := shared.acquire(ctx); err != nil {
		return err
	}
	defer shared.release()
	b, st, err := shared.load(ctx)
	if err != nil {
		return err
	}
	if err = m.loadDocument(b); err != nil {
		return err
	}
	m.mu.Lock()
	m.status = st
	m.mu.Unlock()
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-run.Done():
				return
			case <-t.C:
				check, stop := context.WithTimeout(run, 5*time.Second)
				err := shared.renew(check)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	err = m.pass(run)
	if err == nil {
		shared.delayNext()
	}
	cancel()
	<-done
	if err != nil {
		return fmt.Errorf("shared preload: %w", err)
	}
	return nil
}
func (m *Manager) pin(id string, add bool) error {
	if m.deps.Shared != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return m.deps.Shared.pin(ctx, m.deps.Cache, id, add)
	}
	if add {
		return m.deps.Cache.Pin(id, pinReason)
	}
	return m.deps.Cache.Unpin(id, pinReason)
}

func (s *SharedStore) wake() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.db.ExecContext(ctx, `UPDATE cachefill_shared SET next_run=0 WHERE id=1`)
}
func (s *SharedStore) delayNext() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.tx(ctx, func(tx *sql.Tx, now int64) error {
		if err := s.guard(ctx, tx, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE cachefill_shared SET next_run=? WHERE id=1`, now+900)
		return err
	})
}
