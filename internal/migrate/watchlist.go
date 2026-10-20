package migrate

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/watchlist"
)

func (s *Service) watchlistTitles(ctx context.Context, items []Item, only bool) (map[core.RatingKey]core.Title, error) {
	titles := map[core.RatingKey]core.Title{}
	for _, item := range items {
		if !only && !item.Watchlist {
			continue
		}
		if _, ok := titles[item.Key]; ok {
			continue
		}
		if s.catalog == nil {
			return nil, errors.New("catalog unavailable")
		}
		title, err := s.catalog.Get(ctx, item.Key)
		if err != nil {
			return nil, err
		}
		if title == nil || title.Key != item.Key {
			return nil, core.ErrNotFound
		}
		titles[item.Key] = *title
	}
	return titles, nil
}

// RecoverWatchlist moves records from the old import-only table into the live
// watchlist. It runs during viewer sync (and exports) so existing imports are
// recovered as well. Metadata is fetched before opening a write transaction.
// Failed lookups leave the original rows intact for a later retry.
func (s *Service) RecoverWatchlist(ctx context.Context, viewer int64) error {
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM migrate_watchlist WHERE viewer=? ORDER BY key`, viewer)
	if err != nil {
		return err
	}
	var items []Item
	for rows.Next() {
		var item Item
		if err = rows.Scan(&item.Key); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(items) == 0 {
		return err
	}
	titles, err := s.watchlistTitles(ctx, items, true)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = store.LockKey(ctx, store.DialectOf(s.db), tx, "migrate:import"); err != nil {
		return err
	}
	for _, item := range items {
		// Recheck under the lock: another node may already have recovered this
		// row and the viewer may subsequently have removed the live item.
		var key core.RatingKey
		err = tx.QueryRowContext(ctx, `DELETE FROM migrate_watchlist WHERE viewer=? AND key=? RETURNING key`, viewer, item.Key).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if err = watchlist.ImportTitleTx(ctx, tx, viewer, titles[key], time.Now().Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
