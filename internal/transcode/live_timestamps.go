package transcode

// VOD seek/failover keeps timestamps in the title's coordinate system. Live
// HLS/TS inputs have no fixed duration and can reset their transport clocks.
// copyts disables FFmpeg's discontinuity correction and can leave video minutes
// ahead of audio, so let FFmpeg normalize the initial live run instead.
func inputTimestampArgs(s runSpec) []string {
	if s.Info.Duration <= 0 && s.Start == 0 {
		return nil
	}
	return []string{"-start_at_zero", "-copyts"}
}
