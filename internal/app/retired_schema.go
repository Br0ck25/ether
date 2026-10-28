package app

import "github.com/braedonsaunders/ether/internal/store"

// The invitations feature was removed, but databases that ran its
// migrations still hold its (now unused) tables and versions. Logical
// backups carry both, and restoring one (or moving a server to PostgreSQL)
// requires the target to know every component version and table, so the
// schema stays registered exactly as it was applied.
func init() {
	store.Register("invites", 1, `CREATE TABLE invites_links(id INTEGER PRIMARY KEY, token_hash TEXT UNIQUE NOT NULL,label TEXT NOT NULL,expires INTEGER NOT NULL,max_uses INTEGER NOT NULL,uses INTEGER NOT NULL DEFAULT 0,policy TEXT NOT NULL,sections TEXT NOT NULL,revoked INTEGER NOT NULL DEFAULT 0); CREATE TABLE invites_claims(invite_id INTEGER NOT NULL,plex_id INTEGER NOT NULL,state TEXT NOT NULL,PRIMARY KEY(invite_id,plex_id));`)
	store.Register("invites", 2, `CREATE TABLE invites_pins(handle_hash TEXT PRIMARY KEY, invite_id INTEGER NOT NULL, pin_id TEXT NOT NULL, expires INTEGER NOT NULL, plex_id INTEGER NOT NULL DEFAULT 0, email TEXT NOT NULL DEFAULT '');
CREATE INDEX invites_pins_expires ON invites_pins(expires);`)
}
