package stream

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/par2"
	"github.com/braedonsaunders/ether/internal/segcache"
)

func repairPost(t *testing.T) (*fakeFetcher, []byte, []byte) {
	t.Helper()
	data := make([]byte, 131123)
	for i := range data {
		data[i] = byte((i * 73) ^ (i >> 3) ^ 41)
	}
	copy(data, []byte{0x1a, 0x45, 0xdf, 0xa3})
	index, err := os.ReadFile("../par2/testdata/stream.par2")
	if err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob("../par2/testdata/stream.vol*.par2")
	if len(paths) != 1 {
		t.Fatal(paths)
	}
	volume, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeFetcher()
	doc := post(t, f, 5120, postFile{name: "repair.mkv", data: data}, postFile{name: "stream.par2", data: index}, postFile{name: "stream.vol0+4.par2", data: volume})
	return f, doc, data
}
func repairOpts() Options {
	return Options{Repair: config.Repair{Enabled: true, SummaryRows: 2, LiveWait: config.Duration(5 * time.Second), AheadWait: config.Duration(10 * time.Second)}, HotBufferBytes: 1 << 20, ReadAheadMin: 1, ReadAheadMax: 1}
}

func TestRepairParityDuplicateDoesNotHideFinalEquation(t *testing.T) {
	f, _, data := repairPost(t)
	index, err := os.ReadFile("../par2/testdata/stream.par2")
	if err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob("../par2/testdata/stream.vol*.par2")
	volume, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var duplicate []byte
	for off := 0; off+64 <= len(volume); {
		n := int(binary.LittleEndian.Uint64(volume[off+8:]))
		if string(volume[off+48:off+64]) == par2.TypeRecovery {
			duplicate = bytes.Clone(volume[off : off+n])
			break
		}
		off += n
	}
	if len(duplicate) == 0 {
		t.Fatal("oracle has no recovery packet")
	}
	volume = append(duplicate, volume...)
	doc := post(t, f, 1<<20, postFile{name: "repair.mkv", data: data}, postFile{name: "stream.par2", data: index}, postFile{name: "stream.vol0+4.par2", data: volume})
	opts := repairOpts()
	opts.Repair.SummaryRows = 0
	e := newTestEngine(t, f, nil, opts)
	if err := e.InitRepair(context.Background(), "duplicate", doc); err != nil {
		t.Fatal(err)
	}
	r, err := e.acquire(doc)
	if err != nil {
		t.Fatal(err)
	}
	defer e.release(r)
	packets := e.recoveryPackets(context.Background(), r, r.repair, core.PriorityReadahead, fetchOpts{noRepair: true}, 4<<20, e.fetchedBytes.Load(), 4)
	if len(packets) != 4 {
		t.Fatalf("duplicate hid an available equation: got %d, want 4", len(packets))
	}
}
func TestRepairSourceExactAndPersistent(t *testing.T) {
	for _, part := range []int{1, 4, 7, 26} {
		t.Run(string(rune('A'+part)), func(t *testing.T) {
			f, doc, want := repairPost(t)
			f.setMissing(postID("repair.mkv", part))
			cache, err := segcache.Open(t.TempDir(), 1<<20, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cache.Close()
			e := newTestEngine(t, f, cache, repairOpts())
			src := openSource(t, e, "repair", doc)
			got := make([]byte, len(want))
			n, err := src.ReadAt(got, 0)
			if err != nil || n != len(want) || !bytes.Equal(got, want) {
				t.Fatalf("n=%d err=%v exact=%v stats=%+v", n, err, bytes.Equal(got, want), e.Stats())
			}
			if e.Stats().RepairSuccesses != 1 {
				t.Fatal(e.Stats())
			}
			src.Close()
			e.Close()
			e2 := newTestEngine(t, f, cache, Options{})
			src2 := openSource(t, e2, "repair", doc)
			n, err = src2.ReadAt(got, 0)
			if err != nil || n != len(want) || !bytes.Equal(got, want) {
				t.Fatal("repaired data not durable", err)
			}
		})
	}
}
func TestRepairSharedConcurrentReads(t *testing.T) {
	f, doc, want := repairPost(t)
	f.setMissing(postID("repair.mkv", 4))
	e := newTestEngine(t, f, nil, repairOpts())
	src := openSource(t, e, "shared", doc)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 5120)
			_, err := src.ReadAt(buf, 15360)
			if err != nil || !bytes.Equal(buf, want[15360:20480]) {
				t.Errorf("read %v", err)
			}
		}()
	}
	wg.Wait()
	if e.Stats().RepairSuccesses != 1 {
		t.Fatal(e.Stats())
	}
}

func TestRepairReadaheadFinishesBeforeReaderReachesHole(t *testing.T) {
	f, doc, want := repairPost(t)
	f.setMissing(postID("repair.mkv", 8))
	opts := repairOpts()
	opts.ReadAheadMin, opts.ReadAheadMax = 64<<10, 64<<10
	e := newTestEngine(t, f, nil, opts)
	src := openSource(t, e, "ahead", doc)
	if _, err := src.ReadAt(make([]byte, 1024), 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for e.Stats().RepairSuccesses == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if e.Stats().RepairSuccesses == 0 {
		t.Fatal("readahead did not repair", e.Stats())
	}
	before := e.Stats().RepairAttempts
	b := make([]byte, 5120)
	n, err := src.ReadAt(b, 7*5120)
	if err != nil || n != len(b) || !bytes.Equal(b, want[7*5120:8*5120]) || e.Stats().RepairAttempts != before {
		t.Fatal("foreground needed another repair", n, err, e.Stats())
	}
}

func TestRepairForegroundKeepsUsefulBackgroundJob(t *testing.T) {
	f, doc, want := repairPost(t)
	f.setMissing(postID("repair.mkv", 8))
	opts := repairOpts()
	opts.ReadAheadMin, opts.ReadAheadMax = 64<<10, 64<<10
	e := newTestEngine(t, f, nil, opts)
	src := openSource(t, e, "join-ahead", doc)
	f.mu.Lock()
	f.latency = 20 * time.Millisecond
	f.mu.Unlock()
	if _, err := src.ReadAt(make([]byte, 1024), 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for e.Stats().RepairAttempts == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if e.Stats().RepairAttempts == 0 {
		t.Fatal("background job never started")
	}
	b := make([]byte, 5120)
	_, err := src.ReadAt(b, 7*5120)
	if err != nil || !bytes.Equal(b, want[7*5120:8*5120]) || e.Stats().RepairFailures != 0 {
		t.Fatal("foreground invalidated its own repair", err, e.Stats())
	}
}
func TestRepairInsufficientDoesNotConceal(t *testing.T) {
	f, doc, _ := repairPost(t)
	for i := 4; i <= 20; i++ {
		f.setMissing(postID("repair.mkv", i))
	}
	e := newTestEngine(t, f, nil, repairOpts())
	src := openSource(t, e, "insufficient", doc)
	buf := make([]byte, 5120)
	n, err := src.ReadAt(buf, 15360)
	if err == nil || n != 0 || !errors.Is(err, core.ErrArticleMissing) {
		t.Fatal(n, err)
	}
}
func TestRepairCloseCancelsWork(t *testing.T) {
	f, doc, _ := repairPost(t)
	f.setMissing(postID("repair.mkv", 4))
	e := newTestEngine(t, f, nil, repairOpts())
	src := openSource(t, e, "cancel", doc)
	f.mu.Lock()
	f.latency = 100 * time.Millisecond
	f.mu.Unlock()
	done := make(chan struct{})
	go func() { defer close(done); src.ReadAt(make([]byte, 5120), 15360) }()
	time.Sleep(10 * time.Millisecond)
	src.Close()
	e.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("repair did not cancel")
	}
}

func TestRepairBudgetAndSeekPreserveFailure(t *testing.T) {
	for _, seek := range []bool{false, true} {
		f, doc, _ := repairPost(t)
		opts := repairOpts()
		opts.Repair.MaxDownloadBytes = 1
		e := newTestEngine(t, f, nil, opts)
		if err := e.InitRepair(context.Background(), "bounded", doc); err != nil {
			t.Fatal(err)
		}
		f.setMissing(postID("repair.mkv", 4))
		r, err := e.acquire(doc)
		if err != nil {
			t.Fatal(err)
		}
		fo := fetchOpts{cacheID: "bounded"}
		if seek {
			fo.repairValid = func() bool { return false }
		}
		_, err = e.segment(context.Background(), r, r.files[0], 3, core.PriorityReadahead, fo)
		e.release(r)
		if !errors.Is(err, core.ErrArticleMissing) || e.Stats().RepairSuccesses != 0 {
			t.Fatal(err, e.Stats())
		}
	}
}
func TestRepairSummaryObservationAndBudget(t *testing.T) {
	f, doc, want := repairPost(t)
	e := newTestEngine(t, f, nil, repairOpts())
	if err := e.InitRepair(context.Background(), "summary", doc); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32768)
	if _, err := e.ReadPostedFile(context.Background(), "summary", doc, 0, 0, buf); err != nil || !bytes.Equal(buf, want[:32768]) {
		t.Fatal(err)
	}
	r, err := e.acquire(doc)
	if err != nil {
		t.Fatal(err)
	}
	defer e.release(r)
	r.repairMu.Lock()
	defer r.repairMu.Unlock()
	seen := 0
	for _, v := range r.repair.summaries.Seen {
		if v {
			seen++
		}
	}
	if seen < 2 {
		t.Fatalf("observed %d full slices", seen)
	}
}

func TestRepairLiveDeadlineIncludesMetadataLock(t *testing.T) {
	f, doc, _ := repairPost(t)
	f.setMissing(postID("repair.mkv", 4))
	opts := repairOpts()
	opts.Repair.LiveWait = config.Duration(10 * time.Millisecond)
	e := newTestEngine(t, f, nil, opts)
	r, err := e.acquire(doc)
	if err != nil {
		t.Fatal(err)
	}
	defer e.release(r)
	r.repairMu.Lock()
	defer r.repairMu.Unlock()
	start := time.Now()
	_, err = e.segment(context.Background(), r, r.files[0], 3, core.PriorityLive, fetchOpts{})
	if !errors.Is(err, core.ErrArticleMissing) || time.Since(start) > time.Second {
		t.Fatal("live request waited behind metadata lock", err, time.Since(start))
	}
}

func TestRepairedSpanCacheVersion(t *testing.T) {
	s := &segData{begin: 10, fileSize: 100, total: 4, name: "test", data: []byte("recovered"), repairedSpan: true}
	b := encodeSegment(s)
	if string(b[:4]) != "ETS3" {
		t.Fatal("old binaries would mistake a span for one article")
	}
	d, ok := decodeSegment(b)
	if !ok || !d.repairedSpan || d.rangeExact || !bytes.Equal(d.data, s.data) {
		t.Fatal(d, ok)
	}
	s.repairedSpan = false
	s.rangeExact = true
	b = encodeSegment(s)
	if string(b[:4]) != "ETS2" {
		t.Fatal("ordinary cache entries changed version")
	}
}

func TestRepairAdjacentArticlesAndUnknownTargetDamage(t *testing.T) {
	for _, parts := range [][]int{{4, 5}, {4, 6}} {
		f, doc, want := repairPost(t)
		for _, p := range parts {
			f.setMissing(postID("repair.mkv", p))
		}
		e := newTestEngine(t, f, nil, repairOpts())
		src := openSource(t, e, "adjacent", doc)
		buf := make([]byte, 20000)
		n, err := src.ReadAt(buf, 15360)
		if err != nil || n != len(buf) || !bytes.Equal(buf, want[15360:35360]) {
			t.Fatal(parts, n, err)
		}
	}
}
func TestRepairArticleSpanningSixSlices(t *testing.T) {
	_, _, want := repairPost(t)
	index, err := os.ReadFile("../par2/testdata/wide.par2")
	if err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob("../par2/testdata/wide.vol*.par2")
	if len(paths) != 1 {
		t.Fatal(paths)
	}
	vol, _ := os.ReadFile(paths[0])
	f := newFakeFetcher()
	doc := post(t, f, 20000, postFile{name: "repair.mkv", data: want}, postFile{name: "wide.par2", data: index}, postFile{name: "wide.vol00+16.par2", data: vol})
	f.setMissing(postID("repair.mkv", 4))
	f.setMissing(postID("repair.mkv", 6))
	e := newTestEngine(t, f, nil, repairOpts())
	src := openSource(t, e, "wide", doc)
	b := make([]byte, 20000)
	n, err := src.ReadAt(b, 60000)
	if err != nil || n != len(b) || !bytes.Equal(b, want[60000:80000]) {
		t.Fatal(n, err, e.Stats())
	}
	before := e.Stats().RepairInputBytes
	n, err = src.ReadAt(b, 100000)
	if err != nil || n != len(b) || !bytes.Equal(b, want[100000:120000]) {
		t.Fatal(n, err, e.Stats())
	}
	if e.Stats().RepairInputBytes != before {
		t.Fatal("downloaded repair dependencies again", e.Stats())
	}
}
func TestRepairSummariesAvoidEvictedSourceRedownload(t *testing.T) {
	f, doc, want := repairPost(t)
	opts := repairOpts()
	opts.HotBufferBytes = 16 << 10
	e := newTestEngine(t, f, nil, opts)
	if err := e.InitRepair(context.Background(), "summaries", doc); err != nil {
		t.Fatal(err)
	}
	// Sequential small reads ensure complete slice observations before eviction.
	for off := 0; off < 65536; off += 4096 {
		if _, err := e.ReadPostedFile(context.Background(), "summaries", doc, 0, int64(off), make([]byte, 4096)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 12; i++ {
		f.setMissing(postID("repair.mkv", i))
	}
	f.setMissing(postID("repair.mkv", 20))
	b := make([]byte, 5120)
	n, err := e.ReadPostedFile(context.Background(), "summaries", doc, 0, 19*5120, b)
	if err != nil || n != len(b) || !bytes.Equal(b, want[19*5120:20*5120]) || e.Stats().RepairSummaryBytes == 0 {
		t.Fatal(n, err, e.Stats())
	}
}

// A repaired span covering several article slots is charged once in the hot
// buffer and stored once on disk, with the other slots referencing it.
func TestRepairedSpanStoredOnce(t *testing.T) {
	f, doc, want := repairPost(t)
	f.setMissing(postID("repair.mkv", 4))
	f.setMissing(postID("repair.mkv", 5))
	cache, err := segcache.Open(t.TempDir(), 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	e := newTestEngine(t, f, cache, repairOpts())
	src := openSource(t, e, "span", doc)
	buf := make([]byte, 20000)
	if n, err := src.ReadAt(buf, 15360); err != nil || n != len(buf) || !bytes.Equal(buf, want[15360:35360]) {
		t.Fatal(n, err, e.Stats())
	}

	e.hot.mu.Lock()
	unique := map[*segData]bool{}
	var charged int64
	spanKeys := 0
	for _, el := range e.hot.items {
		seg := el.Value.(*hotEntry).seg
		if seg.repairedSpan {
			spanKeys++
		}
		if !unique[seg] {
			unique[seg] = true
			charged += int64(len(seg.data))
		}
	}
	used := e.hot.used
	e.hot.mu.Unlock()
	if spanKeys < 2 {
		t.Fatalf("expected a repaired span held under several slots, got %d", spanKeys)
	}
	if used != charged {
		t.Fatalf("hot buffer charges %d bytes for %d bytes of distinct segments", used, charged)
	}

	src.Close()
	e.Close()
	refs := 0
	for g := range 64 {
		if b, ok := cache.Get("span", g); ok && string(b[:4]) == spanRefMagic {
			refs++
		}
	}
	if refs == 0 {
		t.Fatal("repaired span was not stored by reference")
	}
	e2 := newTestEngine(t, f, cache, Options{})
	src2 := openSource(t, e2, "span", doc)
	if n, err := src2.ReadAt(buf, 15360); err != nil || n != len(buf) || !bytes.Equal(buf, want[15360:35360]) {
		t.Fatal("repaired span not durable", n, err)
	}
}

func TestHotBufferEvictsSharedSegmentOnLastKey(t *testing.T) {
	h := newHotBuffer(100)
	span := &segData{data: make([]byte, 60), repairedSpan: true}
	h.put(segKey{"r", 1}, span)
	h.put(segKey{"r", 2}, span)
	if h.usage() != 60 {
		t.Fatalf("shared span charged %d", h.usage())
	}
	h.put(segKey{"r", 3}, &segData{data: make([]byte, 30)})
	if h.usage() != 90 || !h.has(segKey{"r", 1}) {
		t.Fatalf("usage=%d", h.usage())
	}
	h.put(segKey{"r", 4}, &segData{data: make([]byte, 30)})
	// Evicting key 1 alone frees nothing; key 2 must go too.
	if h.has(segKey{"r", 1}) || h.has(segKey{"r", 2}) || h.usage() != 60 {
		t.Fatalf("usage=%d", h.usage())
	}
	h.dropRelease("r")
	if h.usage() != 0 || len(h.refs) != 0 {
		t.Fatalf("usage=%d refs=%d", h.usage(), len(h.refs))
	}
}
