package stream

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/nzb"
	"github.com/braedonsaunders/ether/internal/par2"
)

const repairMaxSlice = 16 << 20
const repairMemoryBudget = 128 << 20

// Serialized per release; only one reconstruction across the engine competes
// with playback. Source close cancels readahead repairs and joins their workers.
type repairSession struct {
	summaryRows    int
	summaryBytes   int64
	summaryTime    time.Duration
	summaryStopped bool
	blocksByFile   map[*postedFile][2]int
	layout         *par2.Layout
	field          *par2.Field
	files          map[par2.ID]*postedFile
	summaries      *par2.Summaries
	pending        map[int]*repairPending
	reserved       int64
	retryAfter     map[int]time.Time
	fixed          map[int][]byte // verified small slices reconstructed incidentally
	fixedLimit     int
}
type repairPending struct {
	data  []byte
	spans []damageSpan
}

func (e *Engine) segment(ctx context.Context, r *release, pf *postedFile, i int, prio core.Priority, fo fetchOpts) (*segData, error) {
	seg, err := e.segmentRaw(ctx, r, pf, i, prio, fo)
	if fo.noRepair || !e.repairOptions().Enabled {
		return seg, err
	}
	if err == nil {
		if !fo.deferObserve {
			e.observeRepair(r, pf, seg)
		}
		return seg, nil
	}
	if !repairErasure(err) || ctx.Err() != nil || r.repairParity(pf) {
		return nil, err
	}
	if fixed, ok := e.repairArticle(ctx, r, pf, i, prio, fo); ok {
		return fixed, nil
	}
	return nil, err
}
func repairErasure(err error) bool {
	return errors.Is(err, core.ErrArticleMissing) || errors.Is(err, core.ErrArticleCorrupt) || errors.Is(err, ErrCorrupt)
}

func (r *release) repairParity(pf *postedFile) bool {
	r.infoMu.RLock()
	defer r.infoMu.RUnlock()
	return pf.file.Info.Kind == nzb.KindPar2
}

// prepareRepair does no speculative full-media download. Metadata remains
// bounded; small recovery probes may choose summary equations in advance.
// Caller holds r.repairMu. Ambiguous file identities disable repair.
func (e *Engine) prepareRepair(ctx context.Context, r *release, prio core.Priority, fo fetchOpts) *repairSession {
	if r.repair != nil {
		return r.repair
	}
	fo.noRepair = true
	set := r.loadPar2(ctx, prio, fo, r.par2IndexFile())
	if set == nil {
		return nil
	}
	var layout *par2.Layout
	for id := range set.Main {
		l, err := set.RecoveryLayout(id, repairMaxSlice)
		if err == nil {
			if layout != nil {
				return nil
			}
			layout = l
		}
	}
	if layout == nil {
		return nil
	}
	files := map[par2.ID]*postedFile{}
	usedFiles := map[*postedFile]bool{}
	probed := map[*postedFile]bool{}
	identified := map[*postedFile]*par2.File{}
	for _, b := range layout.Blocks {
		if files[b.File.ID] != nil {
			continue
		}
		var match *postedFile
		for _, pf := range r.files {
			if pf.Name() == b.File.BaseName() {
				if match != nil {
					return nil
				}
				match = pf
			}
		}
		if match == nil {
			// Resolve unmatched obfuscated files in one bounded parallel batch.
			// Serial per-file header requests leave most NNTP connections idle.
			var candidates []*postedFile
			for _, pf := range r.files {
				if probed[pf] || usedFiles[pf] || r.repairParity(pf) || (pf.SizeKnown() && pf.Size() != b.File.Length) {
					continue
				}
				if len(probed) >= max(1, e.opts.MaxProbes) {
					break
				}
				probed[pf] = true
				candidates = append(candidates, pf)
			}
			ids := make([]*par2.File, len(candidates))
			builder := &builder{e: e, rel: r}
			if err := builder.parallel(ctx, len(candidates), func(ctx context.Context, i int) error {
				head, err := e.segmentRaw(ctx, r, candidates[i], 0, prio, fo)
				if err == nil {
					ids[i] = identifyByPar2(set, candidates[i], head)
				}
				return nil
			}); err != nil {
				return nil
			}
			for i, id := range ids {
				if id != nil {
					identified[candidates[i]] = id
					builder.nameFromPar2(candidates[i], id)
				}
			}
			for pf, id := range identified {
				if id.ID == b.File.ID {
					if match != nil {
						return nil
					}
					match = pf
				}
			}
		}
		if match == nil || usedFiles[match] {
			return nil
		}
		usedFiles[match] = true
		files[b.File.ID] = match
	}
	rows := min(16, int((64<<20)/layout.SliceSize), max(0, e.repairOptions().SummaryRows))
	if e.repairOptions().AdaptiveSummaries {
		rows = min(rows, 2)
		if len(layout.Blocks) < 4*rows {
			rows = 0
		}
	}
	// Retained summaries and two partial slices. The single global repair job
	// separately bounds targets/parity to 64 MiB each and packet scanning to 64 MiB.
	reserve := int64(rows+2)*layout.SliceSize + 1<<20
	fixedLimit := 0
	if layout.SliceSize <= 1<<20 {
		fixedLimit = int((32 << 20) / layout.SliceSize)
		reserve += 32 << 20
	}
	if e.repairMemory.Load()+reserve > repairMemoryBudget {
		e.pruneIdleRepair(r)
	}
	for {
		old := e.repairMemory.Load()
		if old+reserve > repairMemoryBudget {
			return nil
		}
		if e.repairMemory.CompareAndSwap(old, old+reserve) {
			break
		}
	}
	s := &repairSession{layout: layout, field: par2.NewField(), files: files, pending: map[int]*repairPending{}, reserved: reserve, retryAfter: map[int]time.Time{}, fixed: map[int][]byte{}, fixedLimit: fixedLimit}
	s.summaryRows = rows
	s.blocksByFile = map[*postedFile][2]int{}
	for i, b := range layout.Blocks {
		pf := files[b.File.ID]
		bounds, ok := s.blocksByFile[pf]
		if !ok {
			bounds[0] = i
		}
		bounds[1] = i + 1
		s.blocksByFile[pf] = bounds
	}
	if rows > 0 && !e.repairOptions().AdaptiveSummaries {
		exps := make([]uint32, rows)
		for i := range exps {
			exps[i] = uint32(i)
		}
		// Small posted slices often fit multiple recovery packets per article.
		// Prefer equations actually observed available, within a 4 MiB probe.
		if layout.SliceSize <= 1<<20 {
			packets := e.recoveryPackets(ctx, r, s, prio, fo, 4<<20, e.fetchedBytes.Load(), rows)
			if len(packets) > 0 {
				exps = nil
				for _, p := range packets {
					exps = append(exps, p.Exponent)
				}
			}
		}
		s.summaries, _ = par2.NewSummaries(layout, s.field, exps, int64(rows)*layout.SliceSize)
	}
	r.repair = s
	return s
}

// Observation never blocks a live reader behind an active reconstruction.
// Missing observations affect efficiency only: coverage remains explicit.
func (e *Engine) observeRepair(r *release, pf *postedFile, seg *segData) {
	if !seg.rangeExact || !r.repairMu.TryLock() {
		return
	}
	defer r.repairMu.Unlock()
	s := r.repair
	if s == nil || s.summaries == nil || s.summaryStopped {
		return
	}
	bounds, ok := s.blocksByFile[pf]
	if !ok || len(seg.data) == 0 {
		return
	}
	start := max(bounds[0], bounds[0]+int(seg.begin/s.layout.SliceSize))
	end := min(bounds[1], bounds[0]+int((seg.end()-1)/s.layout.SliceSize)+1)
	for index := start; index < end; index++ {
		b := s.layout.Blocks[index]
		if s.summaries.Seen[index] {
			continue
		}
		lo, hi := max(seg.begin, b.Offset), min(seg.end(), b.Offset+b.Length)
		if hi <= lo {
			continue
		}
		p := s.pending[index]
		if p == nil {
			if len(s.pending) >= 2 { // Drop the oldest partial, not a verified summary.
				oldest := int(^uint(0) >> 1)
				for n := range s.pending {
					oldest = min(oldest, n)
				}
				delete(s.pending, oldest)
			}
			p = &repairPending{data: make([]byte, s.layout.SliceSize)}
			s.pending[index] = p
		}
		copy(p.data[lo-b.Offset:hi-b.Offset], seg.data[lo-seg.begin:hi-seg.begin])
		p.spans = append(p.spans, damageSpan{lo - b.Offset, hi - b.Offset})
		sort.Slice(p.spans, func(i, j int) bool { return p.spans[i].begin < p.spans[j].begin })
		merged := p.spans[:0]
		for _, span := range p.spans {
			if len(merged) > 0 && span.begin <= merged[len(merged)-1].end {
				merged[len(merged)-1].end = max(merged[len(merged)-1].end, span.end)
			} else {
				merged = append(merged, span)
			}
		}
		p.spans = merged
		if len(merged) == 1 && merged[0].begin == 0 && merged[0].end == b.Length {
			started := time.Now()
			if s.summaries.Observe(index, p.data) {
				s.summaryBytes += int64(len(p.data))
				s.summaryTime += time.Since(started)
			}
			if e.repairOptions().AdaptiveSummaries && s.summaryBytes >= 16<<20 {
				e.repairRate.mu.Lock()
				rate := e.repairRate.rate
				e.repairRate.mu.Unlock()
				if rate > 0 && s.summaryTime.Seconds() > float64(s.summaryBytes)/rate*.05 {
					s.summaryStopped = true
					clear(s.pending)
				}
			}
			delete(s.pending, index)
		}
	}
}

func (e *Engine) repairArticle(parent context.Context, r *release, pf *postedFile, slot int, prio core.Priority, fo fetchOpts) (result *segData, repaired bool) {
	ctx, planned, progress, finish := e.watchRepair(parent, pf, slot, prio, fo)
	defer func() { finish(repaired) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopEngineCancel := context.AfterFunc(e.ctx, cancel)
	defer stopEngineCancel()
	select {
	case e.repairGate <- struct{}{}:
		defer func() { <-e.repairGate }()
	case <-ctx.Done():
		return nil, false
	}
	if fo.repairValid != nil {
		done := make(chan struct{})
		go func() {
			defer close(done)
			tick := time.NewTicker(25 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					if !fo.repairValid() {
						cancel()
						return
					}
				}
			}
		}()
		defer func() { cancel(); <-done }()
	}
	// If another request repaired it while we waited, use its published cache.
	if seg, ok := e.hot.get(segKey{r.digest, pf.segBase + slot}); ok {
		return seg, true
	}
	for !r.repairMu.TryLock() {
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false
		case <-timer.C:
		}
	}
	defer r.repairMu.Unlock()
	if ctx.Err() != nil {
		return nil, false
	}
	prio = max(prio, core.PriorityReadahead)
	if fo.repairValid != nil && !fo.repairValid() {
		return nil, false
	}
	s := e.prepareRepair(ctx, r, prio, fo)
	if s == nil {
		return nil, false
	}
	if time.Now().Before(s.retryAfter[pf.segBase+slot]) {
		return nil, false
	}
	e.repairAttempts.Add(1)
	success := false
	defer func() {
		if !success {
			e.repairFailures.Add(1)
			if len(s.retryAfter) >= 64 {
				clear(s.retryAfter)
			}
			s.retryAfter[pf.segBase+slot] = time.Now().Add(5 * time.Second)
		}
	}()
	fo.noRepair = true
	// Establish exact boundaries from real neighbours; guessed offsets never
	// authorize publishing synthetic source bytes, even if slice hashes match.
	var begin, end int64
	first, last := slot, slot
	for j := slot - 1; j >= 0; j-- {
		prev, err := e.segmentRaw(ctx, r, pf, j, prio, fo)
		if err == nil && prev.rangeExact {
			begin = prev.end()
			break
		}
		if !repairErasure(err) || slot-j >= 16 {
			return nil, false
		}
		first = j
	}
	for j := slot + 1; j < pf.numSlots(); j++ {
		next, err := e.segmentRaw(ctx, r, pf, j, prio, fo)
		if err == nil && next.rangeExact {
			end = next.begin
			break
		}
		if !repairErasure(err) || j-slot >= 16 {
			return nil, false
		}
		last = j
	}
	if last == pf.numSlots()-1 {
		if !pf.SizeKnown() {
			return nil, false
		}
		end = pf.Size()
	}

	if end <= begin || end-begin > maxArticleBytes {
		return nil, false
	}
	var holes []par2.Hole
	var file *par2.File
	for j, b := range s.layout.Blocks {
		if s.files[b.File.ID] != pf {
			continue
		}
		file = b.File
		lo, hi := max(begin, b.Offset), min(end, b.Offset+b.Length)
		if hi > lo {
			holes = append(holes, par2.Hole{Block: j, Begin: lo - b.Offset, End: hi - b.Offset})
		}
	}
	if len(holes) == 0 || len(holes) > min(64, int((64<<20)/s.layout.SliceSize)) || file == nil || end > file.Length {
		return nil, false
	}
	fixed := map[int][]byte{}
	for _, h := range holes {
		if d := s.fixed[h.Block]; d != nil {
			fixed[h.Block] = d
		}
	}
	if len(fixed) != len(holes) {
		maxBytes := e.repairOptions().MaxDownloadBytes
		if maxBytes <= 0 {
			maxBytes = 512 << 20
		}
		startBytes := e.fetchedBytes.Load()
		bulk := &bulkReader{e: e, r: r, prio: prio, fo: fo}
		read := func(ctx context.Context, index int, off int64, dst []byte) error {
			if !e.repairOptions().Enabled || (fo.repairValid != nil && !fo.repairValid()) {
				return context.Canceled
			}
			if e.fetchedBytes.Load()-startBytes >= maxBytes {
				return errors.New("repair download budget exhausted")
			}
			if d := s.fixed[index]; d != nil {
				copy(dst, d[off:off+int64(len(dst))])
				return nil
			}
			b := s.layout.Blocks[index]
			clear(dst)
			if off >= b.Length {
				return nil
			}
			n := min(int64(len(dst)), b.Length-off)
			got, err := bulk.readRange(ctx, s.files[b.File.ID], dst[:n], b.Offset+off)
			if repairErasure(err) {
				return par2.ErrErasure
			}
			if err != nil {
				return err
			}
			if int64(got) != n {
				return par2.ErrErasure
			}
			progress(n)
			return nil
		}
		wanted := max(len(holes), min(4, int((64<<20)/s.layout.SliceSize)))
		if s.layout.SliceSize <= pf.learnedPartSize() {
			wanted = min(64, max(wanted, 4*int((pf.learnedPartSize()+s.layout.SliceSize-1)/s.layout.SliceSize)))
		}
		if s.summaries != nil && !e.repairOptions().AdaptiveSummaries {
			wanted = max(wanted, len(s.summaries.Exponents))
		}
		whole := e.repairOptions().WholeSlices || end-begin >= s.layout.SliceSize || pf.learnedPartSize() >= s.layout.SliceSize
		planned(e.estimateRepair(r, s, holes, whole, wanted, fo))
		parity := e.recoveryPackets(ctx, r, s, prio, fo, maxBytes, startBytes, wanted)
		if len(parity) == 0 {
			return nil, false
		}
		// Adaptive summaries start only after actual damage, using equations
		// already fetched for this solve. Healthy playback pays no parity I/O.
		if e.repairOptions().AdaptiveSummaries && s.summaries == nil && s.summaryRows > 0 {
			var exps []uint32
			for _, p := range parity[:min(s.summaryRows, len(parity))] {
				exps = append(exps, p.Exponent)
			}
			s.summaries, _ = par2.NewSummaries(s.layout, s.field, exps, int64(s.summaryRows)*s.layout.SliceSize)
		}
		summaries := s.summaries
		if e.repairOptions().AdaptiveSummaries && summaries != nil {
			covered := 0
			for _, seen := range summaries.Seen {
				if seen {
					covered++
				}
			}
			if int64(covered)*s.layout.SliceSize < max(int64(8<<20), int64(4*len(summaries.Exponents))*s.layout.SliceSize) {
				summaries = nil
			}
		}
		selected := parity
		if summaries != nil {
			var matching []par2.Recovery
			for _, p := range parity {
				for _, exp := range summaries.Exponents {
					if exp%65535 == p.Exponent%65535 {
						matching = append(matching, p)
						break
					}
				}
			}
			if len(matching) >= len(holes) {
				selected = matching
			}
		}
		opts := par2.RepairOptions{WholeSlices: whole, ChunkBytes: 1 << 20, MaxTargets: min(64, int((64<<20)/s.layout.SliceSize))}
		var stats par2.RepairStats
		var err error
		fixed, stats, err = s.layout.Recover(ctx, s.field, holes, read, selected, summaries, opts)
		if errors.Is(err, par2.ErrErasure) && !opts.WholeSlices && ctx.Err() == nil {
			opts.WholeSlices = true
			planned(e.estimateRepair(r, s, holes, true, len(parity), fo))
			var extra par2.RepairStats
			fixed, extra, err = s.layout.Recover(ctx, s.field, holes, read, parity, nil, opts)
			stats.InputBytes += extra.InputBytes
		}
		if errors.Is(err, par2.ErrInsufficient) && len(selected) < len(parity) && ctx.Err() == nil {
			planned(e.estimateRepair(r, s, holes, opts.WholeSlices, len(parity), fo))
			var extra par2.RepairStats
			fixed, extra, err = s.layout.Recover(ctx, s.field, holes, read, parity, nil, opts)
			stats.InputBytes += extra.InputBytes
			stats.SummaryBytes += extra.SummaryBytes
		}
		for errors.Is(err, par2.ErrInsufficient) && ctx.Err() == nil {
			limit := min(64, int((64<<20)/s.layout.SliceSize))
			if len(parity) < limit {
				previous := len(parity)
				parity = e.recoveryPackets(ctx, r, s, prio, fo, maxBytes, startBytes, min(limit, max(previous+1, previous*2)))
				if len(parity) <= previous {
					break
				}
				var extra par2.RepairStats
				planned(e.estimateRepair(r, s, holes, opts.WholeSlices, len(parity), fo))
				fixed, extra, err = s.layout.Recover(ctx, s.field, holes, read, parity, nil, opts)
				stats.InputBytes += extra.InputBytes
			} else {
				break
			}
		}
		e.repairInputBytes.Add(stats.InputBytes)
		e.repairSummaryBytes.Add(stats.SummaryBytes)
		if err != nil {
			reason := err.Error()
			e.repairLast.Store(&reason)
			e.log.Debug("repair did not finish", "reason", err)
			return nil, false
		}
		if s.fixedLimit > 0 {
			for b, d := range fixed {
				if len(s.fixed) >= s.fixedLimit {
					clear(s.fixed)
				}
				s.fixed[b] = d
			}
		}
	}
	data := make([]byte, end-begin)
	for _, h := range holes {
		b := s.layout.Blocks[h.Block]
		lo, hi := max(begin, b.Offset), min(end, b.Offset+b.Length)
		copy(data[lo-begin:hi-begin], fixed[h.Block][lo-b.Offset:hi-b.Offset])
	}
	if ctx.Err() != nil || (fo.repairValid != nil && !fo.repairValid()) {
		return nil, false
	}
	seg := &segData{data: data, begin: begin, fileSize: file.Length, total: pf.numSlots(), name: pf.Name(), rangeExact: first == last, repairedSpan: first != last}
	if first == last {
		pf.learn(slot, begin, end, file.Length, pf.numSlots())
	}
	for j := first; j <= last; j++ {
		e.hot.put(segKey{r.digest, pf.segBase + j}, seg)

	}

	e.persistRepair(fo.cacheID, pf.segBase+first, pf.segBase+last, seg)
	if s.summaries != nil {
		for b, d := range fixed {
			s.summaries.Observe(b, d)
			delete(s.pending, b)
		}
	}
	success = true
	e.repairSuccesses.Add(1)
	return seg, true
}

// Cache writes must not extend a live repair deadline. At most one repair
// persistence worker holds additional bytes; engine Close joins it, just like
// ordinary article persistence. Busy/failed persistence leaves valid hot data.
func (e *Engine) persistRepair(cacheID string, first, last int, seg *segData) {
	if e.cache == nil || cacheID == "" {
		return
	}
	select {
	case e.repairPersist <- struct{}{}:
	default:
		return
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		<-e.repairPersist
		return
	}
	e.wg.Add(1)
	e.mu.Unlock()
	go func() {
		defer e.wg.Done()
		defer func() { <-e.repairPersist }()
		data := encodeSegment(seg)
		for j := first; j <= last; j++ {
			if err := e.cache.Put(cacheID, j, data); err != nil {
				e.log.Debug("repair cache write failed", "err", err)
			}
		}
	}()
}

func (e *Engine) recoveryPackets(ctx context.Context, r *release, s *repairSession, prio core.Priority, fo fetchOpts, maxBytes, start int64, limit int) []par2.Recovery {
	var files []*postedFile
	for _, pf := range r.files {
		if r.repairParity(pf) {
			files = append(files, pf)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].file.Bytes() < files[j].file.Bytes() })
	var out []par2.Recovery
	seen := map[uint32]bool{}
	// Scan bounded packets rather than loading whole volumes. A missing article
	// loses only packets crossing that gap, not every earlier valid packet.
	scan := par2.RecoveryScanner{ID: s.layout.ID, SliceSize: s.layout.SliceSize}
	bulk := &bulkReader{e: e, r: r, prio: prio, fo: fo}
	for _, pf := range files {
		scan.Reset()
		var nextOffset int64
		stop := false
		bulk.articles(ctx, pf, func(seg *segData, err error) bool {
			if ctx.Err() != nil || !e.repairOptions().Enabled || (fo.repairValid != nil && !fo.repairValid()) || e.fetchedBytes.Load()-start >= maxBytes {
				stop = true
				return false
			}
			if err != nil || !seg.rangeExact {
				scan.Reset()
				nextOffset = -1
				return true
			}
			if nextOffset >= 0 && seg.begin != nextOffset {
				scan.Reset()
			}
			nextOffset = seg.end()
			// Duplicate equations consume scanner output slots, not useful
			// parity slots. Drain buffered packets even at the final article.
			for data := seg.data; ; data = nil {
				packets := scan.Push(data, limit-len(out))
				if len(packets) == 0 {
					break
				}
				for _, p := range packets {
					if !seen[p.Exponent%65535] {
						seen[p.Exponent%65535] = true
						out = append(out, p)
						if len(out) >= limit {
							stop = true
							return false
						}
					}
				}
			}
			return true
		})
		if stop {
			return out
		}
	}

	return out
}

// InitRepair primes bounded metadata and summaries without repairing anything.
// It is also used by the explicit live benchmark. Normal Open calls it with a
// short context; missing PAR2 remains optional.
func (e *Engine) InitRepair(ctx context.Context, releaseID string, nzbData []byte) error {
	if err := e.ctx.Err(); err != nil {
		return ErrClosed
	}
	if !e.repairOptions().Enabled {
		return nil
	}
	r, err := e.acquire(nzbData)
	if err != nil {
		return err
	}
	defer e.release(r)
	r.repairMu.Lock()
	defer r.repairMu.Unlock()
	if e.prepareRepair(ctx, r, core.PriorityReadahead, fetchOpts{cacheID: releaseID, noRepair: true}) == nil {
		return fmt.Errorf("repair metadata unavailable")
	}
	return nil
}

// ReadPostedFile exposes the same verified article/repair path for diagnostics
// and full-download comparisons, before archive extraction. fileIndex is the
// NZB's file index. It does not create a Plex session or write watch history.
func (e *Engine) ReadPostedFile(ctx context.Context, releaseID string, nzbData []byte, fileIndex int, off int64, dst []byte) (int, error) {
	if e.ctx.Err() != nil {
		return 0, ErrClosed
	}
	r, err := e.acquire(nzbData)
	if err != nil {
		return 0, err
	}
	defer e.release(r)
	if fileIndex < 0 || fileIndex >= len(r.files) || off < 0 {
		return 0, errors.New("invalid posted file range")
	}
	return e.readFile(ctx, r, r.files[fileIndex], dst, off, core.PriorityReadahead, fetchOpts{cacheID: releaseID})
}

func (e *Engine) pruneIdleRepair(except *release) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, el := range e.releases {
		r := el.Value.(*release)
		if r == except || r.refs > 0 || !r.repairMu.TryLock() {
			continue
		}
		if r.repair != nil {
			e.repairMemory.Add(-r.repair.reserved)
			r.repair = nil
		}
		r.repairMu.Unlock()
	}
}

// UpdateRepair applies owner settings to new work without mutating snapshots
// held by concurrent readers. Existing dependency reads observe disablement.
func (e *Engine) UpdateRepair(cfg config.Repair) { e.repairConfig.Store(&cfg) }
func (e *Engine) repairOptions() config.Repair {
	if p := e.repairConfig.Load(); p != nil {
		return *p
	}
	return e.opts.Repair
}
