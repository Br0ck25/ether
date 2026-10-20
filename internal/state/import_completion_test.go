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
