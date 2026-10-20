package plex

import (
	"context"
	"testing"
	"time"
)

type recoveringPlayback struct {
	*fakePlayback
	playID, input string
	at            time.Duration
}

func (p *recoveringPlayback) FailoverInput(_ context.Context, id, input string, at time.Duration) error {
	p.playID, p.input, p.at = id, input, at
	return nil
}

func TestEncoderRecoveryMapsClientSessionToPlayback(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	p := &recoveringPlayback{fakePlayback: f.srv.d.Playback.(*fakePlayback)}
	f.srv.d.Playback = p
	f.srv.transcodes.put(&transcodeEntry{id: "client-encoder", playID: "library-playback"})
	if err := f.srv.RecoverTranscode(t.Context(), "client-encoder", "http://loopback/source/generation", 12*time.Second); err != nil {
		t.Fatal(err)
	}
	if p.playID != "library-playback" || p.input != "http://loopback/source/generation" || p.at != 12*time.Second {
		t.Fatalf("wrong recovery mapping: %+v", p)
	}
	p.playID = ""
	if err := f.srv.RecoverTranscode(t.Context(), "stopped-client", "ignored", 0); err != nil || p.playID != "" {
		t.Fatal("stopped encoder triggered recovery")
	}
}
