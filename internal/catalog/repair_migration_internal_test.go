package catalog

import (
	"context"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// Rows stored before episodeKeyFits existed (a year-numbered season under
// another show's episode key) are dropped by the repair migration; keyable
// rows stay.
func TestDropUnkeyableEpisodes(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows := []struct {
		key             core.RatingKey
		typ             string
		show            int64
		season, episode int
		keep            bool
	}{
		{core.EpisodeKey(302, 23, 1), "episode", 300, 2023, 1, false}, // collided: 300 S2023E1 under 302 S23E1's key
		{core.EpisodeKey(300, 1, 1), "episode", 300, 1, 12345, false},
		{core.SeasonKey(300, 2023), "season", 300, 2023, 0, false},
		{core.EpisodeKey(300, 1, 1) + 1, "episode", 300, 1, 2, true},
		{core.SeasonKey(300, 1), "season", 300, 1, 0, true},
	}
	for _, r := range rows {
		if _, err := db.ExecContext(ctx, `INSERT INTO catalog_titles (key,type,tmdb_id,show_tmdb,season,episode,title) VALUES (?,?,?,?,?,?,?)`,
			int64(r.key), r.typ, r.show, r.show, r.season, r.episode, "x"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, dropUnkeyableEpisodes); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_titles WHERE key=?`, int64(r.key)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if (n == 1) != r.keep {
			t.Errorf("key %d (%s S%dE%d): present=%v, want %v", r.key, r.typ, r.season, r.episode, n == 1, r.keep)
		}
	}
}
