//go:build !windows

package service

// secureDataDir is a no-op off Windows: config.EnsureDataDir creates the
// data directory owner-only and the store keeps ether.db 0600.
func secureDataDir(string) error { return nil }
