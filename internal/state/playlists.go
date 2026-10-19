package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func playlistID(key core.RatingKey) (int64, error) {
	if key.Kind() != core.KindPlaylist {
		return 0, fmt.Errorf("%v: not a playlist key", key)
	}
	return key.TMDB(), nil // payload accessor: for non-season/episode kinds TMDB() returns the payload
}

func (s *State) loadPlaylist(ctx context.Context, userID, id int64) (*core.Playlist, error) {
	var p core.Playlist
	var smart int
	var upd int64
	err := s.db.QueryRowContext(ctx, `SELECT title, smart, updated_at FROM state_playlists WHERE id=? AND user_id=?`, id, userID).
		Scan(&p.Title, &smart, &upd)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.Key = core.PlaylistKey(id)
	p.UserID = userID
	p.Smart = smart != 0
	if upd > 0 {
		p.UpdatedAt = time.Unix(0, upd).UTC()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT rating_key FROM state_playlist_items WHERE playlist_id=? ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	p.Items = []core.RatingKey{}
	for rows.Next() {
		var k int64
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		p.Items = append(p.Items, core.RatingKey(k))
	}
	return &p, rows.Err()
}

// List returns the user's playlists (with items), most recently updated first.
func (s *State) List(ctx context.Context, userID int64) ([]core.Playlist, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM state_playlists WHERE user_id=? ORDER BY updated_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]core.Playlist, 0, len(ids))
	for _, id := range ids {
		p, err := s.loadPlaylist(ctx, userID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

// Get returns one playlist owned by the user, or core.ErrNotFound.
func (s *State) GetPlaylist(ctx context.Context, userID int64, key core.RatingKey) (*core.Playlist, error) {
	id, err := playlistID(key)
	if err != nil {
		return nil, err
	}
	return s.loadPlaylist(ctx, userID, id)
}

// Create makes a playlist with the given items in order.
func (s *State) Create(ctx context.Context, userID int64, title string, items []core.RatingKey) (*core.Playlist, error) {
	if title == "" {
		return nil, fmt.Errorf("playlist title is required")
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO state_playlists(user_id, title, smart, updated_at) VALUES(?,?,0,?)`, userID, title, now.UnixNano())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	for i, k := range items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO state_playlist_items(playlist_id, position, rating_key) VALUES(?,?,?)`, id, i, int64(k)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.loadPlaylist(ctx, userID, id)
}

func (s *State) rewriteItems(ctx context.Context, userID int64, key core.RatingKey, mutate func([]core.RatingKey) ([]core.RatingKey, error)) error {
	id, err := playlistID(key)
	if err != nil {
		return err
	}
	p, err := s.loadPlaylist(ctx, userID, id)
	if err != nil {
		return err
	}
	items, err := mutate(append([]core.RatingKey(nil), p.Items...))
	if err != nil {
		return err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM state_playlist_items WHERE playlist_id=?`, id); err != nil {
		return err
	}
	for i, k := range items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO state_playlist_items(playlist_id, position, rating_key) VALUES(?,?,?)`, id, i, int64(k)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE state_playlists SET updated_at=? WHERE id=?`, now.UnixNano(), id); err != nil {
		return err
	}
	return tx.Commit()
}

// Add appends items to a playlist.
func (s *State) Add(ctx context.Context, userID int64, key core.RatingKey, items []core.RatingKey) error {
	return s.rewriteItems(ctx, userID, key, func(cur []core.RatingKey) ([]core.RatingKey, error) {
		return append(cur, items...), nil
	})
}

// Remove deletes the item at itemIndex (0-based).
func (s *State) Remove(ctx context.Context, userID int64, key core.RatingKey, itemIndex int) error {
	return s.rewriteItems(ctx, userID, key, func(cur []core.RatingKey) ([]core.RatingKey, error) {
		if itemIndex < 0 || itemIndex >= len(cur) {
			return nil, fmt.Errorf("item index %d out of range (len %d)", itemIndex, len(cur))
		}
		return append(cur[:itemIndex], cur[itemIndex+1:]...), nil
	})
}

// Move moves the item at from so it lands at index to (both 0-based, to is
// the final position in the resulting list).
func (s *State) Move(ctx context.Context, userID int64, key core.RatingKey, from, to int) error {
	return s.rewriteItems(ctx, userID, key, func(cur []core.RatingKey) ([]core.RatingKey, error) {
		if from < 0 || from >= len(cur) || to < 0 || to >= len(cur) {
			return nil, fmt.Errorf("move %d -> %d out of range (len %d)", from, to, len(cur))
		}
		item := cur[from]
		cur = append(cur[:from], cur[from+1:]...)
		cur = append(cur, 0)
		copy(cur[to+1:], cur[to:])
		cur[to] = item
		return cur, nil
	})
}

// Delete removes a playlist. Deleting another user's playlist is ErrNotFound.
func (s *State) Delete(ctx context.Context, userID int64, key core.RatingKey) error {
	id, err := playlistID(key)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM state_playlists WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return core.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM state_playlist_items WHERE playlist_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// playlistsView adapts State to core.Playlists. core.State.Get and
// core.Playlists.Get have different signatures, so one Go type cannot
// implement both interfaces directly; State implements core.State and
// Playlists() returns the core.Playlists view of the same store.
type playlistsView struct{ s *State }

func (v playlistsView) List(ctx context.Context, userID int64) ([]core.Playlist, error) {
	return v.s.List(ctx, userID)
}
func (v playlistsView) Get(ctx context.Context, userID int64, key core.RatingKey) (*core.Playlist, error) {
	return v.s.GetPlaylist(ctx, userID, key)
}
func (v playlistsView) Create(ctx context.Context, userID int64, title string, items []core.RatingKey) (*core.Playlist, error) {
	return v.s.Create(ctx, userID, title, items)
}
func (v playlistsView) Add(ctx context.Context, userID int64, key core.RatingKey, items []core.RatingKey) error {
	return v.s.Add(ctx, userID, key, items)
}
func (v playlistsView) Remove(ctx context.Context, userID int64, key core.RatingKey, itemIndex int) error {
	return v.s.Remove(ctx, userID, key, itemIndex)
}
func (v playlistsView) Move(ctx context.Context, userID int64, key core.RatingKey, from, to int) error {
	return v.s.Move(ctx, userID, key, from, to)
}
func (v playlistsView) Delete(ctx context.Context, userID int64, key core.RatingKey) error {
	return v.s.Delete(ctx, userID, key)
}

var _ core.Playlists = playlistsView{}

// Playlists returns s as a core.Playlists.
func (s *State) Playlists() core.Playlists { return playlistsView{s} }
