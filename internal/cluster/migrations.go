package cluster

import "github.com/braedonsaunders/ether/internal/store"

// Times are Unix milliseconds written from each node's clock (see
// Postgres for the clock-skew policy). The tables exist on SQLite too, where
// they stay empty: Solo never touches them.
func init() {
	store.Register("cluster", 1, `
CREATE TABLE cluster_nodes (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	version TEXT NOT NULL DEFAULT '',
	addrs TEXT NOT NULL DEFAULT '[]',
	caps TEXT NOT NULL DEFAULT '{}',
	active_sessions INTEGER NOT NULL DEFAULT 0,
	started_at INTEGER NOT NULL,
	last_seen INTEGER NOT NULL,
	incarnation TEXT NOT NULL DEFAULT ''
);
CREATE INDEX cluster_nodes_last_seen ON cluster_nodes(last_seen);
CREATE TABLE cluster_leader (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	epoch INTEGER NOT NULL,
	node_id TEXT NOT NULL,
	acquired_at INTEGER NOT NULL
);`)
}
