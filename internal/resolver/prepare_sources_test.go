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
