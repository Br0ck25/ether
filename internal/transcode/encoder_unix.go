//go:build !windows

package transcode

import (
	"os/exec"
	"syscall"
)

func prepareEncoder(cmd *exec.Cmd)         { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func ownEncoder(*exec.Cmd) (func(), error) { return func() {}, nil }
func stopEncoder(cmd *exec.Cmd)            { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
