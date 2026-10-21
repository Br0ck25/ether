package admin_test

import (
	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/config"
	"strings"
	"testing"
)

func TestRTXVideoConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*config.RTXVideo)
		field  string
	}{
		{"mode", func(c *config.RTXVideo) { c.Mode = "fake" }, "mode"},
		{"source", func(c *config.RTXVideo) { c.SourceMaxHeight = -1 }, "sourceMaxHeight"},
		{"target", func(c *config.RTXVideo) { c.TargetMaxHeight = 4320 }, "targetMaxHeight"},
		{"quality", func(c *config.RTXVideo) { c.Quality = 22 }, "quality"},
		{"concurrency", func(c *config.RTXVideo) { c.MaxConcurrent = 9 }, "maxConcurrent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults(t.TempDir())
			tc.mutate(&cfg.Transcode.RTXVideo)
			for _, e := range admin.ValidateConfig(cfg) {
				if e.Field == "transcode.rtxVideo."+tc.field {
					return
				}
			}
			t.Fatal("missing field validation")
		})
	}
	for _, quality := range []int{0, 1, 2, 3, 4, 21, 23} {
		cfg := config.Defaults(t.TempDir())
		cfg.Transcode.RTXVideo = config.RTXVideo{Quality: quality}
		for _, e := range admin.ValidateConfig(cfg) {
			if strings.HasPrefix(e.Field, "transcode.rtxVideo.") {
				t.Fatal(e)
			}
		}
	}
}
