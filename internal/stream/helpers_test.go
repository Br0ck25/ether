package stream

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// fakeFetcher is an in-memory core.ArticleFetcher. Articles are stored
// decoded with the yEnc part header the real fetcher would report.
type bodyCallKey struct {
	id       string
	priority core.Priority
}

type fakeFetcher struct {
	mu            sync.Mutex
	articles      map[string]fakeArticle
	missing       map[string]bool // listed in the NZB but gone from every provider
	failures      map[string]int  // transient failures before success
	bodyCalls     map[string]int
	bodyPrioCalls map[bodyCallKey]int
	prioCalls     map[core.Priority]int
	statCalls     int

	latency  time.Duration
	failAll  bool // every Body fails (to prove reads come from cache)
	noHeader bool // report no yEnc part header

	inflight, maxInflight atomic.Int64
}

type fakeArticle struct {
	data []byte
	part core.ArticlePart
}

func newFakeFetcher() *fakeFetcher {
	return &fakeFetcher{
		articles:      map[string]fakeArticle{},
		missing:       map[string]bool{},
		failures:      map[string]int{},
		bodyCalls:     map[string]int{},
		bodyPrioCalls: map[bodyCallKey]int{},
		prioCalls:     map[core.Priority]int{},
	}
}

func (f *fakeFetcher) Body(ctx context.Context, id string, groups []string, prio core.Priority) ([]byte, error) {
	b, _, err := f.BodyPart(ctx, id, groups, prio)
	return b, err
}

func (f *fakeFetcher) BodyPart(ctx context.Context, id string, _ []string, prio core.Priority) ([]byte, core.ArticlePart, error) {
	cur := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		m := f.maxInflight.Load()
		if cur <= m || f.maxInflight.CompareAndSwap(m, cur) {
			break
		}
	}
	f.mu.Lock()
	f.bodyCalls[id]++
	f.bodyPrioCalls[bodyCallKey{id, prio}]++
	f.prioCalls[prio]++
	lat := f.latency
	f.mu.Unlock()
	if lat > 0 {
		t := time.NewTimer(lat)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, core.ArticlePart{}, ctx.Err()
		case <-t.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, core.ArticlePart{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAll {
		return nil, core.ArticlePart{}, fmt.Errorf("fake: network down")
	}
	if f.failures[id] > 0 {
		f.failures[id]--
		return nil, core.ArticlePart{}, fmt.Errorf("fake: transient failure")
	}
	a, ok := f.articles[id]
	if !ok || f.missing[id] {
		return nil, core.ArticlePart{}, fmt.Errorf("fake <%s>: %w", id, core.ErrArticleMissing)
	}
	part := a.part
	if f.noHeader {
		part = core.ArticlePart{}
	}
	return bytes.Clone(a.data), part, nil
}

func (f *fakeFetcher) Stat(ctx context.Context, id string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statCalls++
	_, ok := f.articles[id]
	return ok && !f.missing[id], nil
}

func (f *fakeFetcher) calls(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodyCalls[id]
}

// maxLaneCalls keeps coalescing assertions strict within each scheduling
// lane, while allowing a foreground read to bypass queued speculative work.
func (f *fakeFetcher) maxLaneCalls(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for key, calls := range f.bodyPrioCalls {
		if key.id == id {
			n = max(n, calls)
		}
	}
	return n
}

func (f *fakeFetcher) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.bodyCalls {
		n += c
	}
	return n
}

func (f *fakeFetcher) prio(p core.Priority) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prioCalls[p]
}

func (f *fakeFetcher) resetCounts() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodyCalls = map[string]int{}
	f.bodyPrioCalls = map[bodyCallKey]int{}
	f.prioCalls = map[core.Priority]int{}
	f.statCalls = 0
}

func (f *fakeFetcher) setMissing(id string) {
	f.mu.Lock()
	f.missing[id] = true
	f.mu.Unlock()
}

// postFile is one file to post.
type postFile struct {
	name    string // subject name
	yName   string // yEnc name, default name
	data    []byte
	parts   []int // explicit part sizes (must sum to len(data)); default uniform partSize
	subject string
	omit    []int // part numbers left out of the NZB
}

// postID is the message ID of part n (1-based) of a file.
func postID(name string, n int) string { return fmt.Sprintf("%s.p%d@ether.test", sanitizeID(name), n) }

func sanitizeID(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '<' || r == '>' || r == '"' {
			return '_'
		}
		return r
	}, s)
}

// post stores the files' articles in f and returns their NZB.
func post(t testing.TB, f *fakeFetcher, partSize int, files ...postFile) []byte {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	sb.WriteString(`<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"><head><meta type="title">test</meta></head>` + "\n")
	for fi, pf := range files {
		sizes := pf.parts
		if sizes == nil {
			for off := 0; off < len(pf.data); off += partSize {
				sizes = append(sizes, min(partSize, len(pf.data)-off))
			}
			if len(pf.data) == 0 {
				sizes = []int{0}
			}
		}
		yname := pf.yName
		if yname == "" {
			yname = pf.name
		}
		subject := pf.subject
		if subject == "" {
			subject = fmt.Sprintf(`[%d/%d] - "%s" yEnc (1/%d)`, fi+1, len(files), pf.name, len(sizes))
		}
		fmt.Fprintf(&sb, `<file poster="t@test" date="1700000000" subject="%s"><groups><group>alt.binaries.test</group></groups><segments>`, html.EscapeString(subject))
		off := 0
		omit := map[int]bool{}
		for _, n := range pf.omit {
			omit[n] = true
		}
		for i, sz := range sizes {
			n := i + 1
			id := postID(pf.name, n)
			f.mu.Lock()
			f.articles[id] = fakeArticle{
				data: pf.data[off : off+sz],
				part: core.ArticlePart{Name: yname, FileSize: int64(len(pf.data)), Part: n, Total: len(sizes), Begin: int64(off) + 1, End: int64(off + sz)},
			}
			f.mu.Unlock()
			if !omit[n] {
				fmt.Fprintf(&sb, `<segment bytes="%d" number="%d">%s</segment>`, sz+sz/50+400, n, html.EscapeString(id))
			}
			off += sz
		}
		if off != len(pf.data) {
			t.Fatalf("part sizes of %s sum to %d, want %d", pf.name, off, len(pf.data))
		}
		sb.WriteString("</segments></file>\n")
	}
	sb.WriteString("</nzb>\n")
	return []byte(sb.String())
}

func randBytes(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	b := make([]byte, n)
	for i := 0; i < n; i += 8 {
		v := r.Uint64()
		for j := 0; j < 8 && i+j < n; j++ {
			b[i+j] = byte(v >> (8 * j))
		}
	}
	return b
}

// mkvBytes is random data with a Matroska signature.
func mkvBytes(seed uint64, n int) []byte {
	b := randBytes(seed, n)
	copy(b, []byte{0x1a, 0x45, 0xdf, 0xa3})
	return b
}

func newTestEngine(t testing.TB, f core.ArticleFetcher, cache core.SegmentCache, opts Options) *Engine {
	t.Helper()
	e := New(f, cache, opts, nil)
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func openSource(t testing.TB, e *Engine, id string, doc []byte) core.MediaSource {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	src, err := e.Open(ctx, id, doc)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return src
}

// checkRandomReads compares random ReadAt calls against want.
func checkRandomReads(t *testing.T, src io.ReaderAt, want []byte, seed uint64, reads int) {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, 7))
	size := int64(len(want))
	for k := 0; k < reads; k++ {
		off := r.Int64N(size)
		n := 1 + r.IntN(300_000)
		buf := make([]byte, n)
		got, err := src.ReadAt(buf, off)
		end := min(off+int64(n), size)
		if int64(got) != end-off {
			t.Fatalf("ReadAt(%d, %d) = %d bytes, want %d (err %v)", n, off, got, end-off, err)
		}
		if end-off < int64(n) {
			if err != io.EOF {
				t.Fatalf("ReadAt(%d, %d) short read without io.EOF: %v", n, off, err)
			}
		} else if err != nil {
			t.Fatalf("ReadAt(%d, %d): %v", n, off, err)
		}
		if !bytes.Equal(buf[:got], want[off:end]) {
			t.Fatalf("ReadAt(%d, %d) returned wrong bytes", n, off)
		}
	}
}

// readAll reads the source sequentially with a small buffer like an HTTP
// handler does.
func readAll(t testing.TB, src core.MediaSource, chunk int) []byte {
	t.Helper()
	out := make([]byte, 0, src.Size())
	buf := make([]byte, chunk)
	var off int64
	for {
		n, err := src.ReadAt(buf, off)
		out = append(out, buf[:n]...)
		off += int64(n)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("ReadAt at %d: %v", off, err)
		}
	}
}
