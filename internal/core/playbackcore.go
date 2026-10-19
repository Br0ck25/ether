package core

// RemuxRequirer is implemented by playback managers that can report whether
// a session must be (re)started as a zero-encode remux (HLS/DASH) rather
// than direct play — set when a mid-stream failover of a non-spliceable
// direct-play session found no exact-size replacement release, so the
// client needs fresh container headers at the mapped timestamp.
type RemuxRequirer interface {
	RequiresRemux(sessionID string) bool
}
