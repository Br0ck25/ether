package transcodebench

import (
	"errors"
	"io"
	"testing"
)

func TestParsePlaylistAndDeliveredSeconds(t *testing.T) {
	body := []byte("#EXTM3U\n#EXTINF:4.000,\n00000.ts\n#EXTINF:4.000,\n00001.ts\n#EXTINF:1.250,\n00002.ts\n")
	durs := parsePlaylistDurations(body)
	if len(durs) != 3 || durs[2] != 1.25 {
		t.Fatalf("durs = %v", durs)
	}
	// Shortened final segment counts as 1.25s, not nominal 4s.
	if got := deliveredSeconds(durs, 3, 4, 30); got != 9.25 {
		t.Fatalf("delivered = %v want 9.25", got)
	}
	// Never more than requested.
	if got := deliveredSeconds(durs, 3, 4, 8); got != 8 {
		t.Fatalf("delivered capped = %v want 8", got)
	}
	// Missing playlist entries fall back to nominal, still capped.
	if got := deliveredSeconds(nil, 2, 4, 30); got != 8 {
		t.Fatalf("delivered fallback = %v want 8", got)
	}
	if got := deliveredSeconds(nil, 0, 4, 30); got != 0 {
		t.Fatalf("delivered zero = %v", got)
	}
}

type failWriter struct{}

func (failWriter) Write(p []byte) (int, error) { return 0, errors.New("disk full") }

func TestStreamWriterCountsHashesAndSurfacesSinkErrors(t *testing.T) {
	sw := newStreamWriter(io.Discard)
	if _, err := sw.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if sw.count != 5 || sw.sum() == "" {
		t.Fatalf("count=%d sum=%q", sw.count, sw.sum())
	}
	if sw.status != 200 {
		t.Fatalf("default status = %d", sw.status)
	}
	bad := newStreamWriter(failWriter{})
	if _, err := bad.Write([]byte("x")); err == nil {
		t.Fatal("sink error must surface, not be swallowed")
	}
	if bad.err == nil {
		t.Fatal("sink error must be recorded for the session result")
	}
}
