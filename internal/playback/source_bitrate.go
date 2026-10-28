package playback

import "time"

// Cached preparation must respect a new connection's bitrate as well as its
// codec/height capabilities. Resolve afresh to try a smaller source first;
// when none is available the normal decision can still transcode a large one.
func sourceFitsBitrate(size int64, duration time.Duration, bitrate, limit int) bool {
	if limit <= 0 {
		return true
	}
	average := float64(bitrate)
	if size > 0 && duration > 0 {
		average = float64(size) * 8 / duration.Seconds() / 1000
	}
	return average <= 0 || average*1.1 <= float64(limit)
}
