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

// Search implements core.Indexer.
func (x *Indexer) Search(ctx context.Context, req core.SearchRequest) ([]core.Release, error) {
	results, err := x.inner.Search(ctx, req)
	if err != nil {
		return results, err
	}
	enabled := x.svc.Enabled()
	var torrents []string
	out := results[:0:0]
	for _, r := range results {
		if r.Protocol != core.ProtocolTorrent {
			out = append(out, r)
			continue
		}
		if !enabled {
			continue
		}
		if r.InfoHash != "" {
			torrents = append(torrents, r.InfoHash)
		}
		out = append(out, r)
	}
	if len(torrents) == 0 {
		return out, nil
	}
	// One bulk availability call per provider that offers it: torrents
	// known to be uncached everywhere are dropped (they would only burn
	// health-check attempts), and known-cached torrents move ahead of
	// unknown ones of equal rank.
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	cached, definitive := x.svc.CheckCached(cctx, torrents)
	cancel()
	kept := out[:0]
	for _, r := range out {
		if r.Protocol == core.ProtocolTorrent && r.InfoHash != "" && definitive && !cached[r.InfoHash] {
			continue
		}
		kept = append(kept, r)
	}
	rank := func(r core.Release) int {
		if r.Protocol == core.ProtocolTorrent && (cached[r.InfoHash] || x.svc.KnownCached(r.InfoHash)) {
			return 0
		}
		return 1
	}
	sort.SliceStable(kept, func(i, j int) bool { return rank(kept[i]) < rank(kept[j]) })
	return kept, nil
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
