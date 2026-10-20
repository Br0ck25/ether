package livetv

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type fixtureLiveProber struct {
	core.Transcoder
	calls int
	err   error
}

func (p *fixtureLiveProber) ProbeLive(context.Context, string) (*core.MediaInfo, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	return &core.MediaInfo{Streams: []core.StreamInfo{{Type: "video", Codec: "h264", Width: 1280, Height: 720}}}, nil
}

func TestLiveCodecCacheExpiresAndDoesNotCacheFailures(t *testing.T) {
	now := time.Unix(100, 0)
	p := &fixtureLiveProber{}
	s := testService(t, Options{Transcoder: p, Now: func() time.Time { return now }})
	for i := 0; i < 2; i++ {
		info, err := s.probeLiveInput(context.Background(), "https://fixture.invalid/channel")
		if err != nil || info.Video() == nil {
			t.Fatal(err)
		}
		info.Duration = time.Hour // caller changes must not alter the cache
	}
	if p.calls != 1 {
		t.Fatal("retune repeated the expensive live probe")
	}
	now = now.Add(2*time.Minute + time.Second)
	s.probeLiveInput(context.Background(), "https://fixture.invalid/channel")
	if p.calls != 2 {
		t.Fatal("expired live metadata was reused")
	}
	p.err = errors.New("temporary upstream failure")
	for i := 0; i < 2; i++ {
		if _, err := s.probeLiveInput(context.Background(), "https://fixture.invalid/other"); err == nil {
			t.Fatal("failed probe succeeded")
		}
	}
	if p.calls != 4 {
		t.Fatal("failed analysis was cached")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.probeLiveInput(ctx, "https://fixture.invalid/channel"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled tune ignored cancellation")
	}
}
