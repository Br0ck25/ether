//go:build !windows

package transcode

import "errors"

func (ExecRunner) StartRTX(Command) (Process, error) {
	return nil, errors.New("RTX Video requires native Windows")
}
