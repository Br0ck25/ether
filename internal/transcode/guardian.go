package transcode

import (
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"sync"
)

// The guardian is the same executable in a minimal internal mode. A dedicated
// stdin pipe is owned only by Ether: EOF (including an abrupt Ether exit) kills
// the encoder's process tree. This works on platforms without Linux Pdeathsig.
// The encoder never inherits the control pipe.
const guardianEnv = "ETHER_INTERNAL_ENCODER_GUARDIAN"
const guardianArg = "--ether-internal-encoder-guardian"

func init() {
	if os.Getenv(guardianEnv) != "1" || len(os.Args) < 3 || os.Args[1] != guardianArg {
		return
	}
	_ = os.Unsetenv(guardianEnv)
	cmd := exec.Command(os.Args[2], os.Args[3:]...)
	ready := make(chan struct{})
	cmd.Stdout, cmd.Stderr = &guardianOutput{ready: ready}, os.Stderr
	cmd.WaitDelay = waitDelay
	hideWindow(cmd)
	prepareEncoder(cmd)
	if err := cmd.Start(); err != nil {
		os.Exit(127)
	}
	cleanup, err := ownEncoder(cmd)
	if err != nil {
		close(ready)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		os.Exit(127)
	}
	var pid [8]byte
	binary.BigEndian.PutUint64(pid[:], uint64(cmd.Process.Pid))
	if _, err := os.Stdout.Write(pid[:]); err != nil {
		close(ready)
		stopEncoder(cmd)
		cleanup()
		_ = cmd.Wait()
		os.Exit(127)
	}
	close(ready)
	lost := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); close(lost) }()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var exitErr error
	select {
	case exitErr = <-done:
	case <-lost:
		stopEncoder(cmd)
		cleanup()
		exitErr = <-done
	}
	// Clean up descendants even when the encoder exited before them.
	stopEncoder(cmd)
	cleanup()
	if exitErr != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// os/exec copies encoder stdout only after the PID handshake has been sent.
type guardianOutput struct{ ready <-chan struct{} }

func (w *guardianOutput) Write(p []byte) (int, error) { <-w.ready; return os.Stdout.Write(p) }

// encoderOutput strips the internal handshake before forwarding progress data.
type encoderOutput struct {
	dst    io.Writer
	header [8]byte
	n      int
	pid    chan int
	once   sync.Once
}

func (w *encoderOutput) Write(p []byte) (int, error) {
	total := len(p)
	if w.n < len(w.header) {
		n := copy(w.header[w.n:], p)
		w.n += n
		p = p[n:]
		if w.n == len(w.header) {
			w.once.Do(func() { w.pid <- int(binary.BigEndian.Uint64(w.header[:])) })
		}
	}
	if len(p) == 0 {
		return total, nil
	}
	n, err := w.dst.Write(p)
	return total - len(p) + n, err
}
