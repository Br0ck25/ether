package resolver

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

// windowStep is one fake candidate: how long its NZB grab plus health check
// takes and how it ends. A nil err with healthy false is "unhealthy".
type windowStep struct {
	id      string
	name    string // parsed for quality tier and edition when set
	size    int64
	score   float64
	cost    time.Duration
	healthy bool
	err     error // core.ErrArticleMissing, a transient error, or nil
	hang    bool  // never answers; only the candidate budget ends it
}

type windowRun struct {
	winner  int
	err     error
	elapsed time.Duration
	started map[string]time.Duration
	peak    int
	trace   *Trace
}

// runWindow validates steps in rank order on a fake clock, recording when
// each check started and the most checks ever running at once. A positive
// cancelAfter cancels the parent context then.
func runWindow(t *testing.T, steps []windowStep, adjust func([]candidate), cancelAfter time.Duration) windowRun {
	t.Helper()
	var r windowRun
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		r.started = map[string]time.Duration{}
		inflight := 0
		byID := map[string]windowStep{}
		ids := make([]string, len(steps))
		for i, st := range steps {
			byID[st.id], ids[i] = st, st.id
		}
		start := time.Now()
		h := windowHealth(func(ctx context.Context, id string) (bool, error) {
			mu.Lock()
			r.started[id] = time.Since(start)
			inflight++
			r.peak = max(r.peak, inflight)
			mu.Unlock()
			defer func() { mu.Lock(); inflight--; mu.Unlock() }()
			st := byID[id]
			if st.hang {
				<-ctx.Done()
				return false, ctx.Err()
			}
			select {
			case <-time.After(st.cost):
			case <-ctx.Done():
				return false, ctx.Err()
			}
			return st.healthy, st.err
		})
		s := New(&fakeIndexer{}, h, nil, testCfg(), Options{})
		defer s.Close()
		cs, tr := windowCandidates(ids...)
		for i, st := range steps {
			cs[i].rel.Score = st.score
			if st.name != "" {
				cs[i].rel.Name, cs[i].rel.Size, cs[i].rel.Info = st.name, st.size, releaseparse.Parse(st.name)
			}
		}
		if adjust != nil {
			adjust(cs)
		}
		ctx := context.Background()
		if cancelAfter > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, cancelAfter)
			defer cancel()
		}
		r.winner, _, r.err = s.validateCandidates(ctx, cs, tr, 0)
		r.elapsed = time.Since(start)
		r.trace = tr
		mu.Lock()
		defer mu.Unlock()
		if inflight != 0 {
			t.Errorf("%d checks still running after validation returned", inflight)
		}
	})
	return r
}

// gotS01E01 replays resolver_history 285 (Game of Thrones S01E01, 4K,
// 2026-10-21): the attempted releases' real names (parsed for tier and
// edition), sizes and scores, in rank order. The trace's compatibility
// notes carry no client bitrate cap, so every bandwidth class is 0 and
// rank is score order; torrent results sat outside the routing margin.
// The third is HDR10-only, so it is not equivalent to a Dolby Vision lead.
// Costs are the NZB fetch plus health durations. 660f7 spent 5.72s on its
// grab and hit the 8s candidate budget mid-check; the winner, ranked last,
// took 5.24s + 2.32s.
func gotS01E01() []windowStep {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	missing := core.ErrArticleMissing
	const (
		got    = "Game.of.Thrones.S01E01.Winter.Is.Coming.2160p."
		gotLow = "Game.of.Thrones.S01E01.Winter.is.Coming.2160p."
		remux  = "TrueHD.Atmos.7.1.DV.HEVC.REMUX-FraMeSToR"
	)
	return []windowStep{
		{id: "ac8a2", name: got + "HMAX.WEB-DL.DDP5.1.Atmos.Hybrid.DV.HDR.H.265-NoOn", size: 10335495000, score: 141.18, cost: ms(1626 + 698), err: missing},
		{id: "d1deb", name: got + "MAX.WEB-DL.DD.5.1.Atmos.DoVi.HDR.H.265-playWEB", size: 10107841000, score: 133.78, cost: ms(1415 + 520), err: missing},
		{id: "08e0a", name: got + "MAX.WEB-DL.DDP5.1.Atmos.HDR.H.265", size: 10107838000, score: 131.73, cost: ms(1782 + 331), err: missing},
		{id: "db81f", name: got + remux, size: 30159501000, score: 131.08, cost: ms(2439 + 399), err: missing},
		{id: "506bd", name: got + remux + "-AsRequested", size: 31044604000, score: 130.48, cost: ms(1331 + 301), err: missing},
		{id: "660f7", name: gotLow + remux, size: 32539077468, score: 130.37, hang: true},
		{id: "a94e5", name: got + "UHD.BDRip.HEVC.10bit.DV.HDR.TrueHD.7.1.Atmos-MUSHR00M", size: 13148621000, score: 128.46, cost: ms(1182 + 300), err: missing},
		{id: "10cb0", name: got + remux, size: 29224257000, score: 127.66, cost: ms(2313 + 444), err: missing},
		{id: "53cb0", name: got + remux, size: 29568050000, score: 127.65, cost: ms(2113 + 420), err: missing},
		{id: "81456", name: gotLow + remux + "-AsRequested", size: 31044603000, score: 127.25, cost: ms(1260 + 424), err: missing},
		{id: "00f0c", name: gotLow + remux + "-AsRequested", size: 31343865000, score: 124.06, cost: ms(840 + 336), err: missing},
		{id: "c426c", name: got + remux, size: 30125072000, score: 122.39, cost: ms(2121 + 69), err: missing},
		{id: "7f793", name: gotLow + remux, size: 32536264911, score: 121.71, cost: ms(5240 + 2317), healthy: true},
	}
}

// Before widening (cc8c2e6) this replay took 18.428s, against 18.452s of
// live validation (20.824s total less 2.372s search), and the winner
// started at 10.871s. Two confirmed failures now widen the window for the
// equivalent candidates behind them, so it starts at 8.063s. Both versions
// check the same thirteen releases: nothing ranked below the winner runs.
func TestRankedWindowGameOfThronesReplay(t *testing.T) {
	steps := gotS01E01()
	r := runWindow(t, steps, nil, 0)
	if r.err != nil || r.winner != len(steps)-1 {
		t.Fatalf("winner=%d err=%v", r.winner, r.err)
	}
	if r.elapsed != 15620*time.Millisecond || r.started["7f793"] != 8063*time.Millisecond {
		t.Fatalf("validation took %s, winner started at %s; want 15.62s and 8.063s", r.elapsed, r.started["7f793"])
	}
	if r.peak != 4 || len(r.started) != len(steps) {
		t.Fatalf("peak %d checks in flight, %d started; want 4 and %d", r.peak, len(r.started), len(steps))
	}
	for i := 1; i < len(steps); i++ {
		if r.started[steps[i].id] < r.started[steps[i-1].id] {
			t.Fatalf("%s started before higher-ranked %s: %v", steps[i].id, steps[i-1].id, r.started)
		}
	}
	// The DOGnzb timeout is transient: rejected for this play only.
	if ct := r.trace.Candidates[5]; !ct.Rejected || !strings.Contains(ct.Reason, "deadline") {
		t.Fatalf("timed-out candidate trace: %+v", ct)
	}
}

func TestRankedWindowHealthyLeadStartsNoExtraWork(t *testing.T) {
	steps := []windowStep{
		{id: "lead", score: 130, cost: 1500 * time.Millisecond, healthy: true},
		{id: "backup", score: 129, hang: true},
		{id: "third", score: 128, cost: time.Second, err: core.ErrArticleMissing},
		{id: "fourth", score: 127, cost: time.Second, err: core.ErrArticleMissing},
	}
	r := runWindow(t, steps, nil, 0)
	if r.err != nil || r.winner != 0 || r.elapsed != 1500*time.Millisecond {
		t.Fatalf("winner=%d err=%v elapsed=%s", r.winner, r.err, r.elapsed)
	}
	if len(r.started) != 2 || r.peak != 2 {
		t.Fatalf("speculative checks beyond the backup: %v", r.started)
	}
}

// Ten candidates each failing after a second: confirmed failures widen the
// window, timeouts and other provider errors never do.
func TestRankedWindowWidensOnlyOnConfirmedFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		peak int
	}{
		{"missing articles", core.ErrArticleMissing, wideWidthForTest},
		{"corrupt articles", core.ErrArticleCorrupt, wideWidthForTest},
		{"unhealthy sample", nil, wideWidthForTest},
		{"timeout", context.DeadlineExceeded, 2},
		{"provider error", errors.New("indexer: 429 too many requests"), 2},
		{"authentication", errors.New("nntp: 481 authentication failed"), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := make([]windowStep, 10)
			for i := range steps {
				steps[i] = windowStep{id: string(rune('a' + i)), score: 130 - float64(i), cost: time.Second, err: tc.err}
			}
			r := runWindow(t, steps, nil, 0)
			if r.winner != -1 || r.peak != tc.peak || len(r.started) != len(steps) {
				t.Fatalf("winner=%d peak=%d started=%v", r.winner, r.peak, r.started)
			}
		})
	}
}

const wideWidthForTest = 4

// After two confirmed failures, widening skips nothing and never lends a
// slot to a release that could not overtake the lead; the lead still wins.
func TestRankedWindowWidensOnlyForEquivalents(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*candidate)
	}{
		{"equivalent", func(*candidate) {}},
		{"score outside margin", func(c *candidate) { c.rel.Score = 60 }},
		{"lower quality", func(c *candidate) { c.rel.Info.Height = 720 }},
		{"different edition", func(c *candidate) { c.rel.Info.Edition = "extended" }},
		{"worse bandwidth fit", func(c *candidate) { c.bandwidth = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []windowStep{
				{id: "dead1", score: 130, cost: 500 * time.Millisecond, err: core.ErrArticleMissing},
				{id: "dead2", score: 129, cost: 500 * time.Millisecond, err: core.ErrArticleMissing},
				{id: "lead", score: 128, cost: 3 * time.Second, healthy: true},
				{id: "d", score: 127, hang: true},
				{id: "e", score: 126, hang: true},
				{id: "f", score: 125, hang: true},
				{id: "g", score: 124, hang: true},
			}
			r := runWindow(t, steps, func(cs []candidate) {
				for i := 3; i < len(cs); i++ {
					tc.change(&cs[i])
				}
			}, 0)
			if r.err != nil || r.winner != 2 || r.elapsed != 3500*time.Millisecond {
				t.Fatalf("winner=%d err=%v elapsed=%s", r.winner, r.err, r.elapsed)
			}
			want := []string{"dead1", "dead2", "lead", "d"}
			if tc.name == "equivalent" {
				want = append(want, "e", "f")
			}
			if len(r.started) != len(want) || r.peak != min(len(want)-2, wideWidthForTest) {
				t.Fatalf("started %v (peak %d), want %v", r.started, r.peak, want)
			}
			for _, id := range want {
				if _, ok := r.started[id]; !ok {
					t.Fatalf("started %v, want %v", r.started, want)
				}
			}
		})
	}
}

func TestRankedWindowWideningStopsAtHealthyBackup(t *testing.T) {
	steps := []windowStep{
		{id: "dead1", score: 130, cost: 500 * time.Millisecond, err: core.ErrArticleMissing},
		{id: "dead2", score: 129, cost: 500 * time.Millisecond, err: core.ErrArticleMissing},
		{id: "lead", score: 128, hang: true},
		{id: "backup", score: 127, cost: 100 * time.Millisecond, healthy: true},
		{id: "dead3", score: 126, cost: time.Second, err: core.ErrArticleMissing},
		{id: "dead4", score: 125, cost: time.Second, err: core.ErrArticleMissing},
		{id: "unneeded", score: 124, cost: time.Second, healthy: true},
	}
	r := runWindow(t, steps, nil, 0)
	// The backup wins when the lead's grace ends, not before.
	if r.err != nil || r.winner != 3 || r.elapsed != 500*time.Millisecond+rankedSourceGrace {
		t.Fatalf("winner=%d err=%v elapsed=%s", r.winner, r.err, r.elapsed)
	}
	if _, ok := r.started["unneeded"]; ok || len(r.started) != 6 {
		t.Fatalf("checks after a healthy backup: %v", r.started)
	}
}

// A stalled lead's equivalent extra counts against the widened window, so
// stalls never push it past four checks.
func TestRankedWindowWideningCapsStalledExtras(t *testing.T) {
	steps := []windowStep{
		{id: "dead1", score: 130, cost: 100 * time.Millisecond, err: core.ErrArticleMissing},
		{id: "dead2", score: 129, cost: 100 * time.Millisecond, err: core.ErrArticleMissing},
	}
	for i := range 20 {
		steps = append(steps, windowStep{id: string(rune('a' + i)), score: 128 - float64(i), hang: true})
	}
	r := runWindow(t, steps, nil, 0)
	if r.winner != -1 || !errors.Is(r.err, context.DeadlineExceeded) || r.peak != wideWidthForTest {
		t.Fatalf("winner=%d err=%v peak=%d", r.winner, r.err, r.peak)
	}
	// Each wave of four waits out the 8s candidate budget within the 25s
	// validation budget: four waves, the last cut short.
	if len(r.started) != 2+4*wideWidthForTest || r.elapsed != validationBudget {
		t.Fatalf("started %d checks in %s: %v", len(r.started), r.elapsed, r.started)
	}
	for _, ct := range r.trace.Candidates[2:] {
		if ct.Rejected && !strings.Contains(ct.Reason, "deadline") {
			t.Fatalf("stalled check marked bad: %+v", ct)
		}
	}
}

func TestRankedWindowWidenedCancellationJoins(t *testing.T) {
	steps := []windowStep{
		{id: "dead1", score: 130, cost: 100 * time.Millisecond, err: core.ErrArticleMissing},
		{id: "dead2", score: 129, cost: 100 * time.Millisecond, err: core.ErrArticleMissing},
		{id: "c", score: 128, hang: true},
		{id: "d", score: 127, hang: true},
		{id: "e", score: 126, hang: true},
		{id: "f", score: 125, hang: true},
		{id: "unstarted", score: 124, hang: true},
	}
	// runWindow fails the test if any check outlives validation.
	r := runWindow(t, steps, nil, time.Second)
	if r.winner != -1 || !errors.Is(r.err, context.DeadlineExceeded) || r.elapsed != time.Second || r.peak != 4 {
		t.Fatalf("winner=%d err=%v elapsed=%s peak=%d", r.winner, r.err, r.elapsed, r.peak)
	}
	for _, ct := range r.trace.Candidates[2:] {
		if ct.Rejected {
			t.Fatalf("cancelled check rejected release: %+v", ct)
		}
	}
	if r.trace.Candidates[6].Attempted {
		t.Fatal("fifth check started in a widened window")
	}
}
