package playback

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSessionSnapshotReportsClientPlayheadNotSourceBytes(t *testing.T) {
	h := newHarness(t, []*fakeRelease{mkv("A", 100_000, time.Minute)}, []string{"A"})
	ps := h.prepare(owner, movieKey, "viewer")
	h.m.lookup(ps.ID).bytes.Store(33_000)
	unknown, _ := h.m.Session(ps.ID)
	data, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if unknown.ViewOffset != nil || strings.Contains(string(data), "viewOffset") {
		t.Fatal("unreported position must be absent")
	}
	// Another device and another viewer must not affect this session.
	h.m.PlaybackProgress(owner.ID, "other", movieKey, 55*time.Second, "playing")
	h.m.PlaybackProgress(owner.ID+1, "viewer", movieKey, 55*time.Second, "playing")
	untouched, _ := h.m.Session(ps.ID)
	if untouched.ViewOffset != nil {
		t.Fatal("foreign timeline moved the playhead")
	}
	for _, position := range []time.Duration{55 * time.Second, 10 * time.Second, 0} {
		h.m.PlaybackProgress(owner.ID, "viewer", movieKey, position, "paused")
		snap, _ := h.m.Session(ps.ID)
		if snap.ViewOffset == nil || *snap.ViewOffset != position.Milliseconds() || snap.BytesServed != 33_000 {
			t.Fatalf("position %v snapshot %+v", position, snap)
		}
		data, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"viewOffset":`) {
			t.Fatal("reported zero must survive JSON")
		}
	}
}
