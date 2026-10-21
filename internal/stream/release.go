package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/nzb"
)

var errEOF = io.EOF

// release is the engine's state for one NZB: its posted files with their
// indexes, and the plan (layout of the main video), built once.
type release struct {
	e      *Engine
	digest string
	doc    *nzb.NZB
	files  []*postedFile

	partSize atomic.Int64 // first learned uniform part size, shared default

	refs int // guarded by e.mu

	// infoMu guards the nzb files' Name, Info and Obfuscated: identifying
	// obfuscated files (plan build, under planMu) rewrites them while health
	// checks and PAR2 lookups of the same release read them.
	infoMu sync.RWMutex

	planMu  sync.Mutex
	plan    *plan
	planErr error // permanent rejection, cached

	p2 par2State
}

func newRelease(e *Engine, digest string, doc *nzb.NZB) *release {
	r := &release{e: e, digest: digest, doc: doc}
	base := 0
	for i, f := range doc.Files {
		pf := newPostedFile(i, f, base, r.partSize.Load)
		base += pf.numSlots() + 1
		r.files = append(r.files, pf)
	}
	return r
}

func (r *release) notePartSize(pf *postedFile) {
	if p := pf.learnedPartSize(); p > 0 {
		r.partSize.CompareAndSwap(0, p)
	}
}

// ensurePlan builds the plan once. Rejections (compressed, encrypted, no
// playable file ...) are cached; transient errors are retried by the next
// caller.
func (r *release) ensurePlan(ctx context.Context, prio core.Priority, cacheID string) (*plan, error) {
	return r.ensurePlanWithCandidate(ctx, prio, cacheID, nil)
}

// ensurePlanWithCandidate announces the first identified candidate before
// reading its archive layout. Health checks can inspect its articles while
// planning continues. The callback must not block or call back into planMu.
func (r *release) ensurePlanWithCandidate(ctx context.Context, prio core.Priority, cacheID string, ready func([]*postedFile)) (*plan, error) {
	r.planMu.Lock()
	defer r.planMu.Unlock()
	if r.plan != nil {
		if ready != nil {
			ready(r.plan.vols)
		}
		return r.plan, nil
	}
	if r.planErr != nil {
		return nil, r.planErr
	}
	b := &builder{e: r.e, rel: r, prio: prio, fo: fetchOpts{cacheID: cacheID}, candidateReady: ready}
	p, err := b.build(ctx)
	if err != nil {
		if IsRejection(err) && ctx.Err() == nil {
			r.planErr = err
		}
		return nil, err
	}
	r.plan = p
	return p, nil
}

// builder builds a plan: it picks the main video and maps it.
type builder struct {
	e              *Engine
	rel            *release
	prio           core.Priority
	fo             fetchOpts
	candidateReady func([]*postedFile)
}

// candidate is something that may hold the main video.
type candidate struct {
	kind  nzb.Kind // KindVideo, KindRAR, KindSevenZip, KindSplit
	files []*postedFile
	set   *nzb.Set
	bytes int64
}

func (b *builder) build(ctx context.Context) (*plan, error) {
	cands := b.candidates(b.rel.files)
	if b.shouldProbe(cands) {
		if err := b.identifyObfuscated(ctx); err != nil {
			return nil, err
		}
		cands = b.candidates(b.rel.files)
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("%w: nothing recognisable among %d files", ErrNoPlayableFile, len(b.rel.files))
	}
	if b.candidateReady != nil {
		b.candidateReady(cands[0].files)
	}
	var firstErr error
	for _, c := range cands {
		p, err := b.planFor(ctx, c)
		if err == nil {
			b.e.log.Debug("stream plan", "kind", p.kind, "name", p.name, "size", p.size, "volumes", len(p.vols))
			// A PAR2 index already loaded (obfuscated post, or a health
			// check reading it alongside) checks the first volume now; the
			// head is in memory. Open never waits for PAR2.
			if err := b.rel.verifyPar2(ctx, p, b.prio, b.fo, false); err != nil {
				return nil, err
			}
			return p, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Only fall through to smaller candidates when this one simply had
		// no video; a broken or protected main archive rejects the release.
		if !errors.Is(err, ErrNoPlayableFile) {
			return nil, err
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// candidates lists playable-looking files and archive sets, largest first.
func (b *builder) candidates(files []*postedFile) []candidate {
	b.rel.infoMu.RLock()
	defer b.rel.infoMu.RUnlock()
	var out []candidate
	var vols []*nzb.File
	byFile := map[*nzb.File]*postedFile{}
	for _, pf := range files {
		f := pf.file
		byFile[f] = pf
		switch {
		case f.Info.Kind == nzb.KindVideo && !f.Info.Sample && !f.Info.Extra:
			out = append(out, candidate{kind: nzb.KindVideo, files: []*postedFile{pf}, bytes: f.Bytes()})
		case f.Info.Kind.IsArchive():
			vols = append(vols, f)
		}
	}
	for _, s := range nzb.GroupSets(vols) {
		if s.Kind == nzb.KindSplit && len(s.Volumes) > 0 {
			switch nzb.Classify("x." + s.Volumes[0].Info.Ext).Kind {
			case nzb.KindVideo, nzb.KindUnknown, nzb.KindSplit:
			default:
				continue // e.g. a split .iso; "name.2024.001" stays (sniffed)
			}
		}
		if len(s.Volumes) > 0 && s.Volumes[0].Info.Sample {
			continue
		}
		c := candidate{kind: s.Kind, set: s, bytes: s.Bytes()}
		for _, v := range s.Volumes {
			c.files = append(c.files, byFile[v])
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].bytes > out[j].bytes })
	return out
}

func (b *builder) planFor(ctx context.Context, c candidate) (*plan, error) {
	if c.set != nil && len(c.set.Missing) > 0 {
		return nil, incompleteError("%s set %q lacks volumes %v", c.kind, c.set.Name, c.set.Missing)
	}
	if len(c.files) == 0 {
		return nil, ErrNoPlayableFile
	}
	for _, pf := range c.files {
		if m := pf.file.Missing(); len(m) > 0 {
			return nil, incompleteError("%s lacks %d of %d parts (first: %d)", pf.Name(), len(m), pf.file.SegmentCount(), m[0])
		}
	}
	switch c.kind {
	case nzb.KindVideo:
		return b.planPlain(ctx, c.files[0])
	case nzb.KindRAR:
		return b.planRAR(ctx, c.files)
	case nzb.KindSevenZip:
		return b.plan7z(ctx, c.files)
	case nzb.KindSplit:
		return b.planSplit(ctx, c.files)
	}
	return nil, ErrNoPlayableFile
}

// setFileInfo applies a change to nzb file identification under infoMu.
func (r *release) setFileInfo(fn func()) {
	r.infoMu.Lock()
	fn()
	r.infoMu.Unlock()
}

// head fetches slot 0 of each file in parallel (learning their sizes).
func (b *builder) heads(ctx context.Context, files []*postedFile) error {
	return b.parallel(ctx, len(files), func(ctx context.Context, i int) error {
		_, err := b.e.segment(ctx, b.rel, files[i], 0, b.prio, b.fo)
		return err
	})
}

// parallel runs fn(0..n-1) with bounded concurrency, returning the first
// error and cancelling the rest.
func (b *builder) parallel(ctx context.Context, n int, fn func(ctx context.Context, i int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, b.e.opts.OpenParallel)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for i := 0; i < n; i++ {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(ctx, i); err != nil {
				once.Do(func() { first = err; cancel() })
			}
		}(i)
	}
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}

// fileReader is an io.ReaderAt over one posted file, used to parse archive
// headers.
type fileReader struct {
	ctx  context.Context
	b    *builder
	pf   *postedFile
	prio core.Priority
}

func (b *builder) reader(ctx context.Context, pf *postedFile) *fileReader {
	return &fileReader{ctx: ctx, b: b, pf: pf, prio: b.prio}
}

func (r *fileReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("stream: negative offset")
	}
	size := r.pf.Size()
	if off >= size {
		return 0, io.EOF
	}
	want := p
	if rem := size - off; int64(len(want)) > rem {
		want = want[:rem]
	}
	n, err := r.b.e.readFile(r.ctx, r.b.rel, r.pf, want, off, r.prio, r.b.fo)
	if err == nil && n < len(p) {
		err = io.EOF
	}
	return n, err
}

// sniff fetches (if needed) and returns the first bytes of a posted file.
func (b *builder) sniff(ctx context.Context, pf *postedFile) ([]byte, error) {
	seg, err := b.e.segment(ctx, b.rel, pf, 0, b.prio, b.fo)
	if err != nil {
		return nil, err
	}
	if seg.begin != 0 {
		return nil, nil
	}
	return seg.data, nil
}
