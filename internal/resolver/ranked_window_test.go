package resolver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type windowHealth func(context.Context, string) (bool, error)

func (h windowHealth) Check(ctx context.Context, nzb []byte) (bool, error) {
	return h(ctx, strings.TrimPrefix(string(nzb), "nzb:"))
}

func windowCandidates(ids ...string) ([]candidate, *Trace) {
	cs := make([]candidate, len(ids))
	for i, id := range ids {
		cs[i] = candidate{rel: core.Release{ID: id, Score: 100, Info: core.ReleaseInfo{Height: 1080}}, trace: i}
	}
	return cs, &Trace{Candidates: make([]CandidateTrace, len(ids))}
}

func TestRankedWindowReplacesFailedCheckImmediately(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		thirdStarted := make(chan struct{})
		slowCancelled := make(chan struct{})
		h := windowHealth(func(ctx context.Context, id string) (bool, error) {
			switch id {
			case "failed":
				return false, core.ErrArticleMissing
			case "slow":
				<-ctx.Done()
				close(slowCancelled)
				return false, ctx.Err()
			case "third":
				close(thirdStarted)
				return true, nil
			default:
				t.Errorf("unexpected speculative check: %s", id)
				return true, nil
			}
		})
		s := New(&fakeIndexer{}, h, nil, testCfg(), Options{})
		defer s.Close()
		cs, tr := windowCandidates("failed", "slow", "third", "unneeded")
		start := time.Now()
		winner, _, err := s.validateCandidates(context.Background(), cs, tr, 0)
		if err != nil || winner != 2 {
			t.Fatalf("winner=%d err=%v", winner, err)
		}
		if elapsed := time.Since(start); elapsed != rankedSourceGrace {
			t.Fatalf("fallback took %s, want %s rather than the stalled candidate budget", elapsed, rankedSourceGrace)
		}
		select {
		case <-thirdStarted:
		default:
			t.Fatal("third candidate never started")
		}
		select {
		case <-slowCancelled:
		default:
			t.Fatal("stalled check not joined")
		}
		if !tr.Candidates[0].Rejected || !tr.Candidates[2].Healthy || tr.Candidates[1].Rejected || tr.Candidates[3].Attempted {
			t.Fatalf("incorrect trace: %+v", tr.Candidates)
		}
	})
}

func TestRankedWindowKeepsOwnerPreference(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*candidate)
	}{
		{"equivalent during grace", func(*candidate) {}},
		{"score outside margin", func(c *candidate) { c.rel.Score = 60 }},
		{"lower quality", func(c *candidate) { c.rel.Info.Height = 720 }},
		{"different edition", func(c *candidate) { c.rel.Info.Edition = "extended" }},
		{"worse bandwidth fit", func(c *candidate) { c.bandwidth = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				delay := 5 * time.Second
				if tc.name == "equivalent during grace" {
					delay = time.Second
				}
				h := windowHealth(func(ctx context.Context, id string) (bool, error) {
					if id == "preferred" {
						select {
						case <-time.After(delay):
						case <-ctx.Done():
							return false, ctx.Err()
						}
					}
					return true, nil
				})
				s := New(&fakeIndexer{}, h, nil, testCfg(), Options{})
				defer s.Close()
				cs, tr := windowCandidates("preferred", "backup", "unneeded")
				tc.change(&cs[1])
				start := time.Now()
				winner, _, err := s.validateCandidates(context.Background(), cs, tr, 0)
				if err != nil || winner != 0 || time.Since(start) != delay {
					t.Fatalf("winner=%d err=%v elapsed=%s", winner, err, time.Since(start))
				}
				if tr.Candidates[2].Attempted {
					t.Fatal("extra check launched after healthy backup")
				}
			})
		})
	}
}

func TestRankedWindowVerifiedLeadRunsAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := windowHealth(func(ctx context.Context, id string) (bool, error) {
			if id != "verified" {
				t.Errorf("speculative check alongside verified source: %s", id)
			}
			time.Sleep(3 * time.Second)
			return true, nil
		})
		s := New(&fakeIndexer{}, h, nil, testCfg(), Options{})
		defer s.Close()
		cs, tr := windowCandidates("verified", "backup")
		cs[0].verified = true
		winner, _, err := s.validateCandidates(context.Background(), cs, tr, 0)
		if winner != 0 || err != nil || tr.Candidates[1].Attempted {
			t.Fatalf("winner=%d err=%v trace=%+v", winner, err, tr)
		}
	})
}

func TestRankedWindowCancellationJoinsWorkers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stopped := make(chan string, 2)
		h := windowHealth(func(ctx context.Context, id string) (bool, error) {
			<-ctx.Done()
			stopped <- id
			return false, ctx.Err()
		})
		s := New(&fakeIndexer{}, h, nil, testCfg(), Options{})
		defer s.Close()
		cs, tr := windowCandidates("first", "second", "unstarted")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		winner, _, err := s.validateCandidates(ctx, cs, tr, 0)
		if winner != -1 || !errors.Is(err, context.DeadlineExceeded) || len(stopped) != 2 {
			t.Fatalf("winner=%d err=%v stopped=%d", winner, err, len(stopped))
		}
		for _, c := range tr.Candidates {
			if c.Rejected {
				t.Fatal("cancelled check rejected release")
			}
		}
	})
}

func TestRankedWindowPreservesBudgetAndTransientFailures(t *testing.T) {
	for _, transient := range []bool{false, true} {
		h := windowHealth(func(context.Context, string) (bool, error) {
			if transient {
				return false, context.DeadlineExceeded
			}
			return false, core.ErrArticleMissing
		})
		s := New(&fakeIndexer{}, h, nil, testCfg(), Options{})
		cs, tr := windowCandidates(make([]string, maxHealthAttempts+1)...)
		winner, _, err := s.validateCandidates(context.Background(), cs, tr, 0)
		s.Close()
		expected := errHealthBudget
		if transient {
			expected = context.DeadlineExceeded
		}
		if winner != -1 || !errors.Is(err, expected) || tr.Candidates[maxHealthAttempts].Attempted {
			t.Fatalf("transient=%v winner=%d err=%v", transient, winner, err)
		}
	}
}

func TestRankedWindowCountsGraceAlreadySpentDuringDiscovery(t *testing.T) {
	for _, discovery := range []time.Duration{time.Second, 3 * time.Second} {
		t.Run(discovery.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				stopped := make(chan struct{})
				h := windowHealth(func(ctx context.Context, id string) (bool, error) {
					if id == "prepared" {
						<-ctx.Done()
						close(stopped)
						return false, ctx.Err()
					}
					return true, nil
				})
				s := New(&fakeIndexer{}, h, nil, testCfg(), Options{})
				defer s.Close()
				checks := &candidateChecks{}
				defer checks.close()
				cs, tr := windowCandidates("prepared", "backup")
				start := time.Now()
				cs[0].prepared = checks.start(context.Background(), s, cs[0].rel)
				time.Sleep(discovery)
				winner, _, err := s.validateCandidates(context.Background(), cs, tr, 0)
				checks.close()
				want := max(discovery, rankedSourceGrace)
				if winner != 1 || err != nil || time.Since(start) != want {
					t.Fatalf("winner=%d err=%v total=%s want=%s", winner, err, time.Since(start), want)
				}
				select {
				case <-stopped:
				default:
					t.Fatal("prepared check not joined")
				}
			})
		})
	}
}
