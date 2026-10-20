package resolver

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type overlappingIndexer struct {
	fakeIndexer
	mu             sync.Mutex
	fetches        map[string]int
	prepare        func(context.Context)
	torrentMissing bool
}

func (p *overlappingIndexer) PrepareSources(ctx context.Context, releases []core.Release) []core.Release {
	p.prepare(ctx)
	return releases
}

func (p *overlappingIndexer) FetchNZB(ctx context.Context, rel core.Release) ([]byte, error) {
	p.mu.Lock()
	p.fetches[rel.ID]++
	p.mu.Unlock()
	if p.torrentMissing && rel.Protocol == core.ProtocolTorrent {
		return nil, core.ErrNotCached
	}
	return []byte("nzb:" + rel.ID), nil
}

type overlapHealth struct {
	run func(context.Context, []byte) (bool, error)
}

func (h overlapHealth) Check(ctx context.Context, nzb []byte) (bool, error) { return h.run(ctx, nzb) }

func overlapReleases() []core.Release {
	u := rel("usenet", "The.Signal.1999.1080p.WEB-DL.DDP5.1.H.264-U", 4000)
	d := rel("torrent", "The.Signal.1999.1080p.WEB-DL.DDP5.1.H.264-D", 4000)
	d.Protocol, d.InfoHash = core.ProtocolTorrent, hashA
	return []core.Release{u, d}
}

func TestUsenetHedgeOverlapsDiscoveryAndIsNotFetchedTwice(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	idx := &overlappingIndexer{fakeIndexer: fakeIndexer{releases: overlapReleases()}, fetches: map[string]int{}, torrentMissing: true}
	idx.prepare = func(ctx context.Context) {
		select {
		case <-started:
			close(release)
		case <-ctx.Done():
		}
	}
	health := overlapHealth{func(ctx context.Context, nzb []byte) (bool, error) {
		if strings.Contains(string(nzb), "usenet") {
			close(started)
			select {
			case <-release:
				return true, nil
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		return true, nil
	}}
	cfg := testCfg()
	cfg.Resolver.SourcePreference = PreferDebrid
	s := New(idx, health, nil, cfg, Options{})
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	got, err := s.Resolve(ctx, core.ResolveRequest{Title: matrixTitle(), Client: webCaps()})
	if err != nil || got.Release.ID != "usenet" {
		t.Fatalf("discovery and health serialized: %+v %v", got, err)
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.fetches["usenet"] != 1 || idx.fetches["torrent"] != 1 {
		t.Fatalf("prepared validation was repeated: %v", idx.fetches)
	}
}

func TestUnusedDiscoveryHedgeIsCancelledAndJoinedBeforeResolveReturns(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	idx := &overlappingIndexer{fakeIndexer: fakeIndexer{releases: overlapReleases()}, fetches: map[string]int{}}
	idx.prepare = func(ctx context.Context) {
		select {
		case <-started:
		case <-ctx.Done():
		}
	}
	health := overlapHealth{func(ctx context.Context, nzb []byte) (bool, error) {
		if strings.Contains(string(nzb), "usenet") {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return false, ctx.Err()
		}
		return true, nil
	}}
	cfg := testCfg()
	cfg.Resolver.SourcePreference = PreferDebrid
	s := New(idx, health, nil, cfg, Options{})
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	got, err := s.Resolve(ctx, core.ResolveRequest{Title: matrixTitle(), Client: webCaps()})
	if err != nil || got.Release.ID != "torrent" {
		t.Fatalf("preferred source changed: %+v %v", got, err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("unused source check leaked beyond Resolve")
	}
}

func TestDiscoveryDoesNotDelayLeaderForIneligibleTorrentTiers(t *testing.T) {
	idx := &preparingIndexer{signals: cachedSet{}}
	s := New(idx, fakeHealth{}, nil, testCfg(), Options{})
	defer s.Close()
	releases := routingReleases("The.Signal.1999.1080p.WEB-DL.DDP5.1.H.264-OTHER")
	low := releases[1]
	low.Info.Height = 720
	s.prepareSources(t.Context(), []candidate{{rel: releases[0]}, {rel: low}}, routingTitle(false), nil)
	if idx.calls != 0 {
		t.Fatal("lower-tier descriptor discovery delayed the leader")
	}
}
