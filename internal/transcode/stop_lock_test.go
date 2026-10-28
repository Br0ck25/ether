package transcode

import (
	"testing"
	"time"
)

type slowStopProcess struct {
	Process
	entered, release chan struct{}
}

func (p *slowStopProcess) Stop() { close(p.entered); <-p.release }

func TestStopRunDoesNotHoldSessionLockWhileReaping(t *testing.T) {
	p := &slowStopProcess{entered: make(chan struct{}), release: make(chan struct{})}
	s := &Session{
		sessionEncoding: sessionEncoding{
			cur: &run{proc: p},
		},
	}
	stopped := make(chan struct{})
	go func() { s.mu.Lock(); s.stopRunLocked(); s.mu.Unlock(); close(stopped) }()
	<-p.entered
	defer func() { close(p.release); s.watchers.Wait() }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop held session lock while waiting for encoder")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		t.Fatal("stopped run still attached")
	}
}
