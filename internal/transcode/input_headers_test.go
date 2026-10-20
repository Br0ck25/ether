package transcode

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
)

func TestAuthenticatedLiveProbeAndRemuxUseHeadersWithoutLoggingThem(t *testing.T) {
	headers := map[string]string{"Authorization": "Bearer fixture-secret", "User-Agent": "Provider Player", "Referer": "https://fixture.invalid/watch"}
	fr := newFakeRunner()
	fr.probeJSON = []string{liveProbeJSON}
	m := newTestManager(t, fr, config.Transcode{})
	if _, err := m.ProbeLiveWithHeaders(context.Background(), "https://fixture.invalid/live", headers); err != nil {
		t.Fatal(err)
	}
	probe := fr.probeCalls[0]
	if argValue(probe, "-headers") != "Authorization: Bearer fixture-secret\r\n" || argValue(probe, "-user_agent") != "Provider Player" || argValue(probe, "-referer") != headers["Referer"] {
		t.Fatal("authenticated live probe lost its provider headers")
	}
	req := remuxRequest(t, "authenticated", time.Minute)
	req.InputHeaders = headers
	session := startSession(t, m, req)
	fr.mu.Lock()
	args := append([]string(nil), fr.starts[0]...)
	fr.mu.Unlock()
	if argValue(args, "-headers") != "Authorization: Bearer fixture-secret\r\n" {
		t.Fatal("remux lost its provider credentials")
	}
	logged := strings.Join(redactedInputArgs(args), " ")
	if strings.Contains(logged, "fixture-secret") || strings.Contains(logged, req.InputURL) || strings.Contains(logged, headers["Referer"]) {
		t.Fatal("ffmpeg diagnostic arguments expose credentials")
	}
	// The caller cannot mutate credentials already owned by a running session.
	headers["Authorization"] = "changed"
	if session.inputHeaders["Authorization"] != "Bearer fixture-secret" {
		t.Fatal("session retained the caller's mutable header map")
	}
	if err := session.SwapInput(context.Background(), "https://other.invalid/replacement", req.Info, 0); err != nil {
		t.Fatal(err)
	}
	if len(session.inputHeaders) != 0 {
		t.Fatal("replacement input inherited another source's credentials")
	}
}

func TestMediaHeadersRejectInjectionBeforeStartingProcess(t *testing.T) {
	for _, headers := range []map[string]string{
		{"Authorization": "token\r\nInjected: yes"},
		{"Bad Header": "value"},
		{"Authorization": "a", "authorization": "b"},
	} {
		fr := newFakeRunner()
		m := newTestManager(t, fr, config.Transcode{})
		req := remuxRequest(t, "unsafe", time.Minute)
		req.InputHeaders = headers
		if _, err := m.StartHLS(context.Background(), req); err == nil || len(fr.starts) != 0 {
			t.Fatal("invalid headers reached a media process")
		}
		if _, err := m.ProbeLiveWithHeaders(context.Background(), req.InputURL, headers); err == nil || len(fr.probeCalls) != 0 {
			t.Fatal("invalid headers reached ffprobe")
		}
	}
}
