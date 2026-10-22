package playback

import (
	"context"
	"errors"
	"slices"

	"github.com/braedonsaunders/ether/internal/core"
)

// failoverRaceWidth is how many alternates a mid-stream failover opens at
// once. Opening one is mostly waiting (an indexer fetch, a first article or
// first range, a header read), and the viewer is stalled until one works,
// so trying them one after another multiplied the stall by every broken
// alternate.
const failoverRaceWidth = 3

type raceResult struct {
	alt core.Release
	g   *generation
	err error
}

// raceAlternates opens untried alternates concurrently, keeping width in
// flight, and returns the first that opens. Releases that failed are
// returned so the caller does not try them again. Late successes are
// dropped in the background.
func (m *Manager) raceAlternates(ctx context.Context, s *session, tried []string, alternates []core.Release, fallback func(int64) core.MediaInfo, width int, accept ...func(*generation) error) (*generation, []string, error) {
	var cands []core.Release
	for _, alt := range alternates {
		if alt.ID != "" && !slices.Contains(tried, alt.ID) && releaseMatchesTitle(alt, s.title) {
			cands = append(cands, alt)
		}
	}
	if m.deps.FetchNZB == nil || len(cands) < 2 {
		return nil, nil, nil
	}
	rctx, cancel := context.WithCancel(ctx)
	results := make(chan raceResult, len(cands))
	next, running := 0, 0
	launch := func() {
		alt := cands[next]
		next++
		running++
		go func() {
			nzb, err := m.deps.FetchNZB(rctx, alt)
			if err != nil {
				results <- raceResult{alt: alt, err: err}
				return
			}
			res := &core.Resolution{Title: s.ident.key, Release: alt, NZB: nzb, ResolvedAt: m.now()}
			g, err := m.openGeneration(rctx, s, res, fallback)
			if err == nil {
				if perr := m.preflightSwap(s, g); perr != nil {
					m.dropGeneration(g)
					g, err = nil, perr
				}
			}
			if err == nil && len(accept) > 0 && accept[0] != nil {
				if e := accept[0](g); e != nil {
					m.dropGeneration(g)
					g = nil
					err = e
				}
			}
			results <- raceResult{alt: alt, g: g, err: err}
		}()
	}
	for running < width && next < len(cands) {
		launch()
	}
	var failed []string
	var lastErr error
	for running > 0 {
		r := <-results
		running--
		if r.err == nil {
			cancel() // the rest are no longer needed
			if running > 0 {
				m.drainRace(s, results, running)
			}
			return r.g, failed, nil
		}
		if ctx.Err() != nil {
			cancel()
			m.drainRace(s, results, running)
			return nil, failed, ctx.Err()
		}
		failed = append(failed, r.alt.ID)
		lastErr = r.err
		m.log.Warn("failover candidate failed", "session", s.id, "release", r.alt.Name, "err", r.err)
		if next < len(cands) {
			launch()
		}
	}
	cancel()
	return nil, failed, lastErr
}

// drainRace waits for the remaining racers, releases any that opened and
// records the ones that failed so later failovers skip them. Racers that
// were only cancelled are left untried.
func (m *Manager) drainRace(s *session, results chan raceResult, n int) {
	m.mu.Lock()
	track := !m.closed
	if track {
		m.wg.Add(1)
	}
	m.mu.Unlock()
	go func() {
		if track {
			defer m.wg.Done()
		}
		for ; n > 0; n-- {
			r := <-results
			switch {
			case r.err == nil && r.g != nil:
				m.dropGeneration(r.g)
			case r.err != nil && !errors.Is(r.err, context.Canceled):
				s.mu.Lock()
				if !slices.Contains(s.tried, r.alt.ID) {
					s.tried = append(s.tried, r.alt.ID)
				}
				s.mu.Unlock()
			}
		}
	}()
}
