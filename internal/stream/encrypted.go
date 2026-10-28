package stream

import (
	"context"
	"fmt"
	"io"
	"math"
	"path"
	"sync"

	"github.com/braedonsaunders/ether/internal/archive"
	"github.com/braedonsaunders/ether/internal/core"
)

func (b *builder) rarOptions(ctx context.Context, maxFiles int) *archive.RAROptions {
	return &archive.RAROptions{Context: ctx, Password: b.rel.doc.Password(), MaxFiles: maxFiles, PasswordCache: &b.rel.rarPasswords}
}

// encryptedPlan retains ciphertext in the existing segment caches. Only bounded
// requested ranges are decrypted, and neither keys nor plaintext are persisted.
func (b *builder) encryptedPlan(ctx context.Context, kind planKind, member *archive.Member, vols []*postedFile, info []volInfo) (*plan, error) {
	if len(info) != len(vols) {
		return nil, ErrCorrupt
	}
	for i, pf := range vols {
		if !pf.SizeKnown() || info[i].size != pf.Size() {
			return nil, fmt.Errorf("stream: encrypted archive volume size is unverified: %w", ErrCorrupt)
		}
	}
	m := *member
	m.Extents = append([]archive.Extent(nil), member.Extents...)
	if kind == kind7z {
		// The 7z parser addresses the concatenated archive. Resolve those offsets
		// against learned volume sizes before publishing an immutable crypto plan.
		var total int64
		raw := &layout{}
		for i, v := range info {
			if v.size < 0 || v.size > math.MaxInt64-total {
				return nil, ErrCorrupt
			}
			raw.ext = append(raw.ext, extent{inner: total, size: v.size, vol: i})
			total += v.size
		}
		raw.size = total
		m.Extents = nil
		for _, x := range member.Extents {
			if x.Volume != 0 || x.Offset < 0 || x.Size < 0 || x.Offset > total || x.Size > total-x.Offset {
				return nil, ErrCorrupt
			}
			for _, pc := range raw.pieces(x.Offset, x.Size) {
				m.Extents = append(m.Extents, archive.Extent{Volume: pc.vol, Offset: pc.off, Size: pc.n})
			}
		}
	}
	verified := make([]bool, len(vols))
	for i := range verified {
		verified[i] = true
	}
	p := newPlan(b.rel, kind, path.Base(m.Name), m.Size, vols, info, verified)
	p.member, p.encrypted = m.Name, &m
	if err := p.init(); err != nil {
		return nil, err
	}
	r, err := p.encryptedReader(ctx, b.e, b.rel, b.prio, b.fo)
	if err != nil {
		return nil, err
	}
	// Some formats lack a password check for data-only encryption. A successfully
	// parsed header must not bless random plaintext from a wrong password. This
	// bounded signature check is additional to archive CRCs and later media probe;
	// it cannot establish whole-file integrity for random-access playback.
	head := make([]byte, min(int64(4096), p.size))
	if _, err := r.ReadAt(head, 0); err != nil {
		return nil, err
	}
	if !looksLikeVideo(head) {
		return nil, fmt.Errorf("stream: decrypted archive member has no supported video signature (incorrect password or damaged content): %w", ErrCorrupt)
	}
	return p, nil
}

// encryptedLayout builds a plaintext view for read-ahead scheduling while
// retaining the complete ciphertext view for CBC dependencies and padding.
func (p *plan) encryptedLayout(version int) (*layout, error) {
	m := p.encrypted
	if m == nil || m.Encryption == nil || len(p.info) != len(p.vols) || len(p.verified) != len(p.vols) {
		return nil, ErrCorrupt
	}
	c := &layout{version: version}
	for _, x := range m.Extents {
		if x.Volume < 0 || x.Volume >= len(p.info) || !p.verified[x.Volume] {
			return nil, ErrCorrupt
		}
		size := p.info[x.Volume].size
		if size < 0 || x.Offset < 0 || x.Size < 0 || x.Offset > size || x.Size > size-x.Offset || x.Size > math.MaxInt64-c.size {
			return nil, ErrCorrupt
		}
		c.ext = append(c.ext, extent{inner: c.size, size: x.Size, vol: x.Volume, off: x.Offset})
		c.size += x.Size
	}
	enc := m.Encryption
	if p.size < 0 || enc.Size < 0 || enc.Offset < 0 || enc.Offset > enc.Size || p.size > enc.Size-enc.Offset || enc.Size > c.size || c.size-enc.Size > 15 || c.size%16 != 0 {
		return nil, ErrCorrupt
	}
	l := &layout{version: version, size: p.size}
	for _, pc := range c.pieces(enc.Offset, p.size) {
		l.ext = append(l.ext, extent{inner: pc.bufOff, size: pc.n, vol: pc.vol, off: pc.off})
	}
	p.cipherLayout = c
	return l, nil
}

func (p *plan) encryptedReader(ctx context.Context, e *Engine, rel *release, prio core.Priority, fo fetchOpts) (io.ReaderAt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b := &builder{e: e, rel: rel, prio: prio, fo: fo}
	vols := make([]archive.Volume, len(p.vols))
	for i, pf := range p.vols {
		vols[i] = archive.Volume{R: b.reader(ctx, pf), Size: p.info[i].size}
	}
	return archive.NewMemberReader(*p.encrypted, vols)
}

// fetchPieces includes the CBC predecessor block and end padding. Persistent
// preloading must retain these bytes too, even when they sit in another article
// or another archive volume. Piece bufOff values here are ciphertext-relative;
// these pieces are for cache warming, never copied directly to a media response.
func (p *plan) fetchPieces(off, n int64) []piece {
	if p.encrypted == nil {
		return p.layout().pieces(off, n)
	}
	if off < 0 {
		off = 0
	}
	if off >= p.size || n <= 0 {
		return nil
	}
	n = min(n, p.size-off)
	start := p.encrypted.Encryption.Offset + off
	end := start + n
	start -= start % 16
	if start > 0 {
		start -= 16
	}
	if rem := end % 16; rem != 0 {
		end += 16 - rem
	}
	return p.cipherLayout.pieces(start, end-start)
}

// readEncryptedPlan preserves demand-read parallelism across articles. The CBC
// adapter has bounded scratch chunks, but serially feeding one large range to
// it would serialize network requests when speculative read-ahead is disabled.
// Each range has its own CBC state and predecessor; the shared ReaderAt is safe
// for concurrent calls. Workers are joined before returning caller-owned bytes.
func readEncryptedPlan(parent context.Context, e *Engine, rel *release, p *plan, buf []byte, off int64, prio core.Priority, fo fetchOpts) (int, error) {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	r, err := p.encryptedReader(ctx, e, rel, prio, fo)
	if err != nil {
		return 0, err
	}
	const chunk = 256 << 10
	count := (len(buf) + chunk - 1) / chunk
	if count <= 1 {
		return r.ReadAt(buf, off)
	}
	type result struct {
		n   int
		err error
	}
	results := make([]result, count)
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < min(count, max(1, e.opts.ReadParallel)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				lo := index * chunk
				hi := min(lo+chunk, len(buf))
				if err := ctx.Err(); err != nil {
					results[index].err = err
					continue
				}
				n, err := r.ReadAt(buf[lo:hi], off+int64(lo))
				results[index] = result{n, err}
				if err != nil {
					cancel(err)
				}
			}
		}()
	}
	for index := 0; index < count; index++ {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	n := 0
	for _, res := range results {
		n += res.n
		if res.err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return n, cause
			}
			return n, res.err
		}
	}
	return n, nil
}
