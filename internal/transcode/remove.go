package transcode

import (
	"errors"
	"os"
	"sync"
	"time"
)

// pendingRemovals deletes files, keeping the ones the OS refused to delete
// for a later retry. On Windows a file can't be removed while any handle is
// open — including a virus scanner's brief look at a file ffmpeg just
// wrote — so a single failed os.Remove would leak transcode temp files.
type pendingRemovals struct {
	mu    sync.Mutex
	paths []string
}

func (p *pendingRemovals) remove(path string) {
	if err := os.Remove(path); err == nil || errors.Is(err, os.ErrNotExist) {
		return
	}
	p.mu.Lock()
	p.paths = append(p.paths, path)
	p.mu.Unlock()
}

// retry attempts the deferred deletions once.
func (p *pendingRemovals) retry() {
	p.mu.Lock()
	paths := p.paths
	p.paths = nil
	p.mu.Unlock()
	for _, path := range paths {
		p.remove(path)
	}
}

// removeAllRetry is os.RemoveAll that waits out transient sharing
// violations (Windows) for up to about two seconds.
func removeAllRetry(dir string) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		if err = os.RemoveAll(dir); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

// waitTimeout waits for wg, giving up after d.
func waitTimeout(wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}
