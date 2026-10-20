package debrid

import (
	"context"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// PrepareSources resolves a bounded, already ranked shortlist's torrent
// descriptors together, then checks cache availability in bulk. It never
// adds a torrent to an account or starts an uncached download. Unknown and
// failed lookups remain candidates for the normal validation path.
func (x *Indexer) PrepareSources(ctx context.Context, releases []core.Release) []core.Release {
	out := append([]core.Release(nil), releases...)
	if !x.svc.Enabled() {
		return out
	}
	tr, resolves := x.inner.(TorrentResolver)
	dctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(4, len(out)) {
		wg.Go(func() {
			for i := range jobs {
				if out[i].Protocol != core.ProtocolTorrent || out[i].InfoHash != "" {
					continue
				}
				if p, ok := x.svc.reusablePayload(dctx, out[i]); ok {
					out[i].InfoHash = p.InfoHash
					continue
				}
				if resolves && dctx.Err() == nil {
					if resolved, err := tr.ResolveTorrent(dctx, out[i]); err == nil {
						// Keep ranking, identity and episode target unchanged.
						out[i].InfoHash, out[i].MagnetURL = resolved.InfoHash, resolved.MagnetURL
					}
				}
			}
		})
	}
	for i := range out {
		select {
		case jobs <- i:
		case <-dctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	var hashes []string
	seen := map[string]bool{}
	for _, r := range out {
		if r.Protocol == core.ProtocolTorrent && r.InfoHash != "" && !seen[r.InfoHash] {
			hashes = append(hashes, r.InfoHash)
			seen[r.InfoHash] = true
		}
	}
	if len(hashes) > 0 && ctx.Err() == nil {
		x.svc.CheckCached(ctx, hashes)
	}
	return out
}
