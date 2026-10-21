//go:build linux || darwin

package transcode

import (
	"runtime"
	"syscall"
)

// PeakRSSBytes reports the exited encoder's peak resident memory. It excludes
// Ether and other sessions; summing individual peaks is not a tree peak.
func (p *execProcess) PeakRSSBytes() (uint64, bool) {
	select {
	case <-p.done:
		if p.cmd.ProcessState != nil {
			if usage, ok := p.cmd.ProcessState.SysUsage().(*syscall.Rusage); ok && usage.Maxrss > 0 {
				n := uint64(usage.Maxrss)
				if runtime.GOOS == "linux" {
					n *= 1024 // Linux reports KiB; Darwin reports bytes.
				}
				return n, true
			}
		}
	default:
	}
	return 0, false
}
