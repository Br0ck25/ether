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

// earlyIndexer blocks descriptor preparation for a fixed time (or until its
// context ends) so synctest can pin exactly when resolution stops waiting
// for it.
func earlyService(releases []core.Release, prepare func(context.Context), health core.HealthChecker, pref string) (*Service, *overlappingIndexer) {
	idx := &overlappingIndexer{fakeIndexer: fakeIndexer{releases: releases}, fetches: map[string]int{}}
	idx.prepare = prepare
	cfg := testCfg()
	cfg.Resolver.SourcePreference = pref
	return New(idx, health, nil, cfg, Options{}), idx
}

func blockPrepare(stopped chan<- struct{}) func(context.Context) {
	return func(ctx context.Context) {
		<-ctx.Done()
		close(stopped)
	}
}

func stallHealth(want string, stopped chan<- string) overlapHealth {
	return overlapHealth{func(ctx context.Context, nzb []byte) (bool, error) {
		if strings.Contains(string(nzb), want) {
			<-ctx.Done()
			if stopped != nil {
				stopped <- want
			}
			return false, ctx.Err()
		}
		return true, nil
	}}
}

// A Usenet hedge that is already healthy during debrid discovery must not
// wait for the full preparation budget plus a fresh grace period: once the
// equivalent-score grace (counted from discovery start) expires, the ready
// source proceeds and preparation is cancelled and joined.
func TestReadyHedgeProceedsAtGraceInsteadOfWaitingForPreparation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		prepStopped := make(chan struct{})
		s, _ := earlyService(overlapReleases(), blockPrepare(prepStopped), stallHealth("torrent", nil), PreferDebrid)
		defer s.Close()
		start := time.Now()
		got, err := s.Resolve(context.Background(), core.ResolveRequest{Title: matrixTitle(), Client: webCaps()})
		if err != nil || got.Release.ID != "usenet" {
			t.Fatalf("ready equivalent did not proceed: %+v %v", got, err)
		}
		if elapsed := time.Since(start); elapsed != rankedSourceGrace {
			t.Fatalf("elapsed %s, want %s (grace from discovery start, not preparation budget + grace)", elapsed, rankedSourceGrace)
		}
		select {
		case <-prepStopped:
		default:
			t.Fatal("abandoned preparation not cancelled and joined before Resolve returned")
		}
	})
}

// With an explicit Usenet preference, preparation cannot promote any
// torrent over the routed Usenet lead, so a lead already proven healthy
// during discovery proceeds at once — no debrid-discovery grace at all.
func TestPreferUsenetHealthyLeadProceedsImmediately(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		prepStopped := make(chan struct{})
		s, _ := earlyService(overlapReleases(), blockPrepare(prepStopped), stallHealth("torrent", nil), PreferUsenet)
		defer s.Close()
		start := time.Now()
		got, err := s.Resolve(context.Background(), core.ResolveRequest{Title: matrixTitle(), Client: webCaps()})
		if err != nil || got.Release.ID != "usenet" {
			t.Fatalf("preferred Usenet lead did not proceed: %+v %v", got, err)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("elapsed %s, want 0 (no grace when preparation cannot change the preferred lead)", elapsed)
		}
		select {
		case <-prepStopped:
		default:
			t.Fatal("abandoned preparation not cancelled and joined before Resolve returned")
		}
	})
}

// Preparation that completes inside the grace still applies, and the
// preferred source keeps its full grace from when its own check starts:
// a torrent healthy 1.5s into validation (2.5s after discovery began)
// still beats the instantly healthy Usenet hedge.
func TestPreparationCompletingWithinGraceKeepsFullPreferredGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		prepare := func(ctx context.Context) {
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
			}
		}
		health := overlapHealth{func(ctx context.Context, nzb []byte) (bool, error) {
			if strings.Contains(string(nzb), "torrent") {
				select {
				case <-time.After(1500 * time.Millisecond):
					return true, nil
				case <-ctx.Done():
					return false, ctx.Err()
				}
			}
			return true, nil
		}}
		s, _ := earlyService(overlapReleases(), prepare, health, PreferDebrid)
		defer s.Close()
		start := time.Now()
		got, err := s.Resolve(context.Background(), core.ResolveRequest{Title: matrixTitle(), Client: webCaps()})
		if err != nil || got.Release.ID != "torrent" {
			t.Fatalf("preferred source lost its grace: %+v %v", got, err)
		}
		if elapsed := time.Since(start); elapsed != 2500*time.Millisecond {
			t.Fatalf("elapsed %s, want 2.5s (1s preparation + 1.5s preferred check)", elapsed)
		}
	})
}

// A backup that is not equivalent to the preferred source (different
// edition) never cuts preparation short: resolution waits the preparation
// budget and the preferred torrent wins.
func TestNonEquivalentReadySourceDoesNotCutPreparationShort(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		prepStopped := make(chan struct{})
		releases := overlapReleases()
		releases[0] = rel("usenet", "The.Signal.1999.Extended.Edition.1080p.WEB-DL.DDP5.1.H.264-U", 4000)
		s, _ := earlyService(releases, blockPrepare(prepStopped), fakeHealth{}, PreferDebrid)
		defer s.Close()
		start := time.Now()
		got, err := s.Resolve(context.Background(), core.ResolveRequest{Title: matrixTitle(), Client: webCaps()})
		if err != nil || got.Release.ID != "torrent" {
			t.Fatalf("non-equivalent backup displaced preferred source: %+v %v", got, err)
		}
		if elapsed := time.Since(start); elapsed != 2500*time.Millisecond {
			t.Fatalf("elapsed %s, want the full 2.5s preparation budget", elapsed)
		}
	})
}

// When the first hedge fails during discovery, the next eligible Usenet
// candidate starts immediately instead of after preparation ends, so its
// result is already there when the failed torrent lead is rejected.
func TestFailedHedgeSlidesToNextCandidateDuringPreparation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u1 := rel("usenet-a", "The.Signal.1999.1080p.WEB-DL.DDP5.1.H.264-UA", 4000)
		u2 := rel("usenet-b", "The.Signal.1999.1080p.WEB-DL.DDP5.1.H.264-UB", 4000)
		d := rel("torrent", "The.Signal.1999.1080p.WEB-DL.DDP5.1.H.264-D", 4000)
		d.Protocol, d.InfoHash = core.ProtocolTorrent, hashA
		prepare := func(ctx context.Context) {
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
			}
		}
		health := overlapHealth{func(ctx context.Context, nzb []byte) (bool, error) {
			switch {
			case strings.Contains(string(nzb), "usenet-a"):
				return false, core.ErrArticleMissing
			case strings.Contains(string(nzb), "usenet-b"):
				select {
				case <-time.After(time.Second):
					return true, nil
				case <-ctx.Done():
					return false, ctx.Err()
				}
			default:
				return true, nil
			}
		}}
		s, idx := earlyService([]core.Release{u1, u2, d}, prepare, health, PreferDebrid)
		idx.torrentMissing = true // torrent lead fails its fetch at validation
		defer s.Close()
		start := time.Now()
		got, err := s.Resolve(context.Background(), core.ResolveRequest{Title: matrixTitle(), Client: webCaps()})
		if err != nil || got.Release.ID != "usenet-b" {
			t.Fatalf("sliding hedge did not supply the winner: %+v %v", got, err)
		}
		// usenet-b's one-second check overlapped the one-second preparation;
		// starting it only after preparation would finish at 2s.
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Fatalf("elapsed %s, want 1s (second hedge overlapped preparation)", elapsed)
		}
		idx.mu.Lock()
		defer idx.mu.Unlock()
		if idx.fetches["usenet-a"] != 1 || idx.fetches["usenet-b"] != 1 {
			t.Fatalf("prepared validations repeated: %v", idx.fetches)
		}
	})
}

// Sliding hedges during a slow preparation must stay inside the ranked
// validation budget: routeSources can move at most two candidates ahead
// of a checked one, so no candidate at prep-time index
// maxHealthAttempts-2 or beyond may be fetched or health-checked.
func TestPreparationHedgesRespectValidationBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		prepStopped := make(chan struct{})
		health := overlapHealth{func(_ context.Context, _ []byte) (bool, error) {
			return false, core.ErrArticleMissing
		}}
		idx := &overlappingIndexer{fetches: map[string]int{}}
		idx.prepare = blockPrepare(prepStopped)
		s := New(idx, health, nil, testCfg(), Options{})
		defer s.Close()
		mk := func(id string, torrent bool) candidate {
			r := core.Release{ID: id, Score: 100, Info: core.ReleaseInfo{Height: 1080}}
			if torrent {
				r.Protocol = core.ProtocolTorrent
			}
			return candidate{rel: r}
		}
		cands := []candidate{mk("torrent", true)}
		for i := range maxHealthAttempts + 8 {
			cands = append(cands, mk("usenet-"+string(rune('a'+i%26))+string(rune('a'+i/26)), false))
		}
		checks := &candidateChecks{}
		s.prepareSources(context.Background(), cands, routingTitle(false), nil, checks)
		checks.close()
		idx.mu.Lock()
		defer idx.mu.Unlock()
		for i, c := range cands {
			if idx.fetches[c.rel.ID] > 0 && i >= maxHealthAttempts-2 {
				t.Fatalf("candidate %d (%s) fetched beyond the validation budget", i, c.rel.ID)
			}
			if c.prepared != nil && i >= maxHealthAttempts-2 {
				t.Fatalf("candidate %d (%s) speculatively checked beyond the validation budget", i, c.rel.ID)
			}
		}
		total := 0
		for _, n := range idx.fetches {
			total += n
		}
		if total == 0 || total > maxHealthAttempts-2 {
			t.Fatalf("speculative fetches %d, want within 1..%d", total, maxHealthAttempts-2)
		}
	})
}

// Cancelling the request during discovery returns at once; the preparation
// and the in-flight hedge are cancelled and joined before Resolve returns.
func TestCancellationDuringPreparationJoinsWorkers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		prepStopped := make(chan struct{})
		stopped := make(chan string, 2)
		s, _ := earlyService(overlapReleases(), blockPrepare(prepStopped), stallHealth("usenet", stopped), PreferDebrid)
		defer s.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		start := time.Now()
		got, err := s.Resolve(ctx, core.ResolveRequest{Title: matrixTitle(), Client: webCaps()})
		if got != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got=%+v err=%v", got, err)
		}
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Fatalf("elapsed %s, want 1s cancellation", elapsed)
		}
		select {
		case <-prepStopped:
		default:
			t.Fatal("preparation leaked beyond cancelled Resolve")
		}
		select {
		case <-stopped:
		default:
			t.Fatal("hedge check leaked beyond cancelled Resolve")
		}
	})
}
