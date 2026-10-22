package resolver

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type pipelinedIndexer struct {
	fakeIndexer
	first, final []core.Release
	checked      chan struct{}
	grabs        atomic.Int32
}

func (p *pipelinedIndexer) SearchWith(ctx context.Context, _ core.SearchRequest, arrived func([]core.Release)) ([]core.Release, error) {
	arrived(p.first)
	select {
	case <-p.checked:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return p.final, nil
}
func (p *pipelinedIndexer) FetchNZB(ctx context.Context, r core.Release) ([]byte, error) {
	p.grabs.Add(1)
	return []byte(r.ID), nil
}

type pipelineHealth struct {
	checked chan struct{}
	calls   atomic.Int32
}

func (h *pipelineHealth) Check(ctx context.Context, nzb []byte) (bool, error) {
	if h.calls.Add(1) == 1 {
		close(h.checked)
	}
	return true, nil
}

func TestSearchPreparationOverlapsAndReusesHealth(t *testing.T) {
	r := rel("a", "The.Signal.1999.1080p.BluRay.x264-GROUP", 6000)
	checked := make(chan struct{})
	idx := &pipelinedIndexer{first: []core.Release{r}, final: []core.Release{r}, checked: checked}
	health := &pipelineHealth{checked: checked}
	s := New(idx, health, nil, testCfg(), Options{})
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := s.Resolve(ctx, core.ResolveRequest{Title: matrixTitle(), Client: appleTVCaps()})
	if err != nil {
		t.Fatal("search waited for slow indexer before starting health", err)
	}
	if result.Release.ID != "a" || idx.grabs.Load() != 1 || health.calls.Load() != 1 {
		t.Fatalf("preparation was repeated: result=%v grabs=%d health=%d", result, idx.grabs.Load(), health.calls.Load())
	}
}

func TestEarlyHealthDoesNotPromoteLowerQuality(t *testing.T) {
	lo := rel("lo", "The.Signal.1999.720p.BluRay.x264-GROUP", 4000)
	hi := rel("hi", "The.Signal.1999.2160p.UHD.BluRay.HEVC-GROUP", 20000)
	checked := make(chan struct{})
	idx := &pipelinedIndexer{first: []core.Release{lo}, final: []core.Release{lo, hi}, checked: checked}
	s := New(idx, &pipelineHealth{checked: checked}, nil, testCfg(), Options{})
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := s.Resolve(ctx, core.ResolveRequest{Title: matrixTitle(), Client: appleTVCaps()})
	if err != nil || result.Release.ID != "hi" {
		t.Fatalf("early check changed quality policy: %v %v", result, err)
	}
}

func TestDeliveryEvidenceExpiresAndRespectsClientCap(t *testing.T) {
	now := time.Now()
	s := New(&fakeIndexer{}, nil, nil, testCfg(), Options{Now: func() time.Time { return now }})
	defer s.Close()
	r := core.Release{ID: "large", Size: 6_000_000_000}
	title := &core.Title{Runtime: 1000 * time.Second}
	for range 3 {
		s.ObserveSourceDelivery(r, 3_000_000)
		now = now.Add(3 * time.Second)
	}
	if s.deliveryFit(r, core.ClientCaps{}, title) != -1 {
		t.Fatal("6MB/s source was admitted over recent 3MB/s evidence")
	}
	r.Size = 1_000_000_000
	if s.deliveryFit(r, core.ClientCaps{}, title) != 1 {
		t.Fatal("sustainable source did not win")
	}
	if s.deliveryFit(r, core.ClientCaps{MaxBitrateKbps: 1000}, title) != -1 {
		t.Fatal("client cap bypassed")
	}
	now = now.Add(3 * time.Minute)
	if s.deliveryFit(r, core.ClientCaps{}, title) != 0 {
		t.Fatal("stale throughput restricted future playback")
	}
}

// These delays represent two independent waits, not a production benchmark.
// The before adapter hides StreamingIndexer, matching the old serial path.
type timedPipelineIndexer struct {
	fakeIndexer
	releases []core.Release
}

func (p *timedPipelineIndexer) Search(ctx context.Context, req core.SearchRequest) ([]core.Release, error) {
	return p.SearchWith(ctx, req, nil)
}
func (p *timedPipelineIndexer) SearchWith(ctx context.Context, _ core.SearchRequest, arrived func([]core.Release)) ([]core.Release, error) {
	if arrived != nil {
		arrived(p.releases)
	}
	select {
	case <-time.After(150 * time.Millisecond):
		return p.releases, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (p *timedPipelineIndexer) FetchNZB(ctx context.Context, r core.Release) ([]byte, error) {
	return []byte(r.ID), nil
}

type serialPipelineIndexer struct{ core.Indexer }
type timedPipelineHealth struct{}

func (timedPipelineHealth) Check(ctx context.Context, _ []byte) (bool, error) {
	select {
	case <-time.After(150 * time.Millisecond):
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}
func TestMeasuredSearchPipeline(t *testing.T) {
	var times [2]time.Duration
	for i := range 2 {
		idx := &timedPipelineIndexer{releases: []core.Release{rel("a", "The.Signal.1999.1080p.BluRay.x264-GROUP", 6000)}}
		var source core.Indexer = idx
		if i == 0 {
			source = serialPipelineIndexer{idx}
		}
		s := New(source, timedPipelineHealth{}, nil, testCfg(), Options{})
		start := time.Now()
		result, err := s.Resolve(t.Context(), core.ResolveRequest{Title: matrixTitle(), Client: appleTVCaps()})
		times[i] = time.Since(start)
		s.Close()
		if err != nil || result.Release.ID != "a" {
			t.Fatalf("selection changed: %v %v", result, err)
		}
	}
	t.Logf("controlled 150ms discovery + 150ms validation: serial=%s pipelined=%s", times[0], times[1])
}
