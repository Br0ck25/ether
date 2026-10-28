package playback

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestInternalProducerReadsAndBufferingDoNotRenewViewerLease(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(map[bool]string{false: "ffmpeg", true: "probe"}[background], func(t *testing.T) {
			h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute)}, []string{"A"})
			ps := h.prepare(owner, movieKey, "c1")
			h.clock.Advance(90 * time.Second)
			url := h.m.InputURL(ps.ID)
			if background {
				url += "?background=1"
			}
			resp, _, err := get(t, url, map[string]string{"Range": "bytes=0-99"})
			if err != nil || resp.StatusCode != 206 {
				t.Fatal(resp, err)
			}
			h.m.Timeline(owner.ID, movieKey, "buffering")
			h.clock.Advance(31 * time.Second)
			h.m.expire()
			if _, ok := h.m.Session(ps.ID); ok {
				t.Fatal("producer/buffering activity preserved abandoned source")
			}
			// The HTTP client can finish reading before the producer's
			// deferred generation release runs on the server goroutine.
			eventually(t, "abandoned source closed", func() bool { return h.engine.sources[0].closed.Load() })
			if h.cache.pinned("A", PinReasonLive) {
				t.Fatal("abandoned source live pin retained")
			}
		})
	}
}

func TestDirectViewerBytesRenewLease(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute)}, []string{"A"})
	ps := h.prepare(owner, movieKey, "c1")
	h.clock.Advance(90 * time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Range", "bytes=0-99")
	h.m.ServeRange(rec, req, ps.ID)
	if rec.Code != 206 {
		t.Fatal(rec.Code)
	}
	h.clock.Advance(90 * time.Second)
	h.m.expire()
	if _, ok := h.m.Session(ps.ID); !ok {
		t.Fatal("viewer bytes did not renew lease")
	}
	h.clock.Advance(31 * time.Second)
	h.m.expire()
	if _, ok := h.m.Session(ps.ID); ok {
		t.Fatal("disconnected direct viewer survived idle lease")
	}
}
