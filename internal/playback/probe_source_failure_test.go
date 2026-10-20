package playback

import (
	"errors"
	"testing"
	"time"
)

func TestProbeReadFailureTriesAnotherSourceInsteadOfEstimatedPlayback(t *testing.T) {
	bad := mkv("unreadable", 10000, time.Minute)
	bad.setFail(0, bad.Size, errors.New("source reader has closed"))
	h := newHarness(t, []*fakeRelease{bad, mkv("usable", 10000, time.Minute)}, []string{"unreadable", "usable"})
	ps := h.prepare(owner, movieKey, "probe-read-failure")
	if ps.Resolution.Release.ID != "usable" || ps.Info.Estimated {
		t.Fatalf("unreadable source became estimated playback: %+v", ps.Resolution.Release)
	}
	if h.resolver.bad["unreadable"] != "" {
		t.Fatal("unclassified read error permanently blacklisted the release")
	}
}
