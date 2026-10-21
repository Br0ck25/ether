package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Monitor frames travel only over an anonymous pipe, never argv, a file, or logs.
// EOF without a clean frame means the parent terminated unexpectedly. This works
// for fatal runtime crashes too: it does not try to recover in a broken process.
type monitorFrame struct {
	Channels []Channel `json:"channels,omitempty"`
	Private  string    `json:"private,omitempty"`
	Public   string    `json:"public,omitempty"`
	Clean    bool      `json:"clean,omitempty"`
}

// WatchProcess starts a small independent delivery process. The returned function
// must be called on normal exit, with clean=false for a fatal run error. A panic
// must be allowed to terminate the parent so that EOF reaches the watcher.
func (s *Service) WatchProcess(executable string) (func(bool) error, error) {
	cmd := exec.Command(executable, "notification-monitor")
	pipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		pipe.Close()
		return nil, err
	}
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- cmd.Wait(); close(done) }()
	var mu sync.Mutex
	closed := false
	update := func() error {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cs, err := s.channels(ctx)
		if err != nil {
			return err
		}
		selected := make([]Channel, 0, len(cs))
		for _, c := range cs {
			if c.Enabled && matches(c.Events, "server.crash") {
				selected = append(selected, c)
			}
		}
		private, public, err := s.pushKeys(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(pipe).Encode(monitorFrame{Channels: selected, Private: private, Public: public})
	}
	s.watchMu.Lock()
	s.watchUpdate = update
	s.monitorDone = done
	s.watchMu.Unlock()
	finish := func(clean bool) error {
		s.watchMu.Lock()
		s.watchUpdate = nil
		s.watchMu.Unlock()
		mu.Lock()
		if closed {
			mu.Unlock()
			return nil
		}
		closed = true
		if clean {
			_ = json.NewEncoder(pipe).Encode(monitorFrame{Clean: true})
		}
		_ = pipe.Close()
		mu.Unlock()
		return <-result
	}
	if err := update(); err != nil {
		_ = finish(true)
		return nil, err
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := update(); err != nil {
					s.opts.Logger.Warn("crash notification watcher could not refresh its channels")
				}
			}
		}
	}()
	return finish, nil
}
func (s *Service) refreshMonitor() {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if s.watchUpdate != nil {
		if err := s.watchUpdate(); err != nil {
			s.opts.Logger.Warn("crash notification watcher could not refresh its channels")
		}
	}
}

// RunMonitor is used only by the executable's notification-monitor entry point.
// A killed host, network outage, or supervisor killing the whole process group
// also stops this watcher. An external uptime monitor is required for those.
func RunMonitor(input io.Reader) error {
	decoder := json.NewDecoder(input)
	var last monitorFrame
	for {
		var frame monitorFrame
		err := decoder.Decode(&frame)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				return errors.New("invalid monitor stream")
			}
			break
		}
		if frame.Clean {
			return nil
		}
		last = frame
	}
	s := New(nil, Options{})
	s.pushPrivate, s.pushPublic = last.Private, last.Public
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	// A small cap avoids multiplying transport buffers during an already bad exit.
	slots := make(chan struct{}, 4)
	for _, c := range last.Channels {
		if !c.Enabled || !matches(c.Events, "server.crash") {
			continue
		}
		wg.Add(1)
		go func(c Channel) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				return
			}
			_ = s.deliver(ctx, c, Event{Kind: "server.crash", Title: "Ether stopped unexpectedly", Message: "The server process exited without completing a clean shutdown. Check Ether's crash log and service status. This alert was sent by its independent crash watcher.", At: time.Now()})
		}(c)
	}
	wg.Wait()
	return nil
}
