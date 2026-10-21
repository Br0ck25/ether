package transcode

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Command describes one ffmpeg/ffprobe invocation.
type Command struct {
	Path   string
	Args   []string
	Dir    string
	Stdout io.Writer
	Stderr io.Writer
}

// Process is a started long-running command (an ffmpeg HLS run).
type Process interface {
	// Done is closed when the process has exited.
	Done() <-chan struct{}
	// Err is the exit error; valid once Done is closed.
	Err() error
	// Stop kills the process and blocks until it is gone. It is safe to
	// call more than once and after exit.
	Stop()
	// Pause suspends the process (throttling); ErrPauseUnsupported where
	// the platform cannot.
	Pause() error
	// Resume continues a paused process.
	Resume() error
}

// Runner starts processes. The default runs real executables; tests inject
// fakes.
type Runner interface {
	// Start launches a long-running command.
	Start(cmd Command) (Process, error)
	// Run executes a command to completion, killing it when ctx ends.
	Run(ctx context.Context, cmd Command) error
}

// ErrPauseUnsupported is returned by Process.Pause on platforms that cannot
// suspend a process.
var ErrPauseUnsupported = errors.New("transcode: pausing processes is not supported on this platform")

// waitDelay bounds how long Run waits for output pipes after a kill.
const waitDelay = 4 * time.Second

// ExecRunner runs real executables with os/exec.
type ExecRunner struct{}

// Start implements Runner.
func (ExecRunner) Start(c Command) (Process, error) {
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &execProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, c Command) error {
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	cmd.WaitDelay = waitDelay
	hideWindow(cmd)
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

type execProcess struct {
	cmd      *exec.Cmd
	done     chan struct{}
	err      error
	stopOnce sync.Once
	mu       sync.Mutex
	paused   bool
}

func (p *execProcess) Done() <-chan struct{} { return p.done }

// PID identifies the encoder for optional process-tree resource sampling.
// It is deliberately not part of Process, so fake and alternate runners do
// not need to expose an operating-system process.
func (p *execProcess) PID() int { return p.cmd.Process.Pid }

// CPUTime reports completed process CPU usage, excluding wall-clock waits.
// While the process is running the values are zero; callers should sample
// only after Done closes. The channel synchronizes access to ProcessState.
func (p *execProcess) CPUTime() (user, system time.Duration) {
	select {
	case <-p.done:
		if state := p.cmd.ProcessState; state != nil {
			return state.UserTime(), state.SystemTime()
		}
	default:
	}
	return 0, 0
}

func (p *execProcess) Err() error {
	<-p.done
	return p.err
}

func (p *execProcess) Stop() {
	p.stopOnce.Do(func() {
		select {
		case <-p.done:
		default:
			// Segments and playlists are written to temp files and renamed,
			// so a hard kill never leaves a half-written segment visible.
			_ = p.cmd.Process.Kill()
		}
	})
	<-p.done
}

func (p *execProcess) Pause() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.paused {
		return nil
	}
	if err := suspend(p.cmd.Process); err != nil {
		return err
	}
	p.paused = true
	return nil
}

func (p *execProcess) Resume() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.paused {
		return nil
	}
	if err := resume(p.cmd.Process); err != nil {
		return err
	}
	p.paused = false
	return nil
}
