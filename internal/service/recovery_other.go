//go:build !windows

package service

// applyPlatformRecovery is a no-op where the unit/plist rendered by
// kardianos/service already carries the restart policy (Restart=always,
// KeepAlive).
func applyPlatformRecovery(string) error { return nil }
