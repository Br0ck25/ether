package stream

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestConcurrentCheckAndOpenOfObfuscatedRelease runs health checks while
// another caller's Open identifies the obfuscated files of the same NZB
// (shared release state). Classifying files while a check reads them must
// not race (go test -race).
func TestConcurrentCheckAndOpenOfObfuscatedRelease(t *testing.T) {
	for round := 0; round < 10; round++ {
		f := newFakeFetcher()
		f.latency = time.Millisecond
		data := mkvBytes(51, 600_000)
		doc := post(t, f, 100_000,
			postFile{name: "e8f7e6d5c4b3a2f1e0d9c8b7a6f5e4d3", data: data},
			postFile{name: "f8f7e6d5c4b3a2f1e0d9c8b7a6f5e4d3", data: randBytes(2, 2000)},
		)
		e := newTestEngine(t, f, nil, Options{})
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, _ = e.CheckRelease(context.Background(), "obf-race", doc)
			}()
			go func() {
				defer wg.Done()
				if src, err := e.Open(context.Background(), "obf-race", doc); err == nil {
					_ = src.Close()
				}
			}()
		}
		wg.Wait()
	}
}
