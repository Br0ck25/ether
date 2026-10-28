package transcode

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
)

func TestFailedInputCannotRestartAcrossClientRetries(t *testing.T) {
	fr := newFakeRunner()
	fr.failStart = func(args []string) (string, bool) {
		return "input unavailable", argValue(args, "-i") == "http://127.0.0.1:1/source/a"
	}
	failed := make(chan string, 8)
	m := newTestManager(t, fr, config.Transcode{}, WithInputFailureHandler(func(id, input string, at time.Duration, err error) {
		failed <- input
	}))
	req := remuxRequest(t, "bounded-input", time.Hour)
	req.Decision.Container = ContainerFMP4
	s := startSession(t, m, req)
	if r := get(t, s, 0); r.Code != http.StatusBadGateway {
		t.Fatalf("failed media: %d", r.Code)
	}
	select {
	case input := <-failed:
		if input != req.InputURL {
			t.Fatalf("wrong failed input: %q", input)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("input failure not reported")
	}
	starts := fr.startCount()
	if starts != 2 {
		t.Fatalf("starts=%d, want initial + one probe retry", starts)
	}
	for range 5 {
		for _, segment := range []int{0, InitSegment} {
			if r := get(t, s, segment); r.Code != http.StatusBadGateway {
				t.Fatalf("retry status=%d", r.Code)
			}
		}
	}
	if fr.startCount() != starts {
		t.Fatal("client retries restarted exhausted input")
	}
	select {
	case <-failed:
		t.Fatal("duplicate failure callback")
	default:
	}
	if err := s.SwapInput(context.Background(), "http://127.0.0.1:1/source/b", req.Info, 0); err != nil {
		t.Fatal(err)
	}
	if r := get(t, s, 0); r.Code != http.StatusOK {
		t.Fatalf("replacement cannot recover: %d", r.Code)
	}
}
