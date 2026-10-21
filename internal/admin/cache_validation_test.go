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
