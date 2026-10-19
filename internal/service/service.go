// Package service runs Ether as an operating-system service: systemd on
// Linux, launchd on macOS and the Service Control Manager on Windows, via
// github.com/kardianos/service.
//
// The integrator in cmd/ether wires it roughly like this:
//
//	svc, err := service.New(app.Run, service.Options{
//	    DataDir: config.DefaultDataDir(),
//	    Logger:  log,
//	})
//	if err != nil { return err }
//	// `ether service install|uninstall|start|stop|restart|status`
//	if handled, err := service.HandleCommand(os.Args[1:], svc, os.Stdout); handled {
//	    return err
//	}
//	return svc.Run() // blocks; interactive runs also stop on SIGINT/SIGTERM
//
// A nil RunFunc is allowed for control-only use (the tray app): Install,
// Start, Stop, Restart, Uninstall and Status work, Run returns ErrNoRunner.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	ksvc "github.com/kardianos/service"
)

// RunFunc is the server body. It must return promptly after ctx is cancelled.
type RunFunc func(ctx context.Context) error

// Options configures the Ether service. Zero values get Ether defaults.
type Options struct {
	// Name is the service-manager name. Default "ether".
	Name string
	// DisplayName is shown in service listings. Default "Ether".
	DisplayName string
	// Description for service listings.
	Description string
	// Executable registered with the service manager. Default: os.Executable.
	Executable string
	// Arguments passed to Executable when the service manager starts it.
	// Default: ["serve"] is NOT assumed; empty means no arguments, so the
	// binary's default action must be to serve (or to call Service.Run).
	Arguments []string
	// WorkingDirectory for the service process.
	WorkingDirectory string
	// DataDir, when set, is exported to the service as ETHER_DATA_DIR.
	DataDir string
	// EnvVars are extra environment variables for the service process.
	EnvVars map[string]string
	// UserName runs the service as this user (systemd/Windows). On Linux
	// packages this is the dedicated "ether" user.
	UserName string
	// UserService installs a per-user service (launchd LaunchAgent /
	// systemd --user semantics where supported) instead of a system one.
	// macOS installs are user services by default (see DefaultOptions).
	UserService bool
	// Dependencies are service-manager dependency lines (systemd [Unit]).
	Dependencies []string
	// StopTimeout bounds how long Stop waits for RunFunc to return before
	// giving up (the process is exiting anyway). Default 30s.
	StopTimeout time.Duration
	Logger      *slog.Logger
}

// DefaultOptions returns Options for a stock Ether install on this OS.
func DefaultOptions() Options {
	opts := Options{
		Name:        "ether",
		DisplayName: "Ether",
		Description: "Ether media server (presents as Plex Media Server; streams from Usenet on demand)",
	}
	switch runtime.GOOS {
	case "darwin":
		// PMS on macOS runs as a login item; a LaunchAgent matches that and
		// needs no root.
		opts.UserService = true
	case "linux":
		opts.Dependencies = []string{"After=network-online.target", "Wants=network-online.target"}
	}
	return opts
}

func (o Options) withDefaults() (Options, error) {
	d := DefaultOptions()
	if o.Name == "" {
		o.Name = d.Name
	}
	if o.DisplayName == "" {
		o.DisplayName = d.DisplayName
	}
	if o.Description == "" {
		o.Description = d.Description
	}
	if o.Dependencies == nil {
		o.Dependencies = d.Dependencies
	}
	if runtime.GOOS == "darwin" && !o.UserService {
		// Keep explicit false possible only via UserServiceSet below; the
		// zero value means "platform default" which is a user service.
		o.UserService = d.UserService
	}
	if o.Executable == "" {
		exe, err := os.Executable()
		if err != nil {
			return o, fmt.Errorf("service: locate executable: %w", err)
		}
		o.Executable = exe
	}
	if o.StopTimeout <= 0 {
		o.StopTimeout = 30 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o, nil
}

// Config renders Options into the kardianos/service config that will be
// registered with the OS. Exported for tests and for installer tooling that
// wants to preview the registration.
func (o Options) Config() (*ksvc.Config, error) {
	var err error
	o, err = o.withDefaults()
	if err != nil {
		return nil, err
	}
	env := make(map[string]string, len(o.EnvVars)+1)
	for k, v := range o.EnvVars {
		env[k] = v
	}
	if o.DataDir != "" {
		env["ETHER_DATA_DIR"] = o.DataDir
	}
	cfg := &ksvc.Config{
		Name:             o.Name,
		DisplayName:      o.DisplayName,
		Description:      o.Description,
		UserName:         o.UserName,
		Arguments:        append([]string(nil), o.Arguments...),
		Executable:       o.Executable,
		Dependencies:     append([]string(nil), o.Dependencies...),
		WorkingDirectory: o.WorkingDirectory,
		EnvVars:          env,
		Option: ksvc.KeyValue{
			"UserService": o.UserService,
			"KeepAlive":   true,
			"RunAtLoad":   true,
			"Restart":     "on-failure",
		},
	}
	return cfg, nil
}

// ErrNoRunner is returned by Run on a control-only Service.
var ErrNoRunner = errors.New("service: no RunFunc configured (control-only service)")

// backend is the slice of ksvc.Service we use; fakes implement it in tests.
type backend interface {
	Run() error
	Start() error
	Stop() error
	Restart() error
	Install() error
	Uninstall() error
	Status() (ksvc.Status, error)
	Platform() string
	String() string
}

// Service is Ether's OS-service handle.
type Service struct {
	opts Options
	cfg  *ksvc.Config
	prog *program
	svc  backend
	log  *slog.Logger
}

// New builds a Service around run. run may be nil for control-only use.
func New(run RunFunc, opts Options) (*Service, error) {
	opts2, err := opts.withDefaults()
	if err != nil {
		return nil, err
	}
	cfg, err := opts2.Config()
	if err != nil {
		return nil, err
	}
	prog := &program{run: run, log: opts2.Logger, stopTimeout: opts2.StopTimeout}
	ks, err := ksvc.New(prog, cfg)
	if err != nil {
		return nil, fmt.Errorf("service: %w", err)
	}
	return &Service{opts: opts2, cfg: cfg, prog: prog, svc: ks, log: opts2.Logger}, nil
}

// newWithBackend is the test seam.
func newWithBackend(run RunFunc, opts Options, be backend) (*Service, error) {
	opts2, err := opts.withDefaults()
	if err != nil {
		return nil, err
	}
	cfg, err := opts2.Config()
	if err != nil {
		return nil, err
	}
	return &Service{
		opts: opts2, cfg: cfg,
		prog: &program{run: run, log: opts2.Logger, stopTimeout: opts2.StopTimeout},
		svc:  be, log: opts2.Logger,
	}, nil
}

// Config returns the registration config in effect.
func (s *Service) Config() *ksvc.Config { return s.cfg }

// Platform names the service manager in use (e.g. "linux-systemd").
func (s *Service) Platform() string { return s.svc.Platform() }

// Interactive reports whether we are running in a terminal rather than
// under the service manager.
func Interactive() bool { return ksvc.Interactive() }

// Run blocks serving until the service manager stops us or, interactively,
// until SIGINT/SIGTERM. The RunFunc's context is cancelled on stop and Run
// returns its error (context cancellation is treated as a clean stop).
func (s *Service) Run() error {
	if s.prog.run == nil {
		return ErrNoRunner
	}
	if Interactive() {
		return s.runInteractive()
	}
	if err := s.svc.Run(); err != nil {
		return err
	}
	return s.prog.result()
}

func (s *Service) runInteractive() error {
	ctx, stop := signalContext(context.Background())
	defer stop()
	s.log.Info("ether running interactively; interrupt to stop", "service", s.cfg.Name)
	err := s.prog.run(ctx)
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return err
}

// Install registers the service with the OS. May require elevation.
func (s *Service) Install() error { return wrap("install", s.svc.Install()) }

// Uninstall removes the service registration. May require elevation.
func (s *Service) Uninstall() error { return wrap("uninstall", s.svc.Uninstall()) }

// Start asks the service manager to start Ether.
func (s *Service) Start() error { return wrap("start", s.svc.Start()) }

// Stop asks the service manager to stop Ether (graceful: the RunFunc
// context is cancelled in the service process).
func (s *Service) Stop() error { return wrap("stop", s.svc.Stop()) }

// Restart asks the service manager to restart Ether.
func (s *Service) Restart() error { return wrap("restart", s.svc.Restart()) }

func wrap(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("service %s: %w", action, err)
}

// Status is Ether's service status.
type Status string

const (
	StatusUnknown Status = "unknown"
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
)

// Status reports whether the service is running, stopped, or unknown
// (not installed / undeterminable).
func (s *Service) Status() (Status, error) {
	st, err := s.svc.Status()
	if err != nil {
		return StatusUnknown, err
	}
	switch st {
	case ksvc.StatusRunning:
		return StatusRunning, nil
	case ksvc.StatusStopped:
		return StatusStopped, nil
	default:
		return StatusUnknown, nil
	}
}

// program adapts RunFunc to ksvc.Interface (+ Shutdowner).
type program struct {
	run         RunFunc
	log         *slog.Logger
	stopTimeout time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func (p *program) Start(ksvc.Service) error {
	if p.run == nil {
		return ErrNoRunner
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	done := p.done
	go func() {
		err := p.run(ctx)
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		close(done)
		if err != nil && !errors.Is(err, context.Canceled) && p.log != nil {
			p.log.Error("ether exited with error", "err", err)
		}
	}()
	return nil
}

func (p *program) stopLocked() {
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done == nil {
		return
	}
	timeout := p.stopTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	select {
	case <-done:
	case <-time.After(timeout):
		if p.log != nil {
			p.log.Warn("ether did not stop within timeout; exiting anyway", "timeout", timeout)
		}
	}
}

func (p *program) Stop(ksvc.Service) error     { p.stopLocked(); return nil }
func (p *program) Shutdown(ksvc.Service) error { p.stopLocked(); return nil }

func (p *program) result() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if errors.Is(p.err, context.Canceled) {
		return nil
	}
	return p.err
}
