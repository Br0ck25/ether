package usenet

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/usenet/nntptest"
)

func TestBreakerAuthBackoffDoublesToCap(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	b := newBreaker(time.Second, time.Minute, 1, func() time.Time { return now })
	errAuth := errors.New("481")
	want := []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 6 * time.Hour, 6 * time.Hour}
	for i, w := range want {
		if i == 0 {
			if d := b.authFailure(errAuth); d != w {
				t.Fatalf("first failure backoff %v, want %v", d, w)
			}
			// A concurrent dial failing too must not grow the backoff.
			if d := b.authFailure(errAuth); d != w {
				t.Fatalf("concurrent failure backoff %v, want %v", d, w)
			}
			continue
		}
		if b.authProbeDue() {
			t.Fatalf("probe %d due before its backoff elapsed", i)
		}
		now = now.Add(want[i-1])
		if !b.authProbeDue() {
			t.Fatalf("probe %d not due after %v", i, want[i-1])
		}
		if b.authProbeDue() {
			t.Fatal("second concurrent probe claimed")
		}
		if err := b.allow(); !errors.Is(err, ErrProviderDisabled) {
			t.Fatalf("requests allowed while probing: %v", err)
		}
		if d := b.authFailure(errAuth); d != w {
			t.Fatalf("probe %d failure backoff %v, want %v", i, d, w)
		}
	}
	if st, until, _, _ := b.state(); st != StateAuthFail || !until.Equal(now.Add(6*time.Hour)) {
		t.Fatalf("state %q until %v", st, until)
	}

	// An inconclusive probe retries after the minimum without growing.
	now = now.Add(6 * time.Hour)
	b.authProbeDue()
	b.authProbeInconclusive(errors.New("dial: connection refused"))
	if _, until, _, _ := b.state(); !until.Equal(now.Add(15 * time.Minute)) {
		t.Fatalf("inconclusive retry at %v", until)
	}

	now = now.Add(15 * time.Minute)
	if !b.authProbeDue() {
		t.Fatal("probe not due after inconclusive retry delay")
	}
	b.authRecovered()
	if err := b.allow(); err != nil {
		t.Fatalf("not re-enabled: %v", err)
	}
	// The next failure starts the ladder again.
	if d := b.authFailure(errAuth); d != 15*time.Minute {
		t.Fatalf("backoff after recovery %v", d)
	}
}

func TestTransientAuthOutageSelfHeals(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{Username: "u", Password: "right"})
	addSeg(t, s, "a@x", []byte("data"))
	p := s.Provider("p", 2)
	f := newFetcher(t, []config.UsenetProvider{p}, Options{
		AuthRetryMin: 100 * time.Millisecond, AuthRetryMax: time.Second,
		KeepAlive: 100 * time.Millisecond, MinIdle: -1,
	})
	// The provider's auth backend rejects valid credentials for a while.
	s.SetCredentials("u", "rotating")
	if _, err := f.Body(context.Background(), "a@x", nil, core.PriorityLive); err == nil {
		t.Fatal("body served during auth outage")
	}
	if st := statsFor(f, "p"); st.State != StateAuthFail {
		t.Fatalf("state %q, want %q", st.State, StateAuthFail)
	}
	// Still failing: probes keep it disabled.
	time.Sleep(300 * time.Millisecond)
	if st := statsFor(f, "p"); st.State != StateAuthFail {
		t.Fatalf("state %q during outage", st.State)
	}
	if s.Stats().AuthFailures < 2 {
		t.Fatalf("auth failures %d: provider was not re-probed", s.Stats().AuthFailures)
	}
	s.SetCredentials("u", "right")
	deadline := time.Now().Add(10 * time.Second)
	for statsFor(f, "p").State != StateOK {
		if time.Now().After(deadline) {
			t.Fatalf("provider never re-enabled: %+v", statsFor(f, "p"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got, err := f.Body(context.Background(), "a@x", nil, core.PriorityLive); err != nil || string(got) != "data" {
		t.Fatalf("after recovery: %q %v", got, err)
	}
}
