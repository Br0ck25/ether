//go:build windows

package transcode

import (
	"os"
	"os/exec"
	"syscall"
)

// createNoWindow keeps ffmpeg from flashing a console when Ether runs as a
// service or tray app.
const createNoWindow = 0x08000000

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

func suspend(*os.Process) error { return ErrPauseUnsupported }

func resume(*os.Process) error { return nil }
