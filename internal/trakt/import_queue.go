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
}

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
		err := c.db.QueryRowContext(ctx, `SELECT q.id,q.event_json,q.username,t.username FROM trakt_import_queue q JOIN trakt_tokens t ON t.user_id=q.user_id ORDER BY q.id LIMIT 1`).Scan(&id, &raw, &account, &currentAccount)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var ev state.Event
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			return err
		}
		// Never transfer pending history to a different linked Trakt account.
		if account == currentAccount {
			ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err = c.handleEvent(ectx, ev)
			cancel()
			if err != nil {
				return err // retained for retry, including across server restarts
			}
		}
		if _, err = c.db.ExecContext(ctx, `DELETE FROM trakt_import_queue WHERE id=?`, id); err != nil {
			return err
		}
	}
}
