package playback

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type retryDirectSource struct{ core.MediaSource }

func (retryDirectSource) RetryDirectPlay() bool { return true }

func TestTemporaryDirectPlayFailureKeepsFileForNativeRangeRetry(t *testing.T) {
	a := mkv("A", 200000, 100*time.Second)
	h := newHarness(t, []*fakeRelease{a, mkv("B", 400000, 100*time.Second)}, []string{"A", "B"})
	ps := h.prepare(owner, movieKey, "c1")
	g := h.m.lookup(ps.ID).current()
	// Wait for background index access before replacing the test source.
	<-g.indexReady
	g.src = retryDirectSource{g.src}
	a.setFail(120000, 130000, core.ErrSourceUnavailable)
	srv := directPlayServer(t, h.m, ps.ID)
	_, body, err := get(t, srv.URL, nil)
	if err == nil || len(body) != 120000 {
		t.Fatalf("truncated response n=%d err=%v", len(body), err)
	}
	st, _ := h.m.Session(ps.ID)
	if st.Resolution.Release.ID != "A" || st.Failovers != 0 || st.RequiresRemux {
		t.Fatalf("temporary failure replaced original representation: %+v", st)
	}
	a.setFail(0, 0, nil)
	resp, body, err := get(t, srv.URL, map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", len(body), len(body)+999)})
	if err != nil || resp.StatusCode != 206 || !bytes.Equal(body, expected("A", 120000, 1000)) {
		t.Fatalf("native range retry status=%d n=%d err=%v", resp.StatusCode, len(body), err)
	}
}
