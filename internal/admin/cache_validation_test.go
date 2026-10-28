package admin_test

import (
	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/config"
	"testing"
)

func TestCachePolicyValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*config.Cache)
		field  string
	}{
		{"bad content", func(c *config.Cache) { c.PreloadContent = "all-files" }, "cache.preloadContent"},
		{"bad blend", func(c *config.Cache) { v := 101; c.PreloadHouseholdWeight = &v }, "cache.preloadHouseholdWeight"},
		{"bad household", func(c *config.Cache) { c.PreloadHouseholdScope = "everybody" }, "cache.preloadHouseholdScope"},
		{"oversized episode window", func(c *config.Cache) { c.PreloadEpisodesAhead = 25 }, "cache.preloadEpisodesAhead"},
		{"unbounded history", func(c *config.Cache) { c.PreloadHistoryDays = 731 }, "cache.preloadHistoryDays"},
		{"bad mode", func(c *config.Cache) { c.Mode = "infinite-magic" }, "cache.mode"},
		{"negative retention", func(c *config.Cache) { c.RetentionDays = -1 }, "cache.retentionDays"},
		{"oversized warm", func(c *config.Cache) { c.DetailBytes = 65 << 20 }, "cache.detailBytes"},
		{"shelf exceeds total", func(c *config.Cache) { c.PreloadBudgetBytes = c.BudgetBytes + 1 }, "cache.preloadBudgetBytes"},
		{"shelf without disk", func(c *config.Cache) { c.Mode = "off"; c.PreloadEnabled = true; c.PreloadBudgetBytes = 1 << 30 }, "cache.preloadEnabled"},
		{"unbounded count", func(c *config.Cache) { c.PreloadMaxTitles = 5001 }, "cache.preloadMaxTitles"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults(t.TempDir())
			tc.mutate(&cfg.Cache)
			for _, e := range admin.ValidateConfig(cfg) {
				if e.Field == tc.field {
					return
				}
			}
			t.Fatalf("missing validation for %s", tc.field)
		})
	}
	cfg := config.Defaults(t.TempDir())
	cfg.Cache.BudgetBytes = 20 << 40
	cfg.Cache.PreloadEnabled = true
	cfg.Cache.PreloadBudgetBytes = 18 << 40
	cfg.Cache.PreloadMaxTitles = 2500
	cfg.Cache.PreloadSource = "smart"
	cfg.Cache.PreloadContent = "mixed"
	zero := 0
	cfg.Cache.PreloadHouseholdWeight = &zero
	cfg.Cache.PreloadEpisodesAhead = 24
	cfg.Cache.PreloadHouseholdScope = "home"
	if errs := admin.ValidateConfig(cfg); len(errs) > 0 {
		t.Fatal(errs)
	}
}

func TestPassThroughAndUnlimitedCacheDoNotReopenSetup(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	cfg.Cache.BudgetBytes = 0
	for _, mode := range []string{"off", "demand"} {
		cfg.Cache.Mode = mode
		steps, _ := admin.SetupStatus(cfg)
		for _, step := range steps {
			if step.ID == "cache" && !step.Complete {
				t.Fatalf("mode %s reopened cache setup", mode)
			}
		}
	}
}
