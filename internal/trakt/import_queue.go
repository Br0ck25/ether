package trakt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/braedonsaunders/ether/internal/state"
	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	store.Register("trakt", 2, `
CREATE TABLE trakt_import_queue (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES trakt_tokens(user_id) ON DELETE CASCADE,
  username TEXT NOT NULL,
  event_json TEXT NOT NULL
);`)
	// Claim columns let several nodes drain the queue without importing a
	// record twice at once; a claim older than importClaimTimeout is taken
	// over, keeping the queue's retry-until-delivered behaviour.
	store.Register("trakt", 3, `
ALTER TABLE trakt_import_queue ADD COLUMN claimed_by TEXT NOT NULL DEFAULT '';
ALTER TABLE trakt_import_queue ADD COLUMN claimed_at INTEGER NOT NULL DEFAULT 0;`)
}

// importClaimTimeout is how long a claimed import may stay unfinished before
// another node presumes its claimant gone and takes it over.
const importClaimTimeout = 5 * time.Minute

// enqueueImport keeps historical writes out of the bounded live timeline
// queue. Only an integration connected at import time receives the history.
func (c *Client) enqueueImport(ev state.Event) {
	if ev.Kind != state.EventScrobble && ev.Kind != state.EventRated || ev.At.IsZero() || !c.Enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tokens, connected, err := c.Tokens(ctx, ev.UserID)
	if err != nil || !connected {
		return
	}
	b, err := json.Marshal(ev)
	if err == nil {
		_, err = c.db.ExecContext(ctx, `INSERT INTO trakt_import_queue(user_id,username,event_json) VALUES(?,?,?)`, ev.UserID, tokens.Username, string(b))
	}
	if err != nil {
		c.log.Error("persist Trakt imported history", "user", ev.UserID, "key", ev.Key, "err", err)
		return
	}
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

func (c *Client) drainImports(ctx context.Context) error {
	if c.db == nil || !c.Enabled() {
		return nil
	}
	for {
		if !c.Enabled() {
			return nil
		}
		// Historical imports may take hours under Trakt's POST rate limit.
		// Yield between records so active playback scrobbles stay current.
		c.evMu.Lock()
		livePending := len(c.queue) > 0
		c.evMu.Unlock()
		if livePending {
			return nil // the live queue already signalled notify
		}
		select {
		case <-c.done:
			return nil
		default:
		}
		var id int64
		var raw, account, currentAccount string
		now := c.now().Unix()
		stale := now - int64(importClaimTimeout/time.Second)
		err := c.db.QueryRowContext(ctx, `SELECT q.id,q.event_json,q.username,t.username FROM trakt_import_queue q JOIN trakt_tokens t ON t.user_id=q.user_id
			WHERE q.claimed_by='' OR q.claimed_at<? ORDER BY q.id LIMIT 1`, stale).Scan(&id, &raw, &account, &currentAccount)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		// Claim the record; if another node claimed it first, pick again.
		res, err := c.db.ExecContext(ctx, `UPDATE trakt_import_queue SET claimed_by=?, claimed_at=? WHERE id=? AND (claimed_by='' OR claimed_at<?)`,
			c.claimID, now, id, stale)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			continue
		}
		var ev state.Event
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			c.releaseImport(id)
			return err
		}
		// Never transfer pending history to a different linked Trakt account.
		if account == currentAccount {
			ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err = c.handleEvent(ectx, ev)
			cancel()
			if err != nil {
				c.releaseImport(id)
				return err // retained for retry, including across server restarts
			}
		}
		if _, err = c.db.ExecContext(ctx, `DELETE FROM trakt_import_queue WHERE id=? AND claimed_by=?`, id, c.claimID); err != nil {
			return err
		}
	}
}

// releaseImport returns this node's claim so the record is retried promptly.
func (c *Client) releaseImport(id int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.db.ExecContext(ctx, `UPDATE trakt_import_queue SET claimed_by='', claimed_at=0 WHERE id=? AND claimed_by=?`, id, c.claimID); err != nil {
		c.log.Warn("release Trakt import claim", "id", id, "err", err)
	}
}
