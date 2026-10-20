package debrid

import (
	"context"
	"errors"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// reusablePayload uses stable release and episode identities before a tracker
// lookup. Some indexers only return a detail URL; resolving that URL again must
// not hide a complete descriptor already persisted by an earlier play.
func (s *Service) reusablePayload(ctx context.Context, rel core.Release) (Payload, bool) {
	p, ok := s.storedPayload(ctx, payloadKey(rel))
	if !ok || p.ReleaseID != rel.ID || s.entryByID(p.Provider) == nil {
		return Payload{}, false
	}
	if hash := strings.ToLower(rel.InfoHash); hash != "" && hash != p.InfoHash {
		return Payload{}, false
	}
	return p, true
}

type payloadFlight struct {
	done chan struct{}
	data []byte
	err  error
}

// fetchTorrent shares descriptor lookup and provider work across concurrent
// detail/Play requests. Work stays on the leading caller's goroutine/context;
// a cancelled leader releases waiters to retry rather than stranding a stream
// or starting an unowned background acquisition.
func (x *Indexer) fetchTorrent(ctx context.Context, rel core.Release) ([]byte, error) {
	if rel.ID == "" {
		return x.fetchTorrentOnce(ctx, rel)
	}
	key := payloadKey(rel) + "\x00" + strings.ToLower(rel.InfoHash) + "\x00" + rel.MagnetURL
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		x.mu.Lock()
		if f := x.flights[key]; f != nil {
			x.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-f.done:
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(f.err, context.Canceled) || errors.Is(f.err, context.DeadlineExceeded) {
				continue
			}
			return append([]byte(nil), f.data...), f.err
		}
		f := &payloadFlight{done: make(chan struct{})}
		if x.flights == nil {
			x.flights = make(map[string]*payloadFlight)
		}
		x.flights[key] = f
		x.mu.Unlock()
		data, err := x.fetchTorrentOnce(ctx, rel)
		x.mu.Lock()
		f.data, f.err = data, err
		delete(x.flights, key)
		close(f.done)
		x.mu.Unlock()
		return append([]byte(nil), data...), err
	}
}
