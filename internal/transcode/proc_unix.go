//go:build !windows

package transcode

import (
	"os"
	"os/exec"
	"syscall"
)

func hideWindow(*exec.Cmd) {}

func suspend(p *os.Process) error { return p.Signal(syscall.SIGSTOP) }

func resume(p *os.Process) error { return p.Signal(syscall.SIGCONT) }
