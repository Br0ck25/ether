package transcode

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
)

// A live (unknown-duration) EVENT playlist lists the contiguous completed
// prefix. Pruning old segments to bound disk use must not shrink it: an
// EVENT playlist may only grow, and an empty one makes the live relay
// re-request segment 0 and restart the encoder.
func TestLivePlaylistSurvivesPruning(t *testing.T) {
	fr := newFakeRunner()
	m := newTestManager(t, fr, config.Transcode{}, WithBuffering(24*time.Second, 0, 8*time.Second))
	s := startSession(t, m, remuxRequest(t, "live-prune", 0))
	for n := 0; n <= 6; n++ {
		if rec := get(t, s, n); rec.Code != http.StatusOK {
			t.Fatalf("segment %d: %d %s", n, rec.Code, rec.Body.String())
		}
	}
	s.mu.Lock()
	pruned := !s.done[0][0]
	s.mu.Unlock()
	if !pruned {
		t.Fatal("segment 0 was not pruned; the test does not exercise pruning")
	}
	rec := httptest.NewRecorder()
	s.ServePlaylist(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	b := rec.Body.String()
	if got := strings.Count(b, "#EXTINF"); got < 7 {
		t.Fatalf("live playlist shrank to %d segments after pruning:\n%s", got, b)
	}
	if !strings.Contains(b, "00006.ts") {
		t.Fatalf("live playlist lost the requested segment:\n%s", b)
	}
	if n := fr.startCount(); n != 1 {
		t.Fatalf("encoder restarted %d times", n-1)
	}
}
