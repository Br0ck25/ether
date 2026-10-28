package stream

import (
	"context"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// bulkReader owns a bounded fetch window independent of playback cursors. Repair
// can traverse a whole recovery set while the player consumes its cached prefix.
// Each half-second burst may borrow idle provider capacity; a quiet interval
// follows before another burst. The pool always favors waiting live reads.
type bulkReader struct {
	e     *Engine
	r     *release
	prio  core.Priority
	fo    fetchOpts
	mu    sync.Mutex
	cycle time.Time
}

func (b *bulkReader) options() fetchOpts {
	b.mu.Lock()
	now := time.Now()
	if b.cycle.IsZero() || now.Sub(b.cycle) >= time.Second {
		b.cycle = now
	}
	until := b.cycle.Add(500 * time.Millisecond)
	b.mu.Unlock()
	fo := b.fo
	fo.noRepair = true
	fo.parallel = 32
	fo.burstUntil = time.Time{}
	if now.Before(until) {
		fo.burstUntil = until
	}
	return fo
}

// readRange keeps a broad queue of exact range dependencies, independent of the
// ordinary reader's 4 MiB requests. Cached bytes still use the shared namespace.
func (b *bulkReader) readRange(ctx context.Context, pf *postedFile, dst []byte, off int64) (int, error) {
	return b.e.readFile(ctx, b.r, pf, dst, off, b.prio, b.options())
}

// articles pipelines a rolling 16-article window but visits in file order, so
// PAR2 packets crossing article boundaries can be scanned without buffering a
// whole recovery volume. Cancellation joins every worker.
func (b *bulkReader) articles(ctx context.Context, pf *postedFile, visit func(*segData, error) bool) {
	const window = 16
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		seg *segData
		err error
	}
	for from := 0; from < pf.numSlots(); from += window {
		if ctx.Err() != nil {
			return
		}
		n := min(window, pf.numSlots()-from)
		results := make([]chan result, n)
		var wg sync.WaitGroup
		for j := range n {
			results[j] = make(chan result, 1)
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				s, e := b.e.segmentRaw(ctx, b.r, pf, from+j, b.prio, b.options())
				results[j] <- result{s, e}
			}(j)
		}
		keep := true
		for j := range n {
			r := <-results[j]
			if keep && !visit(r.seg, r.err) {
				keep = false
				cancel()
			}
		}
		wg.Wait()
		if !keep {
			return
		}
	}
}

// PostedFileInfo reads a header through the shared cache and repair path. Bulk
// diagnostics must not refetch the same first article through a separate client.
func (e *Engine) PostedFileInfo(ctx context.Context, id string, payload []byte, index int) (core.ArticlePart, error) {
	if e.ctx.Err() != nil {
		return core.ArticlePart{}, ErrClosed
	}
	r, err := e.acquire(payload)
	if err != nil {
		return core.ArticlePart{}, err
	}
	defer e.release(r)
	if index < 0 || index >= len(r.files) {
		return core.ArticlePart{}, ErrCorrupt
	}
	pf := r.files[index]
	reader := bulkReader{e: e, r: r, prio: core.PriorityReadahead, fo: fetchOpts{cacheID: id}}
	fo := reader.options()
	fo.noRepair = false
	seg, err := e.segment(ctx, r, pf, 0, core.PriorityReadahead, fo)
	if err != nil {
		return core.ArticlePart{}, err
	}
	return core.ArticlePart{Name: seg.name, FileSize: seg.fileSize, Part: 1, Total: seg.total, Begin: seg.begin + 1, End: seg.end()}, nil
}

// ReadPostedRange is the explicit bulk reader entry point. Its window is
// independent of the streaming player's demand/read-ahead window.
func (e *Engine) ReadPostedRange(ctx context.Context, id string, payload []byte, index int, off int64, dst []byte) (int, error) {
	if e.ctx.Err() != nil {
		return 0, ErrClosed
	}
	r, err := e.acquire(payload)
	if err != nil {
		return 0, err
	}
	defer e.release(r)
	if index < 0 || index >= len(r.files) || off < 0 {
		return 0, ErrCorrupt
	}
	b := bulkReader{e: e, r: r, prio: core.PriorityReadahead, fo: fetchOpts{cacheID: id}}
	// Caller ranges may be arbitrarily large; never queue an unbounded file.
	var total int
	for total < len(dst) {
		n := min(len(dst)-total, 16<<20)
		fo := b.options()
		fo.noRepair = false
		got, err := e.readFile(ctx, r, r.files[index], dst[total:total+n], off+int64(total), b.prio, fo)
		total += got
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
