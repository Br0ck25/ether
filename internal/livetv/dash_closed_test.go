package livetv

import (
	"context"
	"errors"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

// A DASH request racing a session close (expiry, stop, channel edit) must
// not start a transcoder on the closed session: close has already run and
// nothing would ever stop it.
func TestDASHNotStartedOnClosedSession(t *testing.T) {
	tc := &fakeTranscoder{}
	s := testService(t, Options{Transcoder: tc})
	info := &core.MediaInfo{Container: "mpegts"}
	ss := &session{info: SessionInfo{ID: "s1", Channel: Channel{ID: "c1", Kind: "iptv"}, MediaInfo: info}, inputURL: "http://127.0.0.1/live", resources: map[string]string{}}
	ss.close(s)
	if _, err := s.ensureDASH(context.Background(), ss, "s1"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("ensureDASH on a closed session: %v", err)
	}
	if len(tc.requests) != 0 || ss.dash != nil {
		t.Fatalf("transcoder started for a closed session: %+v", tc.requests)
	}
}
