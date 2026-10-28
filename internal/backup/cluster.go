package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

// Backups on a clustered server.
//
// Scheduled backups run on whichever node leads, and each node writes to
// its own data directory unless the owner configures a shared backup
// directory (Settings.SharedDir) that every node reaches at the same path.
// Either way every bundle is recorded in backup_files with the node that
// wrote it, so the listing on any node shows the whole cluster's backups
// and where each one is. A node reconciles the records of the locations it
// can see (its own directory, and the shared one) with the files there
// whenever it creates or lists backups. Retention applies per location:
// each node keeps Retain bundles of its own, the shared directory keeps
// Retain in total.

// record reconciles the rows for the bundles in dir (this node's own
// directory, or the shared one) with the files present.
func (s *Service) record(ctx context.Context, node Node, dir string, shared bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	lockKey := "backup:node:" + node.ID
	if shared {
		lockKey = "backup:shared:" + filepath.Clean(dir)
	}
	// Every node must agree which shared bundles are already recorded.
	// Take the filesystem snapshot after the lock too, so a waiter cannot
	// delete newer records based on a directory listing from before its wait.
	if err := store.LockKey(ctx, store.DialectOf(s.db), tx, lockKey); err != nil {
		return err
	}
	snaps, err := listDir(dir)
	if err != nil {
		return err
	}
	present := make(map[string]Snapshot, len(snaps))
	for _, sn := range snaps {
		present[sn.Name] = sn
	}
	q, args := `SELECT node_id, name FROM backup_files WHERE node_id=? AND shared=0`, []any{node.ID}
	if shared {
		q, args = `SELECT node_id, name FROM backup_files WHERE shared=1 AND dir=?`, []any{dir}
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	type key struct{ node, name string }
	var stale []key
	known := map[string]bool{}
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.node, &k.name); err != nil {
			rows.Close()
			return err
		}
		if _, ok := present[k.name]; ok {
			known[k.name] = true
		} else {
			stale = append(stale, k)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, k := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM backup_files WHERE node_id=? AND name=?`, k.node, k.name); err != nil {
			return err
		}
	}
	flag := 0
	if shared {
		flag = 1
	}
	for name, sn := range present {
		if known[name] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO backup_files(node_id,name,node_name,dir,shared,size,created) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(node_id,name) DO UPDATE SET node_name=excluded.node_name, dir=excluded.dir, shared=excluded.shared, size=excluded.size, created=excluded.created`,
			node.ID, name, node.Name, dir, flag, sn.Size, sn.Created.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Snapshots lists backups for the owner: this node's bundles on a
// standalone server, every node's (with where each is) on a clustered one.
func (s *Service) Snapshots(ctx context.Context) ([]Snapshot, error) {
	node := s.clusterNode()
	if node == nil {
		return s.List()
	}
	if err := s.record(ctx, *node, s.dir, false); err != nil {
		return nil, err
	}
	shared := ""
	if st, err := s.Settings(ctx); err == nil && st.SharedDir != "" {
		if fi, err := os.Stat(st.SharedDir); err == nil && fi.IsDir() {
			shared = st.SharedDir
			if err := s.record(ctx, *node, shared, true); err != nil {
				return nil, err
			}
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT node_id,name,node_name,dir,shared,size,created FROM backup_files ORDER BY created DESC, name DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Snapshot{}
	for rows.Next() {
		var (
			sn       Snapshot
			dir      string
			isShared int
			created  int64
		)
		if err := rows.Scan(&sn.NodeID, &sn.Name, &sn.Node, &dir, &isShared, &sn.Size, &created); err != nil {
			return nil, err
		}
		sn.Created = time.UnixMilli(created).UTC()
		sn.Shared = isShared != 0
		if sn.Shared {
			sn.Remote = dir != shared // not reachable from this node
		} else {
			sn.Remote = sn.NodeID != node.ID
		}
		out = append(out, sn)
	}
	return out, rows.Err()
}

// errElsewhere is returned by locate for a bundle held by another node.
type errElsewhere struct{ node string }

func (e errElsewhere) Error() string {
	return fmt.Sprintf("this backup is stored on node %q; download it from that node", e.node)
}

// locate finds a bundle by name in this node's directory or the shared
// one; for a bundle another node holds it returns errElsewhere.
func (s *Service) locate(ctx context.Context, name string) (string, error) {
	path := filepath.Join(s.dir, name)
	if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
		return path, nil
	}
	if s.clusterNode() == nil {
		return "", os.ErrNotExist
	}
	if st, err := s.Settings(ctx); err == nil && st.SharedDir != "" {
		path = filepath.Join(st.SharedDir, name)
		if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
			return path, nil
		}
	}
	var holder string
	err := s.db.QueryRowContext(ctx, `SELECT node_name FROM backup_files WHERE name=? AND shared=0 ORDER BY created DESC`, name).Scan(&holder)
	if err == nil {
		return "", errElsewhere{node: holder}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return "", os.ErrNotExist
}
