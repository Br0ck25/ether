package transcode

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

func TestDASHHandoffProtectsStartupUntilTwoSegmentsAreReady(t *testing.T) {
	s := &Session{proto: core.ProtocolDASH, m: &Manager{opts: options{rungDwell: 8 * time.Second}}, wantTrack: 2,
		cur: &run{track: 0, started: time.Now()}}
	if s.rungDwellLocked() <= 0 {
		t.Fatal("an init probe can interrupt a run before it produces video")
	}
	s.cur.produced = 1
	if s.rungDwellLocked() <= 0 {
		t.Fatal("a single opening segment must retain startup protection")
	}
	s.cur.produced = 2
	if left := s.rungDwellLocked(); left != 0 {
		t.Fatalf("ready startup buffer still held quality change for %v", left)
	}
	s.proto = core.ProtocolHLS
	if s.rungDwellLocked() <= 0 {
		t.Fatal("DASH handoff changed native HLS probe protection")
	}
}

func TestCachedOldDASHRepresentationDoesNotSelectAnEncoder(t *testing.T) {
	fr := newFakeRunner()
	m := newTestManager(t, fr, config.Transcode{ABRLadder: true})
	s := startDASHSession(t, m, dashRequest(t, "cached-rung", 10*time.Minute))
	old := httptest.NewRecorder()
	s.ServeMedia(old, httptest.NewRequest(http.MethodGet, "/0/0.m4s", nil), 0, 0)
	if old.Code != http.StatusOK {
		t.Fatalf("base: %d", old.Code)
	}
	next := httptest.NewRecorder()
	s.ServeMedia(next, httptest.NewRequest(http.MethodGet, "/2/0.m4s", nil), 2, 0)
	if next.Code != http.StatusOK {
		t.Fatalf("new rung: %d", next.Code)
	}
	count := fr.startCount()
	delayed := httptest.NewRecorder()
	s.ServeMedia(delayed, httptest.NewRequest(http.MethodGet, "/0/0.m4s", nil), 0, 0)
	if delayed.Code != http.StatusOK {
		t.Fatalf("cached base: %d", delayed.Code)
	}
	s.mu.Lock()
	selected := s.wantTrack
	s.mu.Unlock()
	if selected != 2 || fr.startCount() != count {
		t.Fatalf("cached request changed video demand to track %d (starts=%d)", selected, fr.startCount())
	}
	// A subsequent uncached audio request uses the active video run.
	audio := httptest.NewRecorder()
	s.ServeMedia(audio, httptest.NewRequest(http.MethodGet, "/1/1.m4s", nil), 1, 1)
	if audio.Code != http.StatusOK || fr.startCount() != count {
		t.Fatalf("audio restarted stale video track: HTTP %d starts=%d", audio.Code, fr.startCount())
	}
}
