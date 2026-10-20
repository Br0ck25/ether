package livetv

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestMetadataAndPlaylistPollingCannotPreserveUnusedTune(t *testing.T) {
	var clock atomic.Int64
	clock.Store(7)
	pb, tc := &fakePlayback{}, &fakeTranscoder{}
	s := testService(t, Options{Playback: pb, Transcoder: tc, Now: func() time.Time { return time.Unix(clock.Load(), 0) }})
	c, err := s.SaveChannel(context.Background(), testChannel())
	if err != nil {
		t.Fatal(err)
	}
	u := &core.User{ID: 1, IsOwner: true}
	info, err := s.Tune(context.Background(), u, c.ID, core.ClientCaps{ClientID: "tv"})
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []int64{17, 27, 38} {
		clock.Store(at)
		if _, err := s.Session(context.Background(), info.ID, u); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		s.ServePlaylist(rec, httptest.NewRequest("GET", "/", nil), info.ID, u)
		if rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	s.expireSessions()
	if len(s.ViewingSessions()) != 0 || len(pb.stopped) == 0 {
		t.Fatal("polling preserved an unused viewing reservation/source")
	}
	for _, hls := range tc.hls {
		if !hls.closed {
			t.Fatal("unused ffmpeg session survived")
		}
	}
}

func TestMediaConsumptionKeepsLiveTuneUntilIdle(t *testing.T) {
	var clock atomic.Int64
	clock.Store(7)
	pb, tc := &fakePlayback{}, &fakeTranscoder{}
	s := testService(t, Options{Playback: pb, Transcoder: tc, Now: func() time.Time { return time.Unix(clock.Load(), 0) }})
	c, err := s.SaveChannel(context.Background(), testChannel())
	if err != nil {
		t.Fatal(err)
	}
	u := &core.User{ID: 1, IsOwner: true}
	info, err := s.Tune(context.Background(), u, c.ID, core.ClientCaps{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.ServeSegment(rec, httptest.NewRequest("GET", "/", nil), info.ID, info.Programme.Start.UnixMilli(), 0, u)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	clock.Store(100)
	s.expireSessions()
	if len(s.ViewingSessions()) != 1 {
		t.Fatal("active tune used startup expiry")
	}
	clock.Store(128)
	s.expireSessions()
	if len(s.ViewingSessions()) != 0 || len(pb.stopped) != 1 || !tc.hls[0].closed {
		t.Fatal("idle tune leaked resources")
	}
}

type lifecycleTransport func(*http.Request) (*http.Response, error)

func (f lifecycleTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIPTVStartupHasSharedDeadline(t *testing.T) {
	client := &http.Client{Transport: lifecycleTransport(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 10*time.Second {
			t.Error("IPTV startup has no shared 10-second deadline")
		}
		return nil, context.DeadlineExceeded
	})}
	s := testService(t, Options{HTTPClient: client})
	c, err := s.SaveChannel(context.Background(), Channel{Name: "Stalled", Number: "1", Kind: "iptv", Enabled: true, StreamURL: "http://upstream.invalid/live"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Tune(context.Background(), &core.User{ID: 1}, c.ID, core.ClientCaps{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if len(s.ViewingSessions()) != 0 {
		t.Fatal("failed tune retained its reservation")
	}
}

func TestPendingTuneCanBeInspectedAndStoppedWithoutWaitingForUpstream(t *testing.T) {
	entered := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer up.Close()
	s := testService(t, Options{HTTPClient: up.Client()})
	c, err := s.SaveChannel(context.Background(), Channel{Name: "Stalled", Number: "1", Kind: "iptv", Enabled: true, StreamURL: up.URL})
	if err != nil {
		t.Fatal(err)
	}
	u := &core.User{ID: 1}
	done := make(chan error, 1)
	go func() { _, err := s.Tune(context.Background(), u, c.ID, core.ClientCaps{}); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("tune did not reach upstream")
	}
	stopped := make(chan error, 1)
	go func() {
		views := s.ViewingSessions()
		if len(views) != 1 {
			stopped <- fmt.Errorf("pending reservations = %d", len(views))
			return
		}
		stopped <- s.Stop(context.Background(), views[0].ID, u)
	}()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("monitor/stop blocked behind upstream I/O")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stopped tune kept running")
	}
}
