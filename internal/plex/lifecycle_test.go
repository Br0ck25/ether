package plex

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestTranscodeStopReleasesSourceUnlessReplacementIsActive(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "last-consumer", true: "replacement"}[replacement], func(t *testing.T) {
			f := newFixture()
			defer f.srv.Close()
			f.trans.decision = core.Decision{Kind: core.DecisionRemux, VideoCopy: true, AudioCopy: true, Container: "mpegts"}
			start := f.get(t, "/video/:/transcode/universal/start.m3u8?path=%2Flibrary%2Fmetadata%2F1000000000000550&session=old")
			if start.Code != 200 {
				t.Fatal(start.Code, start.Body.String())
			}
			e, ok := f.srv.transcodes.get("old")
			if !ok || e.playID == "" {
				t.Fatal("missing source binding")
			}
			if replacement {
				f.srv.transcodes.put(&transcodeEntry{id: "replacement", playID: e.playID, userID: e.userID, clientID: e.clientID, started: true})
				f.trans.sessions["replacement"] = &fakeHLS{id: "replacement"}
			}
			stop := f.get(t, "/video/:/transcode/universal/stop?session=old")
			if stop.Code != 200 {
				t.Fatal(stop.Code)
			}
			if _, ok := f.trans.Session("old"); ok {
				t.Fatal("stopped encoder survived")
			}
			_, sourceAlive := f.playback.Session(e.playID)
			if sourceAlive != replacement {
				t.Fatalf("source alive=%v, replacement=%v", sourceAlive, replacement)
			}
			if replacement {
				f.get(t, "/video/:/transcode/universal/stop?session=replacement")
				if _, ok := f.playback.Session(e.playID); ok {
					t.Fatal("last replacement stop retained source")
				}
			}
		})
	}
}
