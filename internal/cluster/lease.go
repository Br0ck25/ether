package cluster

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DefaultUpdateLeaseTTL bounds how long a node may hold the update lease
// without renewing it: long enough to drain, install, restart and confirm
// a release, short enough that a node that died mid-update does not block
// the rest of the cluster for long.
const DefaultUpdateLeaseTTL = 20 * time.Minute

// ErrVersionFailed is returned by UpdateLease.Acquire for a release that
// was rolled back on some node of the cluster; the other nodes skip it.
var ErrVersionFailed = errors.New("cluster: this release was rolled back on another node")

// UpdateLease serialises self-updates across a cluster: a node installs a
// release only while it holds the lease, so nodes restart one at a time.
// The lease is one row (cluster_update_lease) taken with a conditional
// upsert — free, expired or already ours — and renewed by the holder while
// it drains, installs, restarts and confirms the new version. Times are
// Unix milliseconds from the caller's clock (see Postgres for the clock
// policy).
type UpdateLease struct {
	DB     *sql.DB
	NodeID string
	// TTL is the lease lifetime from each acquisition or renewal
	// (DefaultUpdateLeaseTTL).
	TTL time.Duration
	Now func() time.Time
}

// LeaseHolder describes the current lease.
type LeaseHolder struct {
	NodeID     string    `json:"nodeId"`
	Version    string    `json:"version"`
	AcquiredAt time.Time `json:"acquiredAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func (l *UpdateLease) now() time.Time {
	if l.Now == nil {
		return time.Now()
	}
	return l.Now()
}

func (l *UpdateLease) ttl() time.Duration {
	if l.TTL <= 0 {
		return DefaultUpdateLeaseTTL
	}
	return l.TTL
}

// Acquire takes the lease for installing version, or renews it when this
// node already holds it. It reports false while another node holds an
// unexpired lease, and ErrVersionFailed when version rolled back on some
// node.
func (l *UpdateLease) Acquire(ctx context.Context, version string) (bool, error) {
	now := l.now()
	nowMS, exp := now.UnixMilli(), now.Add(l.ttl()).UnixMilli()
	res, err := l.DB.ExecContext(ctx, `INSERT INTO cluster_update_lease(id, node_id, version, acquired_at, expires_at, failed_version)
VALUES (1, ?, ?, ?, ?, '')
ON CONFLICT(id) DO UPDATE SET
	acquired_at = CASE WHEN cluster_update_lease.node_id = excluded.node_id THEN cluster_update_lease.acquired_at ELSE excluded.acquired_at END,
	node_id = excluded.node_id, version = excluded.version, expires_at = excluded.expires_at
WHERE (cluster_update_lease.node_id = excluded.node_id OR cluster_update_lease.node_id = '' OR cluster_update_lease.expires_at < ?)
	AND cluster_update_lease.failed_version <> excluded.version`,
		l.NodeID, version, nowMS, exp, nowMS)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return n == 1, err
	}
	var failed string
	if err := l.DB.QueryRowContext(ctx, `SELECT failed_version FROM cluster_update_lease WHERE id=1`).Scan(&failed); err == nil && failed != "" && failed == version {
		return false, ErrVersionFailed
	}
	return false, nil
}

// Release gives the lease back if this node holds it.
func (l *UpdateLease) Release(ctx context.Context) error {
	_, err := l.DB.ExecContext(ctx, `UPDATE cluster_update_lease SET node_id='', expires_at=0 WHERE id=1 AND node_id=?`, l.NodeID)
	return err
}

// ReportFailed records that version rolled back on this node — the other
// nodes will not install it — and releases the lease if this node holds it.
func (l *UpdateLease) ReportFailed(ctx context.Context, version string) error {
	_, err := l.DB.ExecContext(ctx, `INSERT INTO cluster_update_lease(id, node_id, version, acquired_at, expires_at, failed_version)
VALUES (1, '', '', 0, 0, ?)
ON CONFLICT(id) DO UPDATE SET failed_version = excluded.failed_version,
	node_id = CASE WHEN cluster_update_lease.node_id = ? THEN '' ELSE cluster_update_lease.node_id END,
	expires_at = CASE WHEN cluster_update_lease.node_id = ? THEN 0 ELSE cluster_update_lease.expires_at END`,
		version, l.NodeID, l.NodeID)
	return err
}

// Holder returns the current unexpired lease, or nil when the lease is
// free.
func (l *UpdateLease) Holder(ctx context.Context) (*LeaseHolder, error) {
	var h LeaseHolder
	var acq, exp int64
	err := l.DB.QueryRowContext(ctx, `SELECT node_id, version, acquired_at, expires_at FROM cluster_update_lease WHERE id=1`).
		Scan(&h.NodeID, &h.Version, &acq, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if h.NodeID == "" || exp < l.now().UnixMilli() {
		return nil, nil
	}
	h.AcquiredAt, h.ExpiresAt = time.UnixMilli(acq), time.UnixMilli(exp)
	return &h, nil
}
