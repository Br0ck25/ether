//go:build !windows

package transcode

import "os"

func openMuxOutput(path string) (*os.File, error) { return os.Open(path) }
