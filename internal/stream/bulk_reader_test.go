package stream

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestBulkReaderHasIndependentWindowAndReusesHeader(t *testing.T) {
	f := newFakeFetcher()
	want := mkvBytes(99, 2<<20)
	doc := post(t, f, 32<<10, postFile{name: "bulk.mkv", data: want})
	e := newTestEngine(t, f, nil, Options{ReadParallel: 1, HotBufferBytes: 4 << 20})
	info, err := e.PostedFileInfo(context.Background(), "bulk", doc, 0)
	if err != nil || info.FileSize != int64(len(want)) {
		t.Fatal(info, err)
	}
	f.mu.Lock()
	f.latency = 5 * time.Millisecond
	f.mu.Unlock()
	got := make([]byte, len(want))
	n, err := e.ReadPostedRange(context.Background(), "bulk", doc, 0, 0, got)
	if err != nil || n != len(want) || !bytes.Equal(got, want) {
		t.Fatal(n, err)
	}
	if f.maxInflight.Load() <= 1 {
		t.Fatal("bulk reader inherited serial demand-reader limit")
	}
	if f.calls(postID("bulk.mkv", 1)) != 1 {
		t.Fatal("header fetched twice")
	}
	e.Close()
	if _, err := e.ReadPostedRange(context.Background(), "bulk", doc, 0, 0, got); err != ErrClosed {
		t.Fatal(err)
	}
}

func TestBulkBurstHasQuietInterval(t *testing.T) {
	b := bulkReader{}
	if b.options().burstUntil.IsZero() {
		t.Fatal("initial burst absent")
	}
	b.cycle = time.Now().Add(-750 * time.Millisecond)
	if !b.options().burstUntil.IsZero() {
		t.Fatal("burst persisted through quiet interval")
	}
	b.cycle = time.Now().Add(-2 * time.Second)
	if b.options().burstUntil.IsZero() {
		t.Fatal("next bounded burst absent")
	}
}
