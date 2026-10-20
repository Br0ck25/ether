package debrid

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// TorrentResolver fills a torrent release's info hash and magnet (by
// downloading its .torrent when the indexer gave neither). Implemented by
// *newznab.Manager.
type TorrentResolver interface {
	ResolveTorrent(ctx context.Context, r core.Release) (core.Release, error)
}

// Indexer wraps the aggregate indexer: torrent results are kept only while
// a debrid provider is enabled (annotated with cache availability where a
// provider offers a bulk check), and FetchNZB returns a debrid payload for
// torrents while Usenet releases go to the inner indexer unchanged.
type Indexer struct {
	inner core.Indexer
	svc   *Service
}

var _ core.Indexer = (*Indexer)(nil)

// NewIndexer wraps inner.
func NewIndexer(inner core.Indexer, svc *Service) *Indexer { return &Indexer{inner: inner, svc: svc} }

// Inner returns the wrapped indexer.
func (x *Indexer) Inner() core.Indexer { return x.inner }

// Name implements core.Indexer.
func (x *Indexer) Name() string { return x.inner.Name() }

// Search implements core.Indexer. With a TorBox account, TorBox's own
// torrent search runs alongside the configured indexers; its results carry
// TorBox's cache answer, so they need no separate availability check.
func (x *Indexer) Search(ctx context.Context, req core.SearchRequest) ([]core.Release, error) {
	type tbAnswer struct {
		rels   []core.Release
		cached map[string]bool
		ok     bool
		err    error
	}
	tbc := make(chan tbAnswer, 1)
	go func() {
		var a tbAnswer
		a.rels, a.cached, a.ok, a.err = x.svc.searchTorBox(ctx, req)
		tbc <- a
	}()
	results, err := x.inner.Search(ctx, req)
	tb := <-tbc
	if tb.err != nil {
		x.svc.log.Warn("debrid: torbox search failed", "err", tb.err)
	}
	if err != nil {
		// Indexers failing (or none configured to answer) must not hide
		// what TorBox found.
		if len(tb.rels) == 0 {
			return results, err
		}
		x.svc.log.Warn("debrid: indexer search failed; using torbox results", "err", err)
		results = nil
	}
	enabled := x.svc.Enabled()
	// cachedOnly drops torrents TorBox reports uncached: they would only be
	// skipped at play.
	cachedOnly := x.svc.Config().CachedOnly
	var torrents []string
	out := results[:0:0]
	seen := map[string]bool{}
	for _, r := range results {
		if r.Protocol != core.ProtocolTorrent {
			out = append(out, r)
			continue
		}
		if !enabled {
			continue
		}
		if r.InfoHash != "" {
			seen[r.InfoHash] = true
			if _, answered := tb.cached[r.InfoHash]; !answered {
				torrents = append(torrents, r.InfoHash)
			}
			if c, answered := tb.cached[r.InfoHash]; answered && !c && cachedOnly {
				continue
			}
		}
		out = append(out, r)
	}
	for _, r := range tb.rels {
		if seen[r.InfoHash] || (cachedOnly && !tb.cached[r.InfoHash]) {
			continue
		}
		seen[r.InfoHash] = true
		out = append(out, r)
	}
	if len(torrents) == 0 {
		sort.SliceStable(out, func(i, j int) bool { return x.cachedRank(out[i], tb.cached) < x.cachedRank(out[j], tb.cached) })
		return out, nil
	}
	// One bulk availability call per provider that offers it: torrents
	// known to be uncached everywhere are dropped (they would only burn
	// health-check attempts), and known-cached torrents move ahead of
	// unknown ones of equal rank.
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	cached, definitive := x.svc.CheckCached(cctx, torrents)
	cancel()
	queried := make(map[string]bool, len(torrents))
	for _, h := range torrents {
		queried[h] = true
	}
	kept := out[:0]
	for _, r := range out {
		if r.Protocol == core.ProtocolTorrent && queried[r.InfoHash] && definitive && !cached[r.InfoHash] {
			continue
		}
		kept = append(kept, r)
	}
	for h, c := range tb.cached {
		cached[h] = cached[h] || c
	}
	sort.SliceStable(kept, func(i, j int) bool { return x.cachedRank(kept[i], cached) < x.cachedRank(kept[j], cached) })
	return kept, nil
}

// cachedRank orders known-cached torrents ahead of everything else of
// equal rank (the resolver re-ranks; this only breaks ties).
func (x *Indexer) cachedRank(r core.Release, cached map[string]bool) int {
	if r.Protocol == core.ProtocolTorrent && (cached[r.InfoHash] || x.svc.KnownCached(r.InfoHash)) {
		return 0
	}
	return 1
}

// FetchNZB implements core.Indexer. For a torrent it returns the debrid
// payload of the release's main video file (or the wanted episode of a
// season pack); for anything else, the inner indexer's NZB.
func (x *Indexer) FetchNZB(ctx context.Context, r core.Release) ([]byte, error) {
	if r.Protocol != core.ProtocolTorrent {
		return x.inner.FetchNZB(ctx, r)
	}
	if !x.svc.Enabled() {
		return nil, fmt.Errorf("torrent release %q: %w", r.Name, ErrNoProvider)
	}
	if r.InfoHash == "" {
		tr, ok := x.inner.(TorrentResolver)
		if !ok {
			return nil, fmt.Errorf("debrid: torrent %q has no info hash: %w", r.Name, core.ErrReleaseRejected)
		}
		resolved, err := tr.ResolveTorrent(ctx, r)
		if err != nil {
			return nil, err
		}
		r = resolved
	}
	return x.svc.Acquire(ctx, r)
}
