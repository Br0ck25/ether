package transcode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Signals that serveInit reached its cancellable wait, without racing a
// sleep against the encoder or relying on a fast filesystem.
type initWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *initWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestInitRangeWaitsForActiveEncoder(t *testing.T) {
	for _, format := range []string{ContainerMatroska, ContainerFMP4, "dash-video", "dash-audio"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(format+"/"+method, func(t *testing.T) {
				s := &Session{m: &Manager{opts: options{segmentTimeout: time.Second}}, dir: t.TempDir(), container: format,
					done: []map[int]bool{{}, {}}, cur: &run{track: 0}, changed: make(chan struct{}), wantTrack: 0}
				track := 0
				if format == "dash-video" || format == "dash-audio" {
					s.proto = core.ProtocolDASH
					s.container = ContainerFMP4
					s.dash = &dashState{inits: make([]string, 2), reps: []dashRep{{Kind: "video"}, {Kind: "audio"}}}
					if format == "dash-audio" {
						track = 1
					}
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				waitCtx := &initWaitContext{Context: ctx, entered: make(chan struct{})}
				req := httptest.NewRequest(method, "/header", nil).WithContext(waitCtx)
				req.Header.Set("Range", "bytes=0-")
				rec := httptest.NewRecorder()
				done := make(chan struct{})
				go func() { defer close(done); s.serveInit(rec, req, track) }()
				defer func() { cancel(); <-done }()
				select {
				case <-done:
					t.Fatalf("cold init returned before publication: HTTP %d %q", rec.Code, rec.Body.String())
				case <-waitCtx.entered:
				case <-time.After(time.Second):
					t.Fatal("init did not wait")
				}
				body := []byte("encoder initialization bytes")
				if err := os.WriteFile(filepath.Join(s.dir, "ready.init"), body, 0o600); err != nil {
					t.Fatal(err)
				}
				s.mu.Lock()
				if s.isDASH() {
					s.dash.inits[track] = "ready.init"
				} else {
					s.initFile = "ready.init"
				}
				s.broadcastLocked()
				s.mu.Unlock()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("init waiter did not complete")
				}
				expected := http.StatusPartialContent
				if rec.Code != expected {
					t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
				}
				if method == http.MethodGet && rec.Body.String() != string(body) {
					t.Fatalf("body=%q", rec.Body.String())
				}
				if method == http.MethodHead && rec.Body.Len() != 0 {
					t.Fatal("HEAD returned a body")
				}
				if s.wantTrack != 0 || s.runSeq != 0 || s.deliveredCount != 0 {
					t.Fatal("init probe changed encoder selection or delivery evidence")
				}
			})
		}
	}
}

func TestInitRangeDoesNotStartInactiveRendition(t *testing.T) {
	s := &Session{m: &Manager{opts: options{segmentTimeout: time.Second}}, container: ContainerFMP4,
		done: []map[int]bool{{}, {}}, cur: &run{track: 0}, changed: make(chan struct{})}
	req := httptest.NewRequest(http.MethodGet, "/header", nil)
	req.Header.Set("Range", "bytes=0-")
	rec := httptest.NewRecorder()
	s.serveInit(rec, req, 1)
	if rec.Code != http.StatusServiceUnavailable || s.wantTrack != 0 {
		t.Fatalf("status=%d selected=%d", rec.Code, s.wantTrack)
	}
}
