//go:build windows

package transcode

// StartRTX uses the same crash guardian and kill-on-close job as ffmpeg.
func (ExecRunner) StartRTX(c Command) (Process, error) { return (ExecRunner{}).Start(c) }
