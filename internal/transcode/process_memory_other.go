//go:build !linux && !darwin

package transcode

// The portable ProcessState on Windows does not retain working-set counters.
// Leave the measurement unavailable rather than reporting zero memory usage.
func (p *execProcess) PeakRSSBytes() (uint64, bool) { return 0, false }
