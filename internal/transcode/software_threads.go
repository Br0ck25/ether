package transcode

// softwareThreadBudget bounds each encoder instead of allowing every stream
// to create a machine-sized pool. Larger pictures still receive more workers;
// the available CPU budget also respects the Go runtime's container limit.
func softwareThreadBudget(width, height, available int) int {
	threads := 4
	if width > 0 && height > 0 {
		pixels := int64(width) * int64(height)
		switch {
		case pixels <= 720*576:
			threads = 2
		case pixels > 1920*1080:
			threads = 8
		}
	}
	return max(1, min(threads, available))
}
