package catalog_test

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

// finishedSeasons is show 800 with three seasons of two episodes, all aired
// long ago except season 2's second episode, which has no air date yet.
// It counts SeasonDetails requests; other TMDB calls panic.
type finishedSeasons struct {
	catalog.TMDB
	mu    sync.Mutex
	calls map[int]int
}

func (f *finishedSeasons) TVDetails(_ context.Context, id int64) (*tmdb.TVDetails, error) {
	d := &tmdb.TVDetails{TVShow: tmdb.TVShow{ID: id, Name: "Long Run", FirstAirDate: "2001-01-01", Popularity: 50, VoteCount: 500}}
	for n := 1; n <= 3; n++ {
		d.Seasons = append(d.Seasons, tmdb.SeasonSummary{ID: int64(n), Name: fmt.Sprintf("Season %d", n), SeasonNumber: n, EpisodeCount: 2, AirDate: fmt.Sprintf("%d-01-01", 2000+n)})
	}
	return d, nil
}

func (f *finishedSeasons) SeasonDetails(_ context.Context, id int64, season int) (*tmdb.SeasonDetails, error) {
	f.mu.Lock()
	f.calls[season]++
	f.mu.Unlock()
	sd := &tmdb.SeasonDetails{ID: int64(season), Name: fmt.Sprintf("Season %d", season), SeasonNumber: season, AirDate: fmt.Sprintf("%d-01-01", 2000+season)}
	for e := 1; e <= 2; e++ {
		air := fmt.Sprintf("%d-01-%02d", 2000+season, e)
		if season == 2 && e == 2 {
			air = ""
		}
		sd.Episodes = append(sd.Episodes, tmdb.Episode{ID: int64(season*10 + e), Name: fmt.Sprintf("E%d", e), SeasonNumber: season, EpisodeNumber: e, AirDate: air})
	}
	return sd, nil
}

func (f *finishedSeasons) fetched() map[int]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = map[int]int{}
	return out
}

// A whole-show read fetches missing seasons, and once its seasons go stale
// refetches only those that can still change: the latest, and one with an
// episode yet to air. A finished season is refreshed by its own reads.
func TestGrandchildrenRefreshesOnlySeasonsThatCanChange(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{t: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	fake := &finishedSeasons{calls: map[int]int{}}
	cfg := config.Catalog{Language: "en-US", Region: "US", RefreshInterval: config.Duration(24 * time.Hour)}
	c, err := catalog.New(store.OpenTest(t), fake, cfg, slog.New(slog.DiscardHandler), catalog.WithNow(clock.now))
	if err != nil {
		t.Fatal(err)
	}
	show := core.ShowKey(800)
	want := []core.RatingKey{core.EpisodeKey(800, 1, 1), core.EpisodeKey(800, 1, 2), core.EpisodeKey(800, 2, 1), core.EpisodeKey(800, 3, 1), core.EpisodeKey(800, 3, 2)}
	read := func(step string, fetches map[int]int) {
		t.Helper()
		eps, err := c.Grandchildren(ctx, show)
		if err != nil {
			t.Fatal(err)
		}
		if got := episodeKeys(eps); !sameKeys(got, want) {
			t.Fatalf("%s: episodes %v, want %v", step, got, want)
		}
		if got := fake.fetched(); fmt.Sprint(got) != fmt.Sprint(fetches) {
			t.Fatalf("%s: season fetches %v, want %v", step, got, fetches)
		}
	}
	read("cold", map[int]int{1: 1, 2: 1, 3: 1})
	read("fresh", map[int]int{})
	clock.set(clock.now().Add(25 * time.Hour))
	read("stale", map[int]int{2: 1, 3: 1})
	read("refreshed", map[int]int{})
	// Opening the finished season itself still refreshes it.
	if _, err := c.Children(ctx, core.SeasonKey(800, 1)); err != nil {
		t.Fatal(err)
	}
	if got := fake.fetched(); fmt.Sprint(got) != fmt.Sprint(map[int]int{1: 1}) {
		t.Fatalf("season page fetches %v, want season 1", got)
	}
}
