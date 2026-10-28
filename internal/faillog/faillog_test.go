package faillog

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestTrackerSummarisesAndRecovers(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	tr := New(log, "cluster: heartbeat")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr.Now = func() time.Time { return now }
	errDown := errors.New("connection refused")

	tr.OK() // healthy: nothing logged
	if buf.Len() != 0 {
		t.Fatalf("logged while healthy: %s", buf.String())
	}
	for i := 0; i < 12; i++ { // a minute of 5s failures
		tr.Fail(errDown)
		now = now.Add(5 * time.Second)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "level=WARN") || !strings.Contains(lines[0], "cluster: heartbeat failed") {
		t.Fatalf("first minute should log one warning, got:\n%s", buf.String())
	}
	if failing, since := tr.Failing(); !failing || !since.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("Failing() = %v %v", failing, since)
	}
	tr.Fail(errDown) // 60s after the first: summary
	lines = strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "still failing") || !strings.Contains(lines[1], "attempts=12") {
		t.Fatalf("expected a summary line, got:\n%s", buf.String())
	}
	now = now.Add(3 * time.Second)
	tr.OK()
	lines = strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[2], "level=INFO") || !strings.Contains(lines[2], "recovered") ||
		!strings.Contains(lines[2], "after=1m3s") || !strings.Contains(lines[2], "failures=13") {
		t.Fatalf("expected one recovery line, got:\n%s", buf.String())
	}
	tr.OK()
	if n := len(strings.Split(strings.TrimSpace(buf.String()), "\n")); n != 3 {
		t.Fatalf("recovery logged twice")
	}
	// A new outage warns again at once.
	tr.Fail(errDown)
	if n := len(strings.Split(strings.TrimSpace(buf.String()), "\n")); n != 4 {
		t.Fatalf("new outage not logged")
	}
	var nilTr *Tracker
	nilTr.Fail(errDown)
	nilTr.OK()
}
