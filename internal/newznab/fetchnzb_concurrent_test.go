package newznab

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// saveLog captures slog messages so the test can see saveNZB rename
// failures, which saveNZB only logs.
type saveLog struct {
	mu   sync.Mutex
	msgs []string
}

func (h *saveLog) Enabled(context.Context, slog.Level) bool { return true }
func (h *saveLog) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Message)
	return nil
}
func (h *saveLog) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *saveLog) WithGroup(string) slog.Handler      { return h }
func (h *saveLog) failedSaves() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if strings.Contains(m, "save NZB failed") {
			n++
		}
	}
	return n
}

// Concurrent FetchNZB calls for the same release id all pass the disk
// check before any response lands (the handler barrier holds every
// request until all N are in flight), then race to save. Without the
// per-id lock and per-save temp file they share one *.tmp: writes
// interleave/truncate each other and all but one rename fails. The final
// on-disk NZB must be byte-identical to one served body, with no failed
// rename logged and no temp file left behind.
func TestFetchNZBConcurrentSameID(t *testing.T) {
	const n = 8
	dir := t.TempDir()
	served := make([][]byte, n)
	var hits atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(hits.Add(1))
		if i >= n {
			once.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-time.After(15 * time.Second):
		}
		if i < 1 || i > n {
			http.Error(w, "extra hit", http.StatusInternalServerError)
			return
		}
		// Large, mutually distinct bodies so overlapping writes to a
		// shared temp file cannot pass as one valid body.
		body := make([]byte, 0, (4<<20)+64)
		body = append(body, []byte(`<?xml version="1.0"?><nzb>`)...)
		body = append(body, bytes.Repeat([]byte{byte('A' + i - 1)}, 4<<20)...)
		body = append(body, []byte(`</nzb>`)...)
		served[i-1] = body
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	logs := &saveLog{}
	c := New(testConfig(srv.URL), nil, Options{NZBDir: dir, Logger: slog.New(logs)})
	rel := core.Release{ID: "concurrent1", GUID: "g1", Name: "X", NZBURL: srv.URL + "/nzb?id=g1"}

	var wg sync.WaitGroup
	errs := make([]error, n)
	bodies := make([][]byte, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bodies[i], errs[i] = c.FetchNZB(context.Background(), rel)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("FetchNZB %d: %v", i, err)
		}
		if !isOneOf(bodies[i], served) {
			t.Fatalf("FetchNZB %d returned a body that is not one of the served NZBs", i)
		}
	}
	if got := hits.Load(); got != n {
		t.Fatalf("server hits = %d, want %d (barrier must hold all fetches in flight)", got, n)
	}
	if got := logs.failedSaves(); got != 0 {
		t.Fatalf("%d save NZB rename failures logged under concurrency", got)
	}
	disk, err := os.ReadFile(filepath.Join(dir, "concurrent1.nzb"))
	if err != nil {
		t.Fatalf("saved NZB missing: %v", err)
	}
	if !isOneOf(disk, served) {
		t.Fatalf("on-disk NZB (%d bytes) is corrupt/mixed: not byte-identical to any served body", len(disk))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover temp file %q", e.Name())
		}
	}
	// Later fetch is served from disk: no new server hit.
	b, err := c.FetchNZB(context.Background(), rel)
	if err != nil || !bytes.Equal(b, disk) {
		t.Fatalf("disk reuse = %d bytes, %v", len(b), err)
	}
	if got := hits.Load(); got != n {
		t.Fatalf("server hits after disk reuse = %d, want %d", got, n)
	}
}

func isOneOf(b []byte, set [][]byte) bool {
	for _, s := range set {
		if bytes.Equal(b, s) {
			return true
		}
	}
	return false
}
