package catalog_test

import (
	"context"
	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"log/slog"
	"testing"
	"time"
)

func TestHistoricalReleaseDatesRemainVisible(t *testing.T) {
	db := store.OpenTest(t)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	past := time.Date(1969, 9, 13, 0, 0, 0, 0, time.UTC).Unix()
	show := core.ShowKey(926)
	season := core.SeasonKey(926, 1)
	type entry struct {
		k          core.RatingKey
		typ        string
		show       core.RatingKey
		season, ep int
		date       int64
	}
	for _, e := range []entry{
		{core.MovieKey(1), "movie", 0, 0, 0, past}, {core.MovieKey(2), "movie", 0, 0, 0, 0}, {core.MovieKey(3), "movie", 0, 0, 0, now.Add(24 * time.Hour).Unix()},
		{show, "show", 0, 0, 0, past}, {season, "season", show, 1, 0, past},
		{core.EpisodeKey(926, 1, 1), "episode", show, 1, 1, past}, {core.EpisodeKey(926, 1, 2), "episode", show, 1, 2, 0}, {core.EpisodeKey(926, 1, 3), "episode", show, 1, 3, now.Add(24 * time.Hour).Unix()},
	} {
		_, err := db.Exec(`INSERT INTO catalog_titles(key,type,show_key,show_tmdb,season,episode,title,released_ts,browsable,child_count,detail,detail_at) VALUES(?,?,?,?,?,?,'Historical',?,1,3,1,?)`, e.k, e.typ, e.show, 926, e.season, e.ep, e.date, now.Unix())
		if err != nil {
			t.Fatal(err)
		}
	}
	c, err := catalog.New(db, nil, config.Catalog{}, slog.New(slog.DiscardHandler), catalog.WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, typ := range []core.MediaType{core.TypeMovie, core.TypeShow} {
		p, err := c.Browse(ctx, core.BrowseQuery{Filter: core.Filter{Type: typ}})
		if err != nil || len(p.Items) != 1 {
			t.Errorf("%s browse: %d items, error %v; want historical title only", typ, len(p.Items), err)
		}
	}
	seasons, err := c.Children(ctx, show)
	if err != nil || len(seasons) != 1 {
		t.Errorf("historical seasons: %v, error %v", episodeKeys(seasons), err)
	}
	st, err := c.Get(ctx, season)
	if err != nil {
		t.Errorf("historical season inaccessible: %v", err)
	} else if st.ChildCount != 1 {
		t.Errorf("aired count %d, want 1", st.ChildCount)
	}
	eps, err := c.Children(ctx, season)
	if err != nil || len(eps) != 1 || eps[0].Key != core.EpisodeKey(926, 1, 1) {
		t.Errorf("historical episodes: %v, error %v", episodeKeys(eps), err)
	}
}

func TestUndatedSpecialsUseKnownAiredEpisodeDate(t *testing.T) {
	db := store.OpenTest(t)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	aired := now.Add(-24 * time.Hour)
	for _, show := range []int64{701, 702, 703} {
		_, err := db.Exec(`INSERT INTO catalog_titles(key,type,title,released_ts,child_count,detail,detail_at) VALUES(?,'show','Series',?,1,1,?)`, core.ShowKey(show), aired.Unix(), now.Unix())
		if err != nil {
			t.Fatal(err)
		}
		seasonDate := int64(0)
		epDate := aired.Unix()
		if show == 702 {
			epDate = now.Add(24 * time.Hour).Unix()
		}
		if show == 703 {
			seasonDate = now.Add(24 * time.Hour).Unix()
		}
		_, err = db.Exec(`INSERT INTO catalog_titles(key,type,show_key,show_tmdb,season,title,released_ts,child_count,detail,detail_at) VALUES(?,'season',?,?,0,'Specials',?,1,1,?)`, core.SeasonKey(show, 0), core.ShowKey(show), show, seasonDate, now.Unix())
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`INSERT INTO catalog_titles(key,type,show_key,show_tmdb,season,episode,title,released_ts,detail,detail_at) VALUES(?,'episode',?,?,0,1,'Special',?,1,?)`, core.EpisodeKey(show, 0, 1), core.ShowKey(show), show, epDate, now.Unix())
		if err != nil {
			t.Fatal(err)
		}
	}
	c, err := catalog.New(db, nil, config.Catalog{}, slog.New(slog.DiscardHandler), catalog.WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	for _, show := range []int64{701, 702, 703} {
		seasons, err := c.Children(t.Context(), core.ShowKey(show))
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if show == 701 {
			want = 1
		}
		if len(seasons) != want {
			t.Errorf("show %d: %d seasons, want %d", show, len(seasons), want)
		}
		s, e := c.Get(t.Context(), core.SeasonKey(show, 0))
		if show == 701 {
			if e != nil {
				t.Errorf("aired special inaccessible: %v", e)
			} else if !s.Released.Equal(aired) || s.ChildCount != 1 {
				t.Errorf("unexpected derived special: %+v", s)
			}
		} else if e == nil {
			t.Errorf("unreleased special container admitted: %d", show)
		}
	}
}
