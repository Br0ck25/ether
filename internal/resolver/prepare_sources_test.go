package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type preparingIndexer struct {
	fakeIndexer
	calls   int
	count   int
	signals cachedSet
}

func (p *preparingIndexer) PrepareSources(ctx context.Context, releases []core.Release) []core.Release {
	p.calls++
	p.count = len(releases)
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 2500*time.Millisecond {
		panic("unbounded preparation")
	}
	out := append([]core.Release(nil), releases...)
	for i := range out {
		out[i].InfoHash = hashA
		out[i].Name = "must not replace ranked metadata"
	}
	p.signals[hashA] = true
	return out
}
func TestPrepareSourcesBoundsShortlistAndKeepsRankedMetadata(t *testing.T) {
	cfg := testCfg()
	cfg.Resolver.SourcePreference = PreferDebrid
	idx := &preparingIndexer{signals: cachedSet{}}
	svc := New(idx, fakeHealth{}, nil, cfg, Options{})
	svc.SetSourceSignals(idx.signals)
	releases := routingReleases("The.Signal.1999.1080p.WEB-DL.DDP5.1.H.264-OTHER")
	cands := []candidate{{rel: releases[0]}}
	for i := range 20 {
		r := releases[1]
		r.ID = string(rune('a' + i))
		r.InfoHash = ""
		cands = append(cands, candidate{rel: r})
	}
	got := svc.prepareSources(t.Context(), cands, routingTitle(false), nil)
	if idx.calls != 1 || idx.count != 12 {
		t.Fatalf("shortlist: calls=%d count=%d", idx.calls, idx.count)
	}
	if got[0].rel.Protocol != core.ProtocolTorrent || got[0].rel.InfoHash != hashA || got[0].rel.Name != releases[1].Name {
		t.Fatalf("prepared candidate lost identity/ranking: %+v", got[0])
	}
	if got[1].rel.ID != "usenet" {
		t.Fatal("lost independent Usenet hedge")
	}
}
func TestPrepareSourcesSkipsVerifiedPreferredSource(t *testing.T) {
	cfg := testCfg()
	cfg.Resolver.SourcePreference = PreferDebrid
	idx := &preparingIndexer{signals: cachedSet{}}
	svc := New(idx, fakeHealth{}, nil, cfg, Options{})
	releases := routingReleases("The.Signal.1999.1080p.WEB-DL.DDP5.1.H.264-OTHER")
	svc.prepareSources(t.Context(), []candidate{{rel: releases[1], verified: true}, {rel: releases[0]}}, routingTitle(false), nil)
	if idx.calls != 0 {
		t.Fatal("repeated descriptor work for a verified debrid source")
	}
}

func TestPrepareSourcesDoesNotDelayKnownCachedLeader(t *testing.T) {
	cfg := testCfg()
	cfg.Resolver.SourcePreference = PreferDebrid
	idx := &preparingIndexer{signals: cachedSet{hashA: true}}
	svc := New(idx, fakeHealth{}, nil, cfg, Options{})
	svc.SetSourceSignals(idx.signals)
	releases := routingReleases("The.Signal.1999.1080p.WEB-DL.DDP5.1.H.264-OTHER")
	lead := releases[1]
	lead.InfoHash = hashA
	other := lead
	other.ID, other.InfoHash = "unknown", ""
	cands := []candidate{{rel: lead}, {rel: other}, {rel: releases[0]}}
	got := svc.prepareSources(t.Context(), cands, routingTitle(false), nil)
	if idx.calls != 0 || got[0].rel.ID != lead.ID || got[1].rel.InfoHash != "" {
		t.Fatal("cached leader waited for unrelated descriptor preparation")
	}
}

func TestTorrentOnlyRoutingSkipsUnneededDiscovery(t *testing.T) {
	for _, mode := range []string{PreferAuto, PreferDebrid, PreferUsenet} {
		t.Run(mode, func(t *testing.T) {
			cfg := testCfg()
			cfg.Resolver.SourcePreference = mode
			idx := &preparingIndexer{signals: cachedSet{hashA: true}}
			svc := New(idx, fakeHealth{}, nil, cfg, Options{})
			defer svc.Close()
			svc.SetSourceSignals(idx.signals)
			ready := routingReleases("The.Signal.1999.1080p.WEB-DL.DDP5.1.H.264-OTHER")[1]
			ready.Score = 100
			ready.Info.Height = 1080
			unknown := ready
			unknown.ID, unknown.InfoHash, unknown.Score = "unknown", "", 101
			middle, last := unknown, unknown
			middle.ID, last.ID = "middle", "last"
			cands := []candidate{{rel: unknown, trace: 0}, {rel: middle, trace: 1}, {rel: ready, trace: 2}, {rel: last, trace: 3}}
			tr := &Trace{Candidates: make([]CandidateTrace, len(cands))}
			routed := svc.routeSources(cands, routingTitle(false), tr)
			got := svc.prepareSources(t.Context(), routed, routingTitle(false), tr)
			if idx.calls != 0 || len(got) != 4 || got[0].rel.ID != ready.ID || got[1].rel.ID != unknown.ID || got[2].rel.ID != middle.ID || got[3].rel.ID != last.ID {
				t.Fatalf("ready torrent waited for discovery: calls=%d candidates=%+v", idx.calls, got)
			}
			if cands[0].rel.ID != unknown.ID || tr.Candidates[2].Route == "" {
				t.Fatal("routing mutated its input or omitted its explanation")
			}
		})
	}
}

func TestTorrentOnlyReadyRoutingPreservesConstraints(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]candidate)
	}{
		{"verified", func(cs []candidate) { cs[0].verified = true }},
		{"quality", func(cs []candidate) { cs[1].rel.Info.Height = 720 }},
		{"edition", func(cs []candidate) { cs[1].rel.Info.Edition = "extended" }},
		{"score", func(cs []candidate) { cs[1].rel.Score = 50 }},
		{"bandwidth", func(cs []candidate) { cs[1].bandwidth = -1 }},
		{"availability", func(cs []candidate) { cs[1].rel.InfoHash = "unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(nil, fakeHealth{}, nil, testCfg(), Options{})
			defer svc.Close()
			svc.SetSourceSignals(cachedSet{hashA: true})
			ready := routingReleases("The.Signal.1999.1080p.WEB-DL.DDP5.1.H.264-OTHER")[1]
			ready.Score = 100
			ready.Info.Height = 1080
			unknown := ready
			unknown.ID, unknown.InfoHash = "unknown", ""
			cs := []candidate{{rel: unknown}, {rel: ready}}
			tc.change(cs)
			if got := svc.routeSources(cs, routingTitle(true), nil); got[0].rel.ID != unknown.ID {
				t.Fatalf("routing violated %s constraint", tc.name)
			}
		})
	}
}

func TestTorrentOnlyCachedAnswerStillRequiresHealth(t *testing.T) {
	ready := routingReleases("The.Signal.1999.1080p.WEB-DL.DDP5.1.H.264-OTHER")[1]
	ready.Score, ready.Info.Height = 100, 1080
	unknown := ready
	unknown.ID, unknown.InfoHash, unknown.Score = "unknown", "", 101
	idx := &preparingIndexer{signals: cachedSet{hashA: true}}
	svc := New(idx, fakeHealth{unhealthy: map[string]bool{ready.ID: true}}, nil, testCfg(), Options{})
	defer svc.Close()
	svc.SetSourceSignals(idx.signals)
	tr := &Trace{Candidates: make([]CandidateTrace, 2)}
	cs := svc.routeSources([]candidate{{rel: unknown, trace: 0}, {rel: ready, trace: 1}}, routingTitle(false), tr)
	cs = svc.prepareSources(t.Context(), cs, routingTitle(false), tr)
	winner, _, err := svc.validateCandidates(t.Context(), cs, tr, 0)
	if err != nil || winner < 0 || cs[winner].rel.ID != unknown.ID || !tr.Candidates[1].Rejected || !tr.Candidates[0].Healthy {
		t.Fatalf("stale cache answer bypassed validation/fallback: winner=%d err=%v trace=%+v", winner, err, tr)
	}
}
