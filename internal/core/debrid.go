package core

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
)

// Release protocols (Release.Protocol).
const (
	ProtocolUsenet  = ""        // the zero value: an NZB from a Newznab indexer
	ProtocolTorrent = "torrent" // a Torznab result streamed through a debrid service
)

// ReleaseTarget is the episode wanted from a multi-file release such as a
// season pack.
type ReleaseTarget struct {
	Season  int `json:"season,omitempty"`
	Episode int `json:"episode,omitempty"`
}

// ErrNotCached reports that no debrid provider can serve a torrent release
// instantly. It is a per-release skip, never proof the release is broken,
// so it wraps ErrNotAvailable and is not a permanent failure.
var ErrNotCached = fmt.Errorf("not cached by any debrid provider: %w", ErrNotAvailable)

// TargetReleaseID derives a stable release ID for one episode of a
// multi-file release, so health, pins, cache and bad marks apply to the
// episode file rather than the whole pack.
func TargetReleaseID(releaseID string, t ReleaseTarget) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%s|s%de%d", releaseID, t.Season, t.Episode)))
	return hex.EncodeToString(h[:])
}
