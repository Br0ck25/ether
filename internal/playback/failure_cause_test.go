package playback

import (
	"errors"
	"fmt"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestReleasesFailedKeepsTheCause(t *testing.T) {
	auth := fmt.Errorf("Real-Debrid: real-debrid: bad_token: %w", core.ErrDebridAuth)
	if err := releasesFailed(3, auth, false); !errors.Is(err, core.ErrDebridAuth) || !errors.Is(err, core.ErrNotAvailable) {
		t.Fatalf("auth: %v", err)
	}
	uncached := fmt.Errorf("%q: %w", "rel", core.ErrNotCached)
	if err := releasesFailed(2, uncached, true); !errors.Is(err, core.ErrNotCached) || !errors.Is(err, core.ErrNotAvailable) {
		t.Fatalf("uncached: %v", err)
	}
	// Only the last release uncached: not "nothing cached".
	if err := releasesFailed(2, uncached, false); errors.Is(err, core.ErrNotCached) || !errors.Is(err, core.ErrNotAvailable) {
		t.Fatalf("mixed: %v", err)
	}
	// Release-level sentinels stay text: a missing file must not turn the
	// session failure into a 404.
	gone := fmt.Errorf("file: %w", core.ErrNotFound)
	if err := releasesFailed(1, gone, false); errors.Is(err, core.ErrNotFound) || !errors.Is(err, core.ErrNotAvailable) {
		t.Fatalf("release error leaked: %v", err)
	}
}
