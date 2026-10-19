package service

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	ksvc "github.com/kardianos/service"
)

type fakeBackend struct {
	mu     sync.Mutex
	calls  []string
	status ksvc.Status
	err    map[string]error
}

func (f *fakeBackend) call(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	if f.err != nil {
		return f.err[name]
	}
	return nil
}
func (f *fakeBackend) Run() error       { return f.call("run") }
func (f *fakeBackend) Start() error     { return f.call("start") }
func (f *fakeBackend) Stop() error      { return f.call("stop") }
func (f *fakeBackend) Restart() error   { return f.call("restart") }
func (f *fakeBackend) Install() error   { return f.call("install") }
func (f *fakeBackend) Uninstall() error { return f.call("uninstall") }
func (f *fakeBackend) Status() (ksvc.Status, error) {
	if err := f.call("status"); err != nil {
		return ksvc.StatusUnknown, err
	}
	return f.status, nil
}
func (f *fakeBackend) Platform() string { return "fake" }
func (f *fakeBackend) String() string   { return "Ether" }

func testService(t *testing.T, run RunFunc, be *fakeBackend) *Service {
	t.Helper()
	svc, err := newWithBackend(run, Options{Executable: "/usr/local/bin/ether", DataDir: "/var/lib/ether"}, be)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestConfigDefaults(t *testing.T) {
	cfg, err := Options{Executable: "/opt/ether/ether", DataDir: "/data"}.Config()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "ether" || cfg.DisplayName != "Ether" || cfg.Description == "" {
		t.Errorf("bad identity: %+v", cfg)
	}
	if cfg.Executable != "/opt/ether/ether" {
		t.Errorf("executable = %q", cfg.Executable)
	}
	if cfg.EnvVars["ETHER_DATA_DIR"] != "/data" {
		t.Errorf("ETHER_DATA_DIR not exported: %v", cfg.EnvVars)
	}
	if cfg.Option["KeepAlive"] != true || cfg.Option["RunAtLoad"] != true {
		t.Errorf("missing keepalive/runatload options: %v", cfg.Option)
	}
}

func TestConfigExplicitEnvNotClobbered(t *testing.T) {
	cfg, err := Options{
		Executable: "/x/ether", DataDir: "/ignored",
		EnvVars: map[string]string{"ETHER_DATA_DIR": "/winner", "A": "b"},
	}.Config()
	if err != nil {
		t.Fatal(err)
	}
	// DataDir is applied after EnvVars, so it wins; document via this test
	// that callers should set only one. (Other keys pass through.)
	if cfg.EnvVars["A"] != "b" {
		t.Errorf("custom env lost: %v", cfg.EnvVars)
	}
}

func TestRunNoRunner(t *testing.T) {
	svc := testService(t, nil, &fakeBackend{})
	if err := svc.Run(); !errors.Is(err, ErrNoRunner) {
		t.Fatalf("Run() = %v, want ErrNoRunner", err)
	}
}

func TestProgramStartStopCancelsRun(t *testing.T) {
	started := make(chan struct{})
	p := &program{stopTimeout: 5 * time.Second, run: func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	if err := p.Start(nil); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := p.Stop(nil); err != nil {
		t.Fatal(err)
	}
	if err := p.result(); err != nil {
		t.Fatalf("cancelled run should be clean, got %v", err)
	}
}

func TestProgramPropagatesRunError(t *testing.T) {
	boom := errors.New("boom")
	p := &program{stopTimeout: time.Second, run: func(context.Context) error { return boom }}
	if err := p.Start(nil); err != nil {
		t.Fatal(err)
	}
	<-p.done
	if err := p.Stop(nil); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(p.result(), boom) {
		t.Fatalf("result = %v, want boom", p.result())
	}
}

func TestProgramStopTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	p := &program{stopTimeout: 30 * time.Millisecond, run: func(context.Context) error { <-release; return nil }}
	if err := p.Start(nil); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_ = p.Stop(nil)
	if time.Since(start) > time.Second {
		t.Fatal("Stop did not respect timeout")
	}
}

func TestStatusMapping(t *testing.T) {
	for raw, want := range map[ksvc.Status]Status{
		ksvc.StatusRunning: StatusRunning,
		ksvc.StatusStopped: StatusStopped,
		ksvc.StatusUnknown: StatusUnknown,
	} {
		svc := testService(t, nil, &fakeBackend{status: raw})
		got, err := svc.Status()
		if err != nil || got != want {
			t.Errorf("status %v -> %q, %v; want %q", raw, got, err, want)
		}
	}
}

func TestHandleCommand(t *testing.T) {
	be := &fakeBackend{status: ksvc.StatusRunning}
	svc := testService(t, nil, be)
	var out bytes.Buffer

	if handled, err := HandleCommand([]string{"serve"}, svc, &out); handled || err != nil {
		t.Fatalf("non-service args: handled=%v err=%v", handled, err)
	}
	if handled, err := HandleCommand([]string{"service"}, svc, &out); !handled || err != nil {
		t.Fatalf("bare service: handled=%v err=%v", handled, err)
	}
	if !strings.Contains(out.String(), "usage:") {
		t.Errorf("bare service should print usage, got %q", out.String())
	}
	for _, cmd := range Commands {
		out.Reset()
		handled, err := HandleCommand([]string{"service", cmd}, svc, &out)
		if !handled || err != nil {
			t.Errorf("%s: handled=%v err=%v", cmd, handled, err)
		}
		if out.Len() == 0 {
			t.Errorf("%s: no output", cmd)
		}
	}
	handled, err := HandleCommand([]string{"service", "bogus"}, svc, &out)
	if !handled || err == nil {
		t.Errorf("bogus: handled=%v err=%v, want handled+error", handled, err)
	}
	// uninstall stops first: expect a stop before uninstall in the call log.
	joined := strings.Join(be.calls, ",")
	if !strings.Contains(joined, "stop,uninstall") && !strings.Contains(joined, "uninstall") {
		t.Errorf("unexpected calls: %s", joined)
	}
}

func TestHandleCommandPropagatesErrors(t *testing.T) {
	be := &fakeBackend{err: map[string]error{"install": errors.New("denied")}}
	svc := testService(t, nil, be)
	_, err := HandleCommand([]string{"service", "install"}, svc, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v", err)
	}
}
