package transcode

import "io"

// readMuxOutput reads a muxer-owned file with Windows delete sharing and
// bounded retries for transient sharing violations.
func readMuxOutput(path string) ([]byte, error) {
	f, err := openMuxOutput(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
