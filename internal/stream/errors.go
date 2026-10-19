package stream

import (
	"errors"

	"github.com/braedonsaunders/ether/internal/archive"
	"github.com/braedonsaunders/ether/internal/nzb"
)

// Typed errors returned by Open, Prefetch and Check. They are re-exported
// from the packages that detect them so callers (the resolver, playback)
// only need to import stream. Errors are wrapped with context; use
// errors.Is.
var (
	// ErrNoPlayableFile: the release has no video file the engine can serve
	// (only samples/extras, unsupported containers, or nothing recognisable).
	ErrNoPlayableFile = errors.New("stream: no playable video file in release")
	// ErrCompressed: the video sits in a compressed (non-store) archive.
	ErrCompressed = archive.ErrCompressed
	// ErrEncrypted: the archive is password protected (headers or data).
	ErrEncrypted = archive.ErrEncrypted
	// ErrSolid: the archive is solid, so a file cannot be read in isolation.
	ErrSolid = archive.ErrSolid
	// ErrUnsupported: an archive feature the engine cannot stream.
	ErrUnsupported = archive.ErrUnsupported
	// ErrCorrupt: archive headers or the segment layout are inconsistent.
	ErrCorrupt = archive.ErrCorrupt
	// ErrInvalidNZB: the NZB could not be parsed.
	ErrInvalidNZB = nzb.ErrInvalid
	// ErrIncomplete: the NZB itself lacks segments or volumes of the main
	// file. It wraps core.ErrArticleMissing (see incompleteError).
	ErrIncomplete = errors.New("stream: release is incomplete")
	// ErrClosed is returned by ReadAt after Close.
	ErrClosed = errors.New("stream: source closed")
)

// IsRejection reports whether err means the release itself can never be
// streamed (as opposed to a transient network or context error), so a
// resolver should move on and may mark the release bad.
func IsRejection(err error) bool {
	for _, target := range []error{ErrNoPlayableFile, ErrCompressed, ErrEncrypted, ErrSolid, ErrUnsupported, ErrCorrupt, ErrInvalidNZB, ErrIncomplete, archive.ErrMissingVolume} {
		if errors.Is(err, target) {
			return true
		}
	}
	return isMissing(err)
}
