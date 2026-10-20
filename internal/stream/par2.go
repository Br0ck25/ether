package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/nzb"
	"github.com/braedonsaunders/ether/internal/par2"
)

// PAR2 use. The engine never repairs: a recovery block mixes every slice of
// the set, so rebuilding one article means reading the whole file. It reads
// only the release's PAR2 index file (a few KB of metadata packets) to
//
//   - name obfuscated files: the MD5 of a file's first 16 KiB, taken from
//     its first article, identifies the file description carrying its real
//     name and exact length (a unique exact length is the fallback), which
//     classifies it and orders archive volumes without content sniffing;
//   - check integrity: the first article of the main video (or of the first
//     archive volume) must match the recorded length, first-16k MD5 and any
//     slice checksums it covers, catching wrong or damaged uploads a yEnc
//     CRC cannot.
//
// All of it is best-effort: no PAR2, a failed fetch, an oversize index or
// an unparseable or foreign one leave the engine on its normal path.

const (
	// maxPar2Bytes bounds the listed (encoded) size of the index file read.
	maxPar2Bytes = 1 << 20
	// maxPar2Parts bounds its article count.
	maxPar2Parts = 8
)

// par2State is a release's PAR2 index, loaded at most once per index file.
type par2State struct {
	mu      sync.Mutex
	done    bool
	set     *par2.Set
	file    *postedFile // the file the settled attempt read
	wait    chan struct{}
	skipped bool // an oversize index was noted
}

// par2IndexFile returns the release's PAR2 index file if it exists and is
// small enough to read.
func (r *release) par2IndexFile() *postedFile {
	r.infoMu.RLock()
	f := nzb.Par2Index(r.doc.Files)
	r.infoMu.RUnlock()
	if f == nil {
		return nil
	}
	var pf *postedFile
	for _, c := range r.files {
		if c.file == f {
			pf = c
			break
		}
	}
	if pf == nil {
		return nil
	}
	if !par2Readable(pf) {
		r.p2.mu.Lock()
		first := !r.p2.skipped
		r.p2.skipped = true
		r.p2.mu.Unlock()
		if first {
			r.e.par2Skipped.Add(1)
			r.e.log.Debug("par2 index too large; not read", "name", pf.Name(), "bytes", f.Bytes(), "parts", pf.numSlots())
		}
		return nil
	}
	return pf
}

func par2Readable(pf *postedFile) bool {
	return pf.file.Bytes() <= maxPar2Bytes && pf.numSlots() <= maxPar2Parts
}

// par2Ready returns the loaded index, or nil if none is loaded (yet).
func (r *release) par2Ready() *par2.Set {
	r.p2.mu.Lock()
	defer r.p2.mu.Unlock()
	return r.p2.set
}

// loadPar2 reads and parses PAR2 file pf (joining a load in flight). It
// returns nil when the file is unusable. A settled result is kept; a load
// cut short by ctx or a transient fetch error is retried by the next caller.
func (r *release) loadPar2(ctx context.Context, prio core.Priority, fo fetchOpts, pf *postedFile) *par2.Set {
	if pf == nil {
		return nil
	}
	for {
		r.p2.mu.Lock()
		if r.p2.done && (r.p2.set != nil || r.p2.file == pf) {
			s := r.p2.set
			r.p2.mu.Unlock()
			return s
		}
		if ch := r.p2.wait; ch != nil {
			r.p2.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil
			}
		}
		ch := make(chan struct{})
		r.p2.wait = ch
		r.p2.mu.Unlock()

		set, final := r.fetchPar2(ctx, prio, fo, pf)
		r.p2.mu.Lock()
		r.p2.wait = nil
		if final {
			r.p2.done, r.p2.set, r.p2.file = true, set, pf
		}
		close(ch)
		r.p2.mu.Unlock()
		return set
	}
}

// fetchPar2 fetches every article of pf in parallel (through the hot
// buffer and segment cache) and parses what arrived.
func (r *release) fetchPar2(ctx context.Context, prio core.Priority, fo fetchOpts, pf *postedFile) (*par2.Set, bool) {
	n := pf.numSlots()
	segs := make([]*segData, n)
	errs := make([]error, n)
	b := &builder{e: r.e, rel: r, prio: prio, fo: fo}
	_ = b.parallel(ctx, n, func(ctx context.Context, i int) error {
		segs[i], errs[i] = r.e.segment(ctx, r, pf, i, prio, fo)
		return nil // a lost article only loses the packets it held
	})
	if ctx.Err() != nil {
		return nil, false
	}
	final := true
	var data []byte
	for i, s := range segs {
		if errs[i] != nil || s == nil {
			if !isMissing(errs[i]) {
				final = false
			}
			continue
		}
		if len(data)+len(s.data) > 2*maxPar2Bytes {
			break
		}
		data = append(data, s.data...)
	}
	set, err := par2.Parse(data)
	if err != nil {
		if final {
			r.e.par2Failed.Add(1)
		}
		r.e.log.Debug("par2 index unusable", "name", pf.Name(), "bytes", len(data), "err", err)
		return nil, final
	}
	if final {
		r.e.par2Loaded.Add(1)
	}
	r.e.log.Debug("par2 index loaded", "name", pf.Name(), "files", len(set.Files), "packets", set.Packets, "skipped", set.Skipped)
	return set, final
}

// isPar2Head reports whether a file's first bytes are a PAR2 packet.
func isPar2Head(head []byte) bool { return bytes.HasPrefix(head, []byte(par2.Magic)) }

// identifyByPar2 finds the PAR2 file description of a posted file from its
// first article.
func identifyByPar2(set *par2.Set, pf *postedFile, head *segData) *par2.File {
	if set == nil || head == nil || head.begin != 0 {
		return nil
	}
	var size int64
	if pf.SizeKnown() {
		size = pf.Size()
	}
	f, _ := set.Identify(head.data, size)
	return f
}

// nameFromPar2 gives a posted file the real name recorded in PAR2.
func (b *builder) nameFromPar2(pf *postedFile, f *par2.File) bool {
	name := f.BaseName()
	info := nzb.Classify(name)
	if name == "" || name == "." || info.Kind == nzb.KindUnknown {
		return false
	}
	if pf.Name() != name {
		b.e.par2Named.Add(1)
	}
	b.rel.setFileInfo(func() { pf.file.Name, pf.file.Info, pf.file.Obfuscated = name, info, nzb.IsObfuscated(name) })
	pf.setName(name)
	return true
}

// verifyPar2 checks the plan's first file against the release's PAR2 index
// and keeps the verdict on the plan. With wait it loads the index (joining
// a load in flight); otherwise it only uses an index already loaded. It
// returns an error wrapping ErrPar2Mismatch when the content is not what
// the poster hashed, and nil when it matches or cannot be checked.
func (r *release) verifyPar2(ctx context.Context, p *plan, prio core.Priority, fo fetchOpts, wait bool) error {
	if done, err := p.par2Verdict(); done {
		return err
	}
	var set *par2.Set
	if wait {
		set = r.loadPar2(ctx, prio, fo, r.par2IndexFile())
	}
	if set == nil {
		set = r.par2Ready()
	}
	if set == nil {
		return nil
	}
	final, err := r.e.checkPar2(ctx, r, p, set, prio, fo)
	if final {
		p.setPar2Verdict(err)
	}
	return err
}

// checkPar2 verifies the head of the plan's first volume (the video itself
// for plain posts) against the PAR2 description carrying its name.
func (e *Engine) checkPar2(ctx context.Context, r *release, p *plan, set *par2.Set, prio core.Priority, fo fetchOpts) (final bool, err error) {
	pf := p.vols[0]
	head, ferr := e.segment(ctx, r, pf, 0, prio, fo)
	if ferr != nil {
		// Missing articles are reported by the plan and the sampler.
		return false, nil
	}
	cands := set.Named(pf.Name())
	if len(cands) == 0 && head.name != "" {
		cands = set.Named(head.name)
	}
	if len(cands) == 0 || head.begin != 0 {
		return true, nil
	}
	var size int64
	if pf.SizeKnown() {
		size = pf.Size()
	}
	prefix := head.data
	if int64(len(prefix)) < par2.Hash16kSize && size > int64(len(prefix)) {
		// Articles smaller than 16 KiB: read the rest of the hashed range.
		buf := make([]byte, min(size, par2.Hash16kSize))
		n, err := e.readFile(ctx, r, pf, buf, 0, prio, fo)
		if err != nil && n < len(buf) {
			return false, nil
		}
		prefix = buf[:n]
	}
	var first error
	for _, f := range cands {
		checks, err := f.Verify(prefix, size)
		if err == nil {
			if checks > 0 {
				e.par2Verified.Add(1)
			}
			return true, nil
		}
		if first == nil {
			first = err
		}
	}
	e.par2Mismatches.Add(1)
	e.log.Warn("release content does not match its par2", "name", pf.Name(), "err", first)
	return true, fmt.Errorf("%w: %w", ErrPar2Mismatch, first)
}

// par2Verdict returns the kept verification result.
func (p *plan) par2Verdict() (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.par2Done, p.par2Err
}

func (p *plan) setPar2Verdict(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.par2Done {
		p.par2Done, p.par2Err = true, err
	}
}

// par2Reason is a short reason for a health trace.
func par2Reason(err error) string {
	var mm *par2.MismatchError
	if errors.As(err, &mm) {
		return mm.Error()
	}
	return err.Error()
}
