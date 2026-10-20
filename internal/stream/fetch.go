package stream

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// flight is one in-progress segment fetch that concurrent requests join.
type flight struct {
	done    chan struct{}
	seg     *segData
	err     error
	waiters int
	cancel  context.CancelFunc
}

// fetchOpts says where a fetched segment may be stored.
type fetchOpts struct {
	cacheID string // release ID for the disk cache; "" = don't use it
	noHot   bool   // don't fill the hot buffer (background prefetch)
}

// segment returns slot i of pf: from the hot buffer, the disk cache, or
// Usenet, coalescing concurrent requests. The slot's index entry is
// updated from the article's yEnc header.
func (e *Engine) segment(ctx context.Context, rel *release, pf *postedFile, i int, prio core.Priority, fo fetchOpts) (*segData, error) {
	for attempt := 0; ; attempt++ {
		seg, abandoned, err := e.segmentOnce(ctx, rel, pf, i, prio, fo)
		if !abandoned || attempt >= 3 {
			return seg, err
		}
		// The flight was abandoned by its other waiters just as this request
		// joined it; fetch again on our own behalf.
	}
}

func (e *Engine) segmentOnce(ctx context.Context, rel *release, pf *postedFile, i int, prio core.Priority, fo fetchOpts) (_ *segData, abandoned bool, _ error) {
	id := pf.msgID(i)
	if id == "" {
		e.missing.Add(1)
		return nil, false, fmt.Errorf("stream: %s part %d not in nzb: %w", pf.Name(), i+1, core.ErrArticleMissing)
	}
	key := segKey{rel.digest, pf.segBase + i}
	if seg, ok := e.hot.get(key); ok {
		e.hotHits.Add(1)
		e.learnHot(rel, pf, i, seg)
		return seg, false, nil
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, false, ErrClosed
	}
	// A flight finishing between the lookup above and taking the lock has
	// already filled the hot buffer (it does so before leaving the map).
	if seg, ok := e.hot.get(key); ok {
		e.mu.Unlock()
		e.hotHits.Add(1)
		e.learnHot(rel, pf, i, seg)
		return seg, false, nil
	}
	fl, joined := e.flights[key]
	if joined {
		fl.waiters++
		e.coalesced.Add(1)
	} else {
		fctx, cancel := context.WithCancel(e.ctx)
		fl = &flight{done: make(chan struct{}), waiters: 1, cancel: cancel}
		e.flights[key] = fl
		e.wg.Add(1)
		go e.run(fctx, fl, key, rel, pf, i, id, prio, fo)
	}
	e.mu.Unlock()

	select {
	case <-fl.done:
		if fl.err != nil && errors.Is(fl.err, context.Canceled) && ctx.Err() == nil && e.ctx.Err() == nil {
			return nil, true, fl.err
		}
		return fl.seg, false, fl.err
	case <-ctx.Done():
		e.mu.Lock()
		fl.waiters--
		if fl.waiters == 0 {
			// Nobody wants it any more; stop the fetch and let the next
			// request start afresh.
			fl.cancel()
			if e.flights[key] == fl {
				delete(e.flights, key)
			}
		}
		e.mu.Unlock()
		return nil, false, ctx.Err()
	}
}

// learnHot records a hot-buffer segment's exact range in pf. Usually the
// fetch that filled the buffer taught this release state already, but hot
// entries are keyed by NZB digest and can outlive the state that fetched
// them (a fetch finishing after its release was trimmed), so a fresh state
// for the same NZB would otherwise locate offsets from estimates forever.
func (e *Engine) learnHot(rel *release, pf *postedFile, i int, seg *segData) {
	if pf.slotKnown(i) {
		return
	}
	pf.learn(i, seg.begin, seg.end(), seg.fileSize, seg.total)
	rel.notePartSize(pf)
}

func (e *Engine) run(ctx context.Context, fl *flight, key segKey, rel *release, pf *postedFile, i int, id string, prio core.Priority, fo fetchOpts) {
	defer e.wg.Done()
	defer fl.cancel()
	seg, fromCache, err := e.load(ctx, pf, i, id, key.seg, prio, fo)
	if err == nil {
		pf.learn(i, seg.begin, seg.end(), seg.fileSize, seg.total)
		rel.notePartSize(pf)
		if !fo.noHot {
			e.hot.put(key, seg)
		}
	}
	persist := func() {
		if err == nil && !fromCache && fo.cacheID != "" && e.cache != nil {
			if perr := e.cache.Put(fo.cacheID, key.seg, encodeSegment(seg)); perr != nil {
				e.log.Debug("segment cache put failed", "release", fo.cacheID, "segment", key.seg, "err", perr)
			}
		}
	}
	if fo.noHot {
		// Background prefetch exists to fill the disk cache: finish the
		// write before reporting success.
		persist()
	}
	e.mu.Lock()
	fl.seg, fl.err = seg, err
	if e.flights[key] == fl {
		delete(e.flights, key)
	}
	e.mu.Unlock()
	close(fl.done)
	if !fo.noHot {
		// Readers are not kept waiting for the disk.
		persist()
	}
}

// load reads a segment from the disk cache or fetches it, with retries for
// transient errors.
func (e *Engine) load(ctx context.Context, pf *postedFile, i int, id string, global int, prio core.Priority, fo fetchOpts) (*segData, bool, error) {
	if fo.cacheID != "" && e.cache != nil {
		if b, ok := e.cache.Get(fo.cacheID, global); ok {
			if seg, ok := decodeSegment(b); ok {
				e.cacheHits.Add(1)
				return seg, true, nil
			}
		}
	}
	var lastErr error
	for attempt := 0; attempt <= e.opts.Retries; attempt++ {
		if attempt > 0 {
			t := time.NewTimer(time.Duration(attempt) * 150 * time.Millisecond)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, false, ctx.Err()
			case <-t.C:
			}
		}
		data, part, err := e.fetcher.BodyPart(ctx, id, pf.groups, prio)
		if err == nil {
			e.fetches.Add(1)
			e.fetchedBytes.Add(int64(len(data)))
			return e.normalize(pf, i, data, part), false, nil
		}
		if errors.Is(err, core.ErrArticleMissing) {
			e.missing.Add(1)
			return nil, false, fmt.Errorf("stream: %s part %d <%s>: %w", pf.Name(), i+1, id, err)
		}
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		lastErr = err
		e.log.Debug("article fetch failed", "file", pf.Name(), "part", i+1, "attempt", attempt+1, "err", err)
	}
	return nil, false, fmt.Errorf("stream: %s part %d: %w", pf.Name(), i+1, lastErr)
}

// maxArticleBytes is the most a damaged (short) article is zero-filled to.
// Real Usenet articles are well under a few MiB.
const maxArticleBytes = 32 << 20

// normalize places decoded article bytes in the posted file using the yEnc
// header, tolerating headers that are absent or inconsistent.
func (e *Engine) normalize(pf *postedFile, i int, data []byte, part core.ArticlePart) *segData {
	if part.Begin >= 1 && part.End >= part.Begin && part.End-part.Begin+1 > max(int64(len(data)), maxArticleBytes) {
		// The header is untrusted: a part claiming far more bytes than any
		// article carries would be zero-filled into a huge allocation.
		// Treat the whole header as absent.
		e.log.Warn("article yEnc part header claims an implausible size; ignoring it", "file", pf.Name(), "part", i+1,
			"got", len(data), "begin", part.Begin, "end", part.End)
		part = core.ArticlePart{Name: part.Name}
	}
	seg := &segData{fileSize: part.FileSize, total: part.Total, name: part.Name}
	if part.Begin >= 1 && part.End >= part.Begin {
		seg.begin = part.Begin - 1
		want := part.End - part.Begin + 1
		switch {
		case int64(len(data)) > want:
			e.log.Warn("article longer than its yEnc part header; truncating", "file", pf.Name(), "part", i+1, "got", len(data), "want", want)
			data = data[:want]
		case int64(len(data)) < want:
			// Keep offsets stable: a short (damaged) article is zero-filled so
			// the rest of the file stays aligned.
			e.log.Warn("article shorter than its yEnc part header; zero-filling", "file", pf.Name(), "part", i+1, "got", len(data), "want", want)
			padded := make([]byte, want)
			copy(padded, data)
			data = padded
		}
	} else {
		// No part header: trust the index estimate.
		seg.begin, _, _ = pf.rangeOf(i)
		if seg.fileSize == 0 && pf.numSlots() == 1 {
			seg.fileSize = int64(len(data))
		}
	}
	seg.data = data
	return seg
}

// Disk cache encoding: a small header so a cached segment restores its
// position in the posted file without the article.
//
//	"ETS1" | begin u64 | fileSize u64 | total u32 | nameLen u16 | name | data
const segMagic = "ETS1"

func encodeSegment(s *segData) []byte {
	name := s.name
	if len(name) > 1024 {
		name = name[:1024]
	}
	b := make([]byte, 4+8+8+4+2+len(name)+len(s.data))
	copy(b, segMagic)
	binary.LittleEndian.PutUint64(b[4:], uint64(s.begin))
	binary.LittleEndian.PutUint64(b[12:], uint64(s.fileSize))
	binary.LittleEndian.PutUint32(b[20:], uint32(s.total))
	binary.LittleEndian.PutUint16(b[24:], uint16(len(name)))
	copy(b[26:], name)
	copy(b[26+len(name):], s.data)
	return b
}

func decodeSegment(b []byte) (*segData, bool) {
	if len(b) < 26 || string(b[:4]) != segMagic {
		return nil, false
	}
	begin := int64(binary.LittleEndian.Uint64(b[4:]))
	size := int64(binary.LittleEndian.Uint64(b[12:]))
	total := int(binary.LittleEndian.Uint32(b[20:]))
	nl := int(binary.LittleEndian.Uint16(b[24:]))
	if begin < 0 || size < 0 || 26+nl > len(b) {
		return nil, false
	}
	return &segData{begin: begin, fileSize: size, total: total, name: string(b[26 : 26+nl]), data: b[26+nl:]}, true
}

// readFile fills p from posted file pf at off, fetching the covering
// segments in parallel. It returns the bytes copied; n < len(p) only with
// an error (io.EOF at the end of the file).
func (e *Engine) readFile(ctx context.Context, rel *release, pf *postedFile, p []byte, off int64, prio core.Priority, fo fetchOpts) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	size := pf.Size()
	if off >= size && pf.SizeKnown() {
		return 0, errEOF
	}
	// Prefetch every covering slot in parallel; assembly below waits on them.
	slots := pf.slotsFor(off, int64(len(p)))
	type res struct {
		seg *segData
		err error
	}
	results := make(map[int]chan res, len(slots))
	if len(slots) > 1 {
		sem := make(chan struct{}, e.opts.ReadParallel)
		for _, i := range slots {
			ch := make(chan res, 1)
			results[i] = ch
			go func(i int, ch chan res) {
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					ch <- res{err: ctx.Err()}
					return
				}
				seg, err := e.segment(ctx, rel, pf, i, prio, fo)
				<-sem
				ch <- res{seg, err}
			}(i, ch)
		}
	}
	get := func(i int) (*segData, error) {
		if ch, ok := results[i]; ok {
			delete(results, i)
			r := <-ch
			return r.seg, r.err
		}
		return e.segment(ctx, rel, pf, i, prio, fo)
	}

	n := 0
	cur := off
	misses := 0
	for n < len(p) {
		if pf.SizeKnown() && cur >= pf.Size() {
			return n, errEOF
		}
		i := pf.locate(cur)
		seg, err := get(i)
		if err != nil {
			return n, err
		}
		if cur < seg.begin || cur >= seg.end() {
			// The estimate was wrong; the index has been corrected by the
			// fetch, so locate again.
			misses++
			if misses > 8 {
				return n, fmt.Errorf("stream: %s: offset %d not found in part %d [%d,%d): %w", pf.Name(), cur, i+1, seg.begin, seg.end(), ErrCorrupt)
			}
			if i == pf.numSlots()-1 && cur >= seg.end() {
				return n, errEOF
			}
			continue
		}
		c := copy(p[n:], seg.data[cur-seg.begin:])
		n += c
		cur += int64(c)
	}
	return n, nil
}
