package resolver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestRankedHedgeReleasesHealthyEquivalentAfterGrace(t *testing.T) {
	preferred, backup := make(chan validation, 1), make(chan validation, 1)
	backup <- validation{index: 1, healthy: true}
	started := time.Now()
	v := waitRankedValidation(preferred, backup, 20*time.Millisecond)
	if v.index != 1 || time.Since(started) < 20*time.Millisecond {
		t.Fatalf("equivalent did not respect grace: %+v", v)
	}
}

func TestRankedHedgeKeepsPreferredAndRestoresBackup(t *testing.T) {
	preferred, backup := make(chan validation, 1), make(chan validation, 1)
	backup <- validation{index: 1, healthy: true}
	done := make(chan validation, 1)
	go func() { done <- waitRankedValidation(preferred, backup, time.Second) }()
	preferred <- validation{index: 0, healthy: true}
	select {
	case v := <-done:
		if v.index != 0 {
			t.Fatal("equivalent bypassed a ready preferred source")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("preferred source was held")
	}
	if v := <-backup; v.index != 1 {
		t.Fatal("backup result was lost")
	}
}

func TestRankedHedgeFailedBackupCannotCancelPreferred(t *testing.T) {
	preferred, backup := make(chan validation, 1), make(chan validation, 1)
	backup <- validation{index: 1, err: errors.New("temporary outage")}
	done := make(chan validation, 1)
	go func() { done <- waitRankedValidation(preferred, backup, 0) }()
	select {
	case <-done:
		t.Fatal("failed backup ended the preferred check")
	case <-time.After(20 * time.Millisecond):
	}
	preferred <- validation{index: 0, healthy: true}
	select {
	case v := <-done:
		if v.index != 0 {
			t.Fatal("wrong winner")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("preferred result not returned")
	}
}

func TestEquivalentHedgePreservesQualityEditionBandwidthAndScore(t *testing.T) {
	lead := candidate{rel: core.Release{Score: 100, Info: core.ReleaseInfo{Height: 2160}}, bandwidth: 1}
	if !equivalentHedge(lead, lead, 25) {
		t.Fatal("identical tier rejected")
	}
	for _, tc := range []struct {
		name   string
		change func(*candidate)
	}{
		{"height", func(c *candidate) { c.rel.Info.Height = 1080 }},
		{"hdr", func(c *candidate) { c.rel.Info.HDR10 = true }},
		{"edition", func(c *candidate) { c.rel.Info.Edition = "extended" }},
		{"bandwidth", func(c *candidate) { c.bandwidth = 0 }},
		{"score", func(c *candidate) { c.rel.Score = 74 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := lead
			tc.change(&c)
			if equivalentHedge(lead, c, 25) {
				t.Fatal("non-equivalent source bypassed preference")
			}
		})
	}
	lead.verified = true
	if equivalentHedge(lead, lead, 25) {
		t.Fatal("verified source was hedged")
	}
}

func TestRankedValidationJoinsStalledEquivalentLoser(t *testing.T) {
	h := controlledHealth{make(chan struct{}), make(chan struct{})}
	svc := New(&fakeIndexer{}, h, nil, testCfg(), Options{})
	defer svc.Close()
	lead := candidate{rel: core.Release{ID: "uhd", Score: 100, Info: core.ReleaseInfo{Height: 2160}}, trace: 0}
	backup := lead
	backup.rel.ID, backup.trace = "hd", 1
	tr := &Trace{Candidates: make([]CandidateTrace, 2)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	winner, _, err := svc.validateCandidates(ctx, []candidate{lead, backup}, tr, 0)
	if err != nil || winner != 1 {
		t.Fatalf("healthy equivalent held behind stalled source: %d %v", winner, err)
	}
	select {
	case <-h.slowCancelled:
	default:
		t.Fatal("stalled preferred validation was not cancelled and joined")
	}
	if !tr.Candidates[1].Healthy || tr.Candidates[0].Rejected {
		t.Fatal("cancelled speculative work was treated as a bad release")
	}
}
