package tmdb

import (
	"context"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

// BenchmarkLocalizedInvalidLanguage measures the language validation that
// Localized performs on every call before any network I/O: an invalid
// language returns the title copy immediately.
func BenchmarkLocalizedInvalidLanguage(b *testing.B) {
	c := &Client{}
	title := &core.Title{}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Localized(context.Background(), title, "not-a-language"); err != nil {
			b.Fatal(err)
		}
	}
}
