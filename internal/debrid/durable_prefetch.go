package debrid

import (
	"context"
	"errors"
	"github.com/braedonsaunders/ether/internal/core"
)

// DurablePrefetch also persists memory hits and verifies disk presence. Ordinary
// Prefetch is intentionally best effort; a managed shelf must not call a memory
// buffer or a failed cache write a completed preload.
func (e *Engine) DurablePrefetch(ctx context.Context, id string, payload []byte, off, n int64) error {
	if !IsPayload(payload) {
		if inner, ok := e.inner.(core.DurablePrefetcher); ok {
			return inner.DurablePrefetch(ctx, id, payload, off, n)
		}
		return errors.New("debrid: durable prefetch unsupported by inner engine")
	}
	if e.cache == nil {
		return errors.New("debrid: persistent cache unavailable")
	}
	f, err := e.acquireFile(ctx, id, payload)
	if err != nil {
		return err
	}
	defer e.releaseFile(f)
	if err = f.prefetch(ctx, [][2]int64{{off, min(off+n, f.size)}}); err != nil {
		return err
	}
	for idx := off / e.block; idx*e.block < min(off+n, f.size); idx++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, ok := f.diskGet(idx); ok {
			continue
		}
		data, err := f.block(ctx, idx, fetchPrefetch, 1, ctx)
		if err != nil {
			return err
		}
		f.diskPut(idx, data)
		if _, ok := f.diskGet(idx); !ok {
			return errors.New("debrid: preload block was not retained on disk")
		}
	}
	return nil
}
