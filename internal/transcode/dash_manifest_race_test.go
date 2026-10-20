package transcode

import (
	"context"
	"net/http"
	"net/http/httptest"
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
