package cardigann

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	store.Register("cardigann", 1, `
CREATE TABLE cardigann_definitions (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  yaml TEXT NOT NULL,
  origin TEXT NOT NULL DEFAULT '',
  sha TEXT NOT NULL DEFAULT '',
  installed_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE cardigann_sessions (
  indexer_id TEXT PRIMARY KEY,
  cookies TEXT NOT NULL,
  user_agent TEXT NOT NULL DEFAULT '',
  version INTEGER NOT NULL DEFAULT 1,
  updated_at INTEGER NOT NULL
);
CREATE TABLE cardigann_tests (
  indexer_id TEXT PRIMARY KEY,
  ok INTEGER NOT NULL,
  message TEXT NOT NULL DEFAULT '',
  results INTEGER NOT NULL DEFAULT 0,
  tested_at INTEGER NOT NULL
);`)
}

// storedDefinition is one installed definition row.
type storedDefinition struct {
	ID          string
	Name        string
	YAML        []byte
	Origin      string // source URL it was downloaded from; "" for uploads
	SHA         string // source revision (GitHub blob sha) when known
	InstalledAt time.Time
	UpdatedAt   time.Time
}

// defStore persists definitions in the database, or in memory when db is nil.
type defStore struct {
	db  *sql.DB
	mu  sync.Mutex
	mem map[string]storedDefinition
}

func (s *defStore) list(ctx context.Context) ([]storedDefinition, error) {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := make([]storedDefinition, 0, len(s.mem))
		for _, d := range s.mem {
			out = append(out, d)
		}
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, yaml, origin, sha, installed_at, updated_at FROM cardigann_definitions ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedDefinition
	for rows.Next() {
		var d storedDefinition
		var y string
		var ins, upd int64
		if err := rows.Scan(&d.ID, &d.Name, &y, &d.Origin, &d.SHA, &ins, &upd); err != nil {
			return nil, err
		}
		d.YAML, d.InstalledAt, d.UpdatedAt = []byte(y), time.Unix(ins, 0), time.Unix(upd, 0)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *defStore) get(ctx context.Context, id string) (storedDefinition, bool, error) {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		d, ok := s.mem[id]
		return d, ok, nil
	}
	var d storedDefinition
	var y string
	var ins, upd int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, yaml, origin, sha, installed_at, updated_at FROM cardigann_definitions WHERE id=?`, id).
		Scan(&d.ID, &d.Name, &y, &d.Origin, &d.SHA, &ins, &upd)
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	d.YAML, d.InstalledAt, d.UpdatedAt = []byte(y), time.Unix(ins, 0), time.Unix(upd, 0)
	return d, true, nil
}

func (s *defStore) put(ctx context.Context, d storedDefinition) error {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.mem == nil {
			s.mem = map[string]storedDefinition{}
		}
		if old, ok := s.mem[d.ID]; ok {
			d.InstalledAt = old.InstalledAt
		}
		s.mem[d.ID] = d
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO cardigann_definitions(id, name, yaml, origin, sha, installed_at, updated_at) VALUES (?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, yaml=excluded.yaml, origin=excluded.origin, sha=excluded.sha, updated_at=excluded.updated_at`,
		d.ID, d.Name, string(d.YAML), d.Origin, d.SHA, d.InstalledAt.Unix(), d.UpdatedAt.Unix())
	return err
}

func (s *defStore) delete(ctx context.Context, id string) error {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.mem, id)
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM cardigann_definitions WHERE id=?`, id)
	return err
}

// sessionRow is the shared login state of one indexer. Version increases
// on every saved login so nodes can tell whether someone else refreshed it.
type sessionRow struct {
	Cookies   []savedCookie
	UserAgent string
	Version   int64
	UpdatedAt time.Time
}

// sessionStore is the source of truth for login cookies; nodes keep only
// a cached copy.
type sessionStore struct {
	db  *sql.DB
	now func() time.Time
	mu  sync.Mutex
	mem map[string]sessionRow
}

func (s *sessionStore) load(ctx context.Context, indexerID string) (sessionRow, bool, error) {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		r, ok := s.mem[indexerID]
		return r, ok, nil
	}
	var raw, ua string
	var r sessionRow
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT cookies, user_agent, version, updated_at FROM cardigann_sessions WHERE indexer_id=?`, indexerID).
		Scan(&raw, &ua, &r.Version, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	_ = json.Unmarshal([]byte(raw), &r.Cookies)
	r.UserAgent, r.UpdatedAt = ua, time.Unix(at, 0)
	return r, true, nil
}

// save stores a fresh login in one statement, incrementing the version,
// and returns the new version.
func (s *sessionStore) save(ctx context.Context, indexerID string, cookies []savedCookie, ua string) (int64, error) {
	raw, _ := json.Marshal(cookies)
	now := s.now()
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.mem == nil {
			s.mem = map[string]sessionRow{}
		}
		r := s.mem[indexerID]
		r.Cookies, r.UserAgent, r.Version, r.UpdatedAt = cookies, ua, r.Version+1, now
		s.mem[indexerID] = r
		return r.Version, nil
	}
	var version int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO cardigann_sessions(indexer_id, cookies, user_agent, version, updated_at) VALUES (?,?,?,1,?)
ON CONFLICT(indexer_id) DO UPDATE SET cookies=excluded.cookies, user_agent=excluded.user_agent, version=cardigann_sessions.version+1, updated_at=excluded.updated_at
RETURNING version`, indexerID, string(raw), ua, now.Unix()).Scan(&version)
	return version, err
}

func (s *sessionStore) clear(ctx context.Context, indexerID string) error {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.mem, indexerID)
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM cardigann_sessions WHERE indexer_id=?`, indexerID)
	return err
}

// TestStatus is the outcome of an indexer's most recent test.
type TestStatus struct {
	IndexerID string    `json:"indexerId"`
	OK        bool      `json:"ok"`
	Message   string    `json:"message,omitempty"`
	Results   int       `json:"results"`
	TestedAt  time.Time `json:"testedAt"`
}

type testStore struct {
	db  *sql.DB
	mu  sync.Mutex
	mem map[string]TestStatus
}

func (s *testStore) put(ctx context.Context, t TestStatus) error {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.mem == nil {
			s.mem = map[string]TestStatus{}
		}
		s.mem[t.IndexerID] = t
		return nil
	}
	ok := 0
	if t.OK {
		ok = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO cardigann_tests(indexer_id, ok, message, results, tested_at) VALUES (?,?,?,?,?)
ON CONFLICT(indexer_id) DO UPDATE SET ok=excluded.ok, message=excluded.message, results=excluded.results, tested_at=excluded.tested_at`,
		t.IndexerID, ok, t.Message, t.Results, t.TestedAt.Unix())
	return err
}

func (s *testStore) list(ctx context.Context) ([]TestStatus, error) {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := make([]TestStatus, 0, len(s.mem))
		for _, t := range s.mem {
			out = append(out, t)
		}
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT indexer_id, ok, message, results, tested_at FROM cardigann_tests`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TestStatus
	for rows.Next() {
		var t TestStatus
		var ok int
		var at int64
		if err := rows.Scan(&t.IndexerID, &ok, &t.Message, &t.Results, &at); err != nil {
			return nil, err
		}
		t.OK, t.TestedAt = ok != 0, time.Unix(at, 0)
		out = append(out, t)
	}
	return out, rows.Err()
}

// keyedMutex is the default LoginLock: an in-process lock per key that
// respects context cancellation.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

func (k *keyedMutex) lock(ctx context.Context, key string) (func(), error) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]chan struct{}{}
	}
	ch, ok := k.locks[key]
	if !ok {
		ch = make(chan struct{}, 1)
		k.locks[key] = ch
	}
	k.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
