package auth

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plextv"
)

// delayedReloadConnector delays only the reload carrying this gate in its
// context. The real SQLite rows close first, so no database lock prevents the
// authoritative revocation from committing while the old snapshot is paused.
type delayedReloadConnector struct {
	driver *sqlite.Driver
	path   string
}

func (c delayedReloadConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.path)
	if err != nil {
		return nil, err
	}
	return delayedReloadConn{Conn: conn}, nil
}

func (c delayedReloadConnector) Driver() driver.Driver { return c.driver }

type delayedReloadConn struct{ driver.Conn }
type reloadGateKey struct{}
type reloadGate struct {
	read, release chan struct{}
}

func (c delayedReloadConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	if gate, ok := ctx.Value(reloadGateKey{}).(*reloadGate); ok && query == `SELECT token_hash, user_id FROM auth_tokens` {
		return &delayedReloadRows{Rows: rows, gate: gate}, nil
	}
	return rows, nil
}

type delayedReloadRows struct {
	driver.Rows
	gate *reloadGate
}

func (r *delayedReloadRows) Close() error {
	err := r.Rows.Close()
	close(r.gate.read)
	<-r.gate.release
	return err
}

func TestEarlierReloadCannotRestoreRevokedAccess(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ether.db")
	observer := openDBAt(t, path)
	db := sql.OpenDB(delayedReloadConnector{driver: &sqlite.Driver{}, path: path})
	t.Cleanup(func() { db.Close() })
	s, err := New(ctx, db, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SyncFriends(ctx, []plextv.Friend{{PlexID: 100, Username: "viewer", Token: "viewer-token", AllLibs: true}}); err != nil {
		t.Fatal(err)
	}
	u, err := s.Authenticate(ctx, "viewer-token")
	if err != nil {
		t.Fatal(err)
	}
	gate := &reloadGate{read: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(gate.release) }) }
	defer unblock()
	reloaded := make(chan error, 1)
	go func() { reloaded <- s.reload(context.WithValue(ctx, reloadGateKey{}, gate)) }()
	select {
	case <-gate.read:
	case <-time.After(5 * time.Second):
		t.Fatal("old reload did not reach the snapshot boundary")
	}
	revoked := make(chan error, 1)
	go func() { revoked <- s.SyncFriends(ctx, nil) }()
	// The write must commit despite the paused reader. With ordered reloads,
	// SyncFriends waits to publish its newer snapshot before returning.
	eventually(t, "revocation committed", func() bool {
		var active int
		err := observer.QueryRow(`SELECT active FROM auth_users WHERE plex_id=100`).Scan(&active)
		return err == nil && active == 0
	})
	var revocationErr error
	select {
	case revocationErr = <-revoked:
		// Before the fix the newer reload finishes first; resuming the old
		// one below then restores its active user and synced token.
		unblock()
	case <-time.After(250 * time.Millisecond):
		unblock()
		revocationErr = <-revoked
	}
	if err := <-reloaded; err != nil {
		t.Fatal(err)
	}
	if revocationErr != nil {
		t.Fatal(revocationErr)
	}
	if _, err := s.Authenticate(ctx, "viewer-token"); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("older reload restored revoked token access: %v", err)
	}
	if _, err := s.ActiveUser(ctx, u.ID); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatalf("older reload restored stream-grant access: %v", err)
	}
}
