package transcode

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
)

// ServeManifest must read the media info under the session lock: a
// concurrent failover (SwapInput) replaces it. Run with -race.
func TestDASHManifestDuringSwapInputIsRaceFree(t *testing.T) {
	fr := newFakeRunner()
	m := newTestManager(t, fr, config.Transcode{})
	req := dashRequest(t, "manifest-swap", time.Hour)
	s := startDASHSession(t, m, req)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			s.ServeManifest(rec, httptest.NewRequest(http.MethodGet, "/manifest", nil))
		}()
		go func() {
			defer wg.Done()
			if err := s.SwapInput(context.Background(), "http://127.0.0.1:1/source/b", req.Info, 12*time.Second); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

// A newer run replaces (and deletes) a representation's init segment while
// requests for it are in flight: each request must serve the init it
// looked up or the new one, never a 500 for a file deleted in between.
func TestDASHInitServedWhileReplaced(t *testing.T) {
	fr := newFakeRunner()
	m := newTestManager(t, fr, config.Transcode{})
	s := startDASHSession(t, m, dashRequest(t, "init-replace", time.Hour))
	r0 := httptest.NewRecorder()
	s.ServeInit(r0, httptest.NewRequest(http.MethodGet, "/init", nil), 0)
	if r0.Code != 200 {
		t.Fatalf("init: %d", r0.Code)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for k := 0; ; k++ {
			select {
			case <-stop:
				return
			default:
			}
			name := fmt.Sprintf("init-0-replace-%d.mp4", k)
			s.mu.Lock()
			if err := os.WriteFile(filepath.Join(s.dir, name), testVideoInit(1280, 720), 0o644); err != nil {
				s.mu.Unlock()
				t.Error(err)
				return
			}
			old := s.dash.inits[0]
			s.dash.inits[0] = name
			_ = os.Remove(filepath.Join(s.dir, old))
			s.mu.Unlock()
		}
	}()
	for i := 0; i < 2000; i++ {
		r := httptest.NewRecorder()
		s.ServeInit(r, httptest.NewRequest(http.MethodGet, "/init", nil), 0)
		if r.Code != 200 {
			t.Errorf("request %d: %d %s", i, r.Code, r.Body.String())
			break
		}
	}
	close(stop)
	wg.Wait()
}

// A run re-producing segments an earlier run already published (a seek
// back over cached media) must not keep its assembled copies on disk
// until it exits.
func TestDASHRedundantSegmentsAreRemoved(t *testing.T) {
	fr := newFakeRunner()
	m := newTestManager(t, fr, config.Transcode{}, WithBuffering(time.Hour, 0, 0))
	s := startDASHSession(t, m, dashRequest(t, "redundant", time.Hour))
	for n := 0; n < 4; n++ {
		if r := media(t, s, 0, n); r.Code != 200 {
			t.Fatalf("segment %d: %d", n, r.Code)
		}
	}
	s.mu.Lock()
	err := s.startRunLocked(0, false) // the new run makes 0..3 again
	r := s.cur
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "re-production", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return r.produced >= 4
	})
	left, _ := filepath.Glob(filepath.Join(r.dash.dir, "seg-*"))
	if len(left) != 0 {
		t.Errorf("redundant assembled segments left in the run directory: %v", left)
	}
}
