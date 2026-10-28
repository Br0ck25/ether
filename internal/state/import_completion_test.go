package state

import (
	"database/sql"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestImportCompletionTracksResumeState(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, resume := range []bool{false, true} {
			name := "new"
			if existing {
				name = "existing"
			}
			if resume {
				name += "/resume"
			} else {
				name += "/complete"
			}
			t.Run(name, func(t *testing.T) {
				s, now := newTestState(t)
				key := core.MovieKey(11)
				if existing {
					if err := s.Scrobble(ctx, 1, key); err != nil {
						t.Fatal(err)
					}
					if !resume {
						if err := s.Unscrobble(ctx, 1, key); err != nil {
							t.Fatal(err)
						}
					}
				}
				*now = now.Add(time.Hour)
				r := ImportRecord{UserID: 1, Key: key, Views: 2, Duration: 100 * time.Minute, ViewedAt: *now}
				if resume {
					r.Offset = 85 * time.Minute
				}
				if err := s.ImportTransaction(ctx, func(*sql.Tx) (ImportBatch, error) { return ImportBatch{Records: []ImportRecord{r}}, nil }); err != nil {
					t.Fatal(err)
				}
				if err := s.Progress(ctx, 1, key, 95*time.Minute, r.Duration, "stopped"); err != nil {
					t.Fatal(err)
				}
				got, err := s.Get(ctx, 1, key)
				if err != nil {
					t.Fatal(err)
				}
				want := 2
				if resume {
					want = 3
				}
				if got.ViewCount != want {
					t.Fatalf("views = %d, want %d", got.ViewCount, want)
				}
			})
		}
	}
}

func TestImportKeepsNewestStateRegardlessOfRecordOrder(t *testing.T) {
	for _, newestFirst := range []bool{false, true} {
		name := "oldest_first"
		if newestFirst {
			name = "newest_first"
		}
		t.Run(name, func(t *testing.T) {
			s, now := newTestState(t)
			key := core.MovieKey(19)
			old := ImportRecord{UserID: 1, Key: key, Views: 1, Offset: 20 * time.Minute, Duration: 100 * time.Minute, Rating: 3, ViewedAt: now.Add(-48 * time.Hour)}
			latest := old
			latest.Offset, latest.Rating, latest.Views, latest.ViewedAt = 70*time.Minute, 9, 2, now.Add(-24*time.Hour)
			records := []ImportRecord{old, latest}
			if newestFirst {
				records = []ImportRecord{latest, old}
			}
			if err := s.ImportTransaction(ctx, func(*sql.Tx) (ImportBatch, error) { return ImportBatch{Records: records}, nil }); err != nil {
				t.Fatal(err)
			}
			got, err := s.Get(ctx, 1, key)
			if err != nil {
				t.Fatal(err)
			}
			if got.ViewOffset != latest.Offset || got.UserRating != latest.Rating || got.ViewCount != latest.Views || !got.LastViewedAt.Equal(latest.ViewedAt) {
				t.Fatalf("newest state lost: %+v", got)
			}
			history, err := s.History(ctx, 1, time.Time{}, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(history) != 2 {
				t.Fatalf("history count = %d, want 2", len(history))
			}
		})
	}
}
