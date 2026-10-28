package stream

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/braedonsaunders/ether/internal/archive"
	"github.com/braedonsaunders/ether/internal/core"
)

type planKind string

const (
	kindPlain planKind = "plain"
	kindRAR   planKind = "rar"
	kind7z    planKind = "7z"
	kindSplit planKind = "split"
)

// volInfo is what the layout knows (or assumes) about one volume.
type volInfo struct {
	size     int64 // decoded size of the posted file
	dataOff  int64 // RAR/split/plain: where the video's bytes start in the volume
	dataSize int64 // ... and how many there are
}

// extent maps [inner, inner+size) of the video onto volume vol at off.
type extent struct {
	inner, size int64
	vol         int
	off         int64
}

// layout is an immutable snapshot of the video → volume mapping.
type layout struct {
	version int
	size    int64
	ext     []extent
}

// piece is one contiguous run of a read inside a single volume.
type piece struct {
	vol    int
	off    int64 // offset in the volume
	n      int64
	bufOff int64 // offset in the caller's buffer
}

// pieces splits [off, off+n) into per-volume runs.
func (l *layout) pieces(off, n int64) []piece {
	if off < 0 || off >= l.size || n <= 0 {
		return nil
	}
	if n > l.size-off {
		n = l.size - off
	}
	i := sort.Search(len(l.ext), func(i int) bool { return l.ext[i].inner+l.ext[i].size > off })
	var out []piece
	var done int64
	for ; i < len(l.ext) && done < n; i++ {
		x := l.ext[i]
		start := off + done
		if start < x.inner {
			break // hole: cannot happen for a valid layout
		}
		within := start - x.inner
		take := x.size - within
		if take > n-done {
			take = n - done
		}
		if take <= 0 {
			continue
		}
		out = append(out, piece{vol: x.vol, off: x.off + within, n: take, bufOff: done})
		done += take
	}
	return out
}

// plan is the resolved main video of a release.
type plan struct {
	kind planKind
	name string
	size int64
	vols []*postedFile
	rel  *release

	// RAR: the member name every continuation header must carry, and
	// whether volumes use RAR5 headers.
	member string
	rar5   bool
	// 7z: the member's extents in the concatenated archive.
	concatExt []archive.Extent

	// Encrypted plans use verified ciphertext extents, plus a plaintext layout
	// for scheduling. Keys exist only in this bounded, in-memory release plan.
	encrypted    *archive.Member
	cipherLayout *layout

	mu       sync.Mutex
	lay      *layout
	info     []volInfo
	verified []bool
	pending  map[int]chan struct{}
	exactErr error
	exact    chan struct{} // non-nil while an exact rebuild runs

	// PAR2 verification verdict of the first volume (see verifyPar2).
	par2Done bool
	par2Err  error
}

func newPlan(rel *release, kind planKind, name string, size int64, vols []*postedFile, info []volInfo, verified []bool) *plan {
	return &plan{rel: rel, kind: kind, name: name, size: size, vols: vols, info: info, verified: verified, pending: map[int]chan struct{}{}}
}

// init builds the first layout; it fails if the volume model does not add
// up to the video size.
func (p *plan) init() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	l, err := p.buildLocked(1)
	if err != nil {
		return err
	}
	p.lay = l
	return nil
}

func (p *plan) layout() *layout {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lay
}

func (p *plan) isVerified(vol int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.verified[vol]
}

func (p *plan) buildLocked(version int) (*layout, error) {
	if p.encrypted != nil {
		return p.encryptedLayout(version)
	}
	if p.size < 0 || len(p.info) != len(p.vols) || len(p.verified) != len(p.vols) {
		return nil, fmt.Errorf("stream: inconsistent archive layout: %w", ErrCorrupt)
	}
	for k, vi := range p.info {
		if vi.size < 0 || vi.dataSize < 0 {
			return nil, fmt.Errorf("stream: volume %d has a negative size: %w", k, ErrCorrupt)
		}
		if !p.verified[k] {
			p.vols[k].setAssumed(vi.size)
		}
	}
	l := &layout{version: version, size: p.size}
	switch p.kind {
	case kind7z:
		starts := make([]int64, len(p.info)+1)
		for k, vi := range p.info {
			if vi.size > math.MaxInt64-starts[k] {
				return nil, fmt.Errorf("stream: concatenated archive size overflows: %w", ErrCorrupt)
			}
			starts[k+1] = starts[k] + vi.size
		}
		var inner int64
		for _, x := range p.concatExt {
			total := starts[len(p.info)]
			if x.Volume != 0 || x.Offset < 0 || x.Size < 0 || x.Offset > total || x.Size > total-x.Offset || x.Size > p.size-inner {
				return nil, fmt.Errorf("stream: invalid concatenated archive extent: %w", ErrCorrupt)
			}
			off, rem := x.Offset, x.Size
			k := sort.Search(len(p.info), func(k int) bool { return starts[k+1] > off })
			for rem > 0 {
				if k >= len(p.info) {
					return nil, fmt.Errorf("stream: 7z member extends past the last volume: %w", ErrCorrupt)
				}
				take := starts[k+1] - off
				if take > rem {
					take = rem
				}
				l.ext = append(l.ext, extent{inner: inner, size: take, vol: k, off: off - starts[k]})
				inner += take
				off += take
				rem -= take
				k++
			}
		}
		if inner != p.size {
			return nil, fmt.Errorf("stream: 7z extents cover %d of %d bytes: %w", inner, p.size, ErrCorrupt)
		}
	default:
		var inner int64
		for k, vi := range p.info {
			if vi.dataSize <= 0 {
				continue
			}
			if vi.dataOff < 0 || vi.dataOff > vi.size || vi.dataSize > vi.size-vi.dataOff || vi.dataSize > p.size-inner {
				return nil, fmt.Errorf("stream: volume %d data [%d,+%d) exceeds its size %d: %w", k, vi.dataOff, vi.dataSize, vi.size, ErrCorrupt)
			}
			l.ext = append(l.ext, extent{inner: inner, size: vi.dataSize, vol: k, off: vi.dataOff})
			inner += vi.dataSize
		}
		if inner != p.size {
			return nil, fmt.Errorf("stream: volumes hold %d of %d bytes: %w", inner, p.size, ErrCorrupt)
		}
	}
	return l, nil
}

// verifier performs a volume check with a given priority and cache id.
type verifier struct {
	e    *Engine
	prio core.Priority
	fo   fetchOpts
}

// ensure verifies every listed volume. It returns once each is verified
// (possibly after an exact rebuild changed the layout).
func (p *plan) ensure(ctx context.Context, v verifier, vols []int) error {
	for _, k := range vols {
		if err := p.ensureOne(ctx, v, k); err != nil {
			return err
		}
	}
	return nil
}

func (p *plan) ensureOne(ctx context.Context, v verifier, k int) error {
	for {
		p.mu.Lock()
		if p.verified[k] {
			p.mu.Unlock()
			return nil
		}
		if p.exactErr != nil {
			err := p.exactErr
			p.mu.Unlock()
			return err
		}
		if ch := p.exact; ch != nil {
			p.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if ch, ok := p.pending[k]; ok {
			p.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		ch := make(chan struct{})
		p.pending[k] = ch
		p.mu.Unlock()

		actual, err := p.inspect(ctx, v, k)
		p.mu.Lock()
		delete(p.pending, k)
		close(ch)
		if err != nil {
			p.mu.Unlock()
			return err
		}
		if p.verified[k] {
			// An exact rebuild finished meanwhile.
			p.mu.Unlock()
			return nil
		}
		if actual == p.info[k] {
			p.verified[k] = true
			p.mu.Unlock()
			return nil
		}
		if ch := p.exact; ch != nil {
			// A rebuild is already running; it will settle this volume.
			p.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		// A concurrent verifier can replace info as soon as we unlock.
		// Snapshot the diagnostic value under the same lock as the comparison.
		assumed := p.info[k]
		p.mu.Unlock()
		v.e.log.Warn("archive volume layout differs from the uniform assumption; rebuilding from every volume",
			"name", p.name, "volume", k, "assumed", fmt.Sprintf("%+v", assumed), "actual", fmt.Sprintf("%+v", actual))
		if err := p.rebuildExact(ctx, v); err != nil {
			return err
		}
	}
}

// inspect reads volume k's real parameters.
func (p *plan) inspect(ctx context.Context, v verifier, k int) (volInfo, error) {
	pf := p.vols[k]
	b := &builder{e: v.e, rel: p.rel, prio: v.prio, fo: v.fo}
	if _, err := v.e.segment(ctx, p.rel, pf, 0, v.prio, v.fo); err != nil {
		return volInfo{}, err
	}
	size := pf.Size()
	switch p.kind {
	case kindRAR:
		vol, err := archive.ReadRARVolume(b.reader(ctx, pf), size, b.rarOptions(ctx, 1))
		if err != nil {
			return volInfo{}, fmt.Errorf("stream: rar volume %d (%s): %w", k+1, pf.Name(), err)
		}
		if len(vol.Files) == 0 {
			return volInfo{}, fmt.Errorf("stream: rar volume %d (%s) has no file header: %w", k+1, pf.Name(), ErrCorrupt)
		}
		f := vol.Files[0]
		if f.Name != p.member || (k > 0 && !f.SplitBefore) {
			return volInfo{}, fmt.Errorf("stream: rar volume %d (%s) starts with %q, not a continuation of %q: %w", k+1, pf.Name(), f.Name, p.member, ErrCorrupt)
		}
		if m := rarMember(vol.Format, &f); m.Streamable() != nil {
			return volInfo{}, fmt.Errorf("stream: rar volume %d (%s): %w", k+1, pf.Name(), m.Streamable())
		}
		return volInfo{size: size, dataOff: f.DataOffset, dataSize: f.PackedSize}, nil
	case kind7z:
		return volInfo{size: size}, nil
	default:
		return volInfo{size: size, dataOff: 0, dataSize: size}, nil
	}
}

// rebuildExact inspects every volume and rebuilds the layout from facts.
func (p *plan) rebuildExact(ctx context.Context, v verifier) error {
	p.mu.Lock()
	if p.exactErr != nil {
		err := p.exactErr
		p.mu.Unlock()
		return err
	}
	if ch := p.exact; ch != nil {
		p.mu.Unlock()
		select {
		case <-ch:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ch := make(chan struct{})
	p.exact = ch
	p.mu.Unlock()

	infos := make([]volInfo, len(p.vols))
	b := &builder{e: v.e, rel: p.rel, prio: v.prio, fo: v.fo}
	err := b.parallel(ctx, len(p.vols), func(ctx context.Context, k int) error {
		vi, err := p.inspect(ctx, v, k)
		infos[k] = vi
		return err
	})

	p.mu.Lock()
	defer func() {
		p.exact = nil
		close(ch)
		p.mu.Unlock()
	}()
	if err != nil {
		if IsRejection(err) && ctx.Err() == nil {
			p.exactErr = err
		}
		return err
	}
	p.info = infos
	for k := range p.verified {
		p.verified[k] = true
	}
	version := 1
	if p.lay != nil {
		version = p.lay.version + 1
	}
	l, err := p.buildLocked(version)
	if err != nil {
		p.exactErr = err
		return err
	}
	p.lay = l
	return nil
}
