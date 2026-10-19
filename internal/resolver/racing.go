package resolver

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type candidate struct {
	rel   core.Release
	trace int
}
type validation struct {
	index                         int
	nzb                           []byte
	err                           error
	healthy, healthChecked        bool
	fetchDuration, healthDuration time.Duration
}

// Close cancels and joins optimistic checks. Owners should close before the DB.
func (s *Service) Close() error {
	s.bgMu.Lock()
	s.bgClosed = true
	s.bgCancel()
	s.bgMu.Unlock()
	s.bgWG.Wait()
	return nil
}
func (s *Service) optimisticCheck(r core.Release, nzb []byte) {
	if s.health == nil {
		return
	}
	s.bgMu.Lock()
	if s.bgClosed {
		s.bgMu.Unlock()
		return
	}
	s.bgWG.Add(1)
	s.bgMu.Unlock()
	go func() {
		defer s.bgWG.Done()
		ctx, cancel := context.WithTimeout(s.bgCtx, 30*time.Second)
		defer cancel()
		ok, err := s.checkHealth(ctx, r.ID, nzb)
		if ctx.Err() != nil {
			return
		}
		if !ok || err != nil {
			_ = s.MarkBad(ctx, r.ID, "optimistic background health check failed")
		}
	}()
}

func (s *Service) resolution(key core.RatingKey, cands []candidate, winner int, nzb []byte, tr *Trace) *core.Resolution {
	res := &core.Resolution{Title: key, Release: cands[winner].rel, NZB: nzb, ResolvedAt: s.now()}
	for i, c := range cands {
		if i != winner && !tr.Candidates[c.trace].Rejected && len(res.Alternates) < s.maxCandidates() {
			res.Alternates = append(res.Alternates, c.rel)
		}
	}
	return res
}

// validateCandidates races bounded groups when enabled. Ranked mode still starts
// two checks together, but returns as soon as the preferred result succeeds.
// Every worker is cancelled and joined before this function returns.
func (s *Service) validateCandidates(ctx context.Context, cands []candidate, tr *Trace, race int) (int, []byte, error) {
	width := 2
	if race > 0 {
		width = min(max(race, 1), 4)
	}
	limit := min(len(cands), maxHealthAttempts)
	for base := 0; base < limit; base += width {
		count := min(width, limit-base)
		workCtx, cancel := context.WithCancel(ctx)
		results := make(chan validation, count)
		ordered := make([]chan validation, count)
		var wg sync.WaitGroup
		for j := 0; j < count; j++ {
			ordered[j] = make(chan validation, 1)
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				i := base + j
				v := validation{index: i}
				phase := time.Now()
				v.nzb, v.err = s.indexer.FetchNZB(workCtx, cands[i].rel)
				v.fetchDuration = time.Since(phase)
				if v.err == nil {
					v.healthy = true
					if s.health != nil {
						v.healthChecked = true
						phase = time.Now()
						v.healthy, v.err = s.checkHealth(workCtx, cands[i].rel.ID, v.nzb)
						v.healthDuration = time.Since(phase)
					}
				}
				ordered[j] <- v
				results <- v
			}(j)
		}
		winner := -1
		var nzb []byte
		for j := 0; j < count; j++ {
			var v validation
			if race > 0 {
				v = <-results
			} else {
				v = <-ordered[j]
			}
			ct := &tr.Candidates[cands[v.index].trace]
			ct.FetchDuration = v.fetchDuration
			ct.HealthDuration = v.healthDuration
			ct.HealthChecked = v.healthChecked
			ct.Healthy = v.healthy && v.err == nil
			if ctx.Err() != nil {
				cancel()
				wg.Wait()
				return -1, nil, ctx.Err()
			}
			if ct.Healthy {
				s.recordReputation(ctx, cands[v.index].rel.Indexer, true)
				winner = v.index
				nzb = v.nzb
				break
			}
			ct.Rejected = true
			if v.err != nil {
				ct.Reason = v.err.Error()
			} else {
				ct.Reason = "health: unhealthy"
			}
			s.recordReputation(ctx, cands[v.index].rel.Indexer, false)
		}
		cancel()
		wg.Wait()
		if winner >= 0 {
			return winner, nzb, nil
		}
	}
	return -1, nil, nil
}

// compatibilityScore puts bandwidth-fit direct play ahead of a large source
// that would require a video transcode, while leaving both as failover choices.
func compatibilityScore(r core.Release, c core.ClientCaps, t *core.Title) (float64, string) {
	score := 0.0
	var notes []string
	if r.Info.AudioCodec != "" && len(c.AudioCodecs) > 0 {
		if c.SupportsAudio(r.Info.AudioCodec) {
			score += 10
			notes = append(notes, "audio copies")
		} else {
			score -= 15
			notes = append(notes, "audio transcode required")
		}
	}
	if c.Remote && c.MaxBitrateKbps > 0 && r.Size > 0 && t.Runtime > 0 {
		kbps := float64(r.Size) * 8 / t.Runtime.Seconds() / 1000
		// NZB sizes include archive overhead; reserve 10% for instantaneous peaks.
		if kbps*1.1 <= float64(c.MaxBitrateKbps) {
			score += 200
			notes = append(notes, fmt.Sprintf("estimated %.0f kbps fits remote cap", kbps))
		} else {
			score -= 200
			notes = append(notes, fmt.Sprintf("estimated %.0f kbps exceeds remote cap", kbps))
		}
	}
	if r.Info.Atmos && c.SupportsAudio(r.Info.AudioCodec) {
		score += 5
	}
	return score, strings.Join(notes, "; ")
}
