package app

import (
	"context"
	"fmt"
	"time"

	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/segcache"
)

// validateExtra is the app-level config validation.
func (a *App) validateExtra(c config.Config) []admin.FieldError {
	errs := append(validatePlaybackConfig(c), a.validateIndexerSites(c)...)
	if c.Cache.Storage == "shared" {
		if !a.clustered() {
			errs = append(errs, admin.FieldError{Field: "cache.storage", Message: "set up a PostgreSQL cluster before choosing shared NAS storage"})
		}
		if err := segcache.ValidateSharedMount(c.Cache.Dir, a.db); err != nil {
			errs = append(errs, admin.FieldError{Field: "cache.dir", Message: err.Error()})
		}
	}
	return errs
}

// validateIndexerSites keeps password-type settings of built-in sites out
// of the plain settings map, where they would be shown unredacted.
func (a *App) validateIndexerSites(c config.Config) []admin.FieldError {
	var errs []admin.FieldError
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i, ix := range c.Indexers {
		if !ix.IsBuiltin() || len(ix.Settings) == 0 {
			continue
		}
		def, err := a.sites.Definition(ctx, ix.Definition)
		if err != nil {
			continue // a missing definition surfaces when the indexer is searched
		}
		for _, s := range def.Settings {
			if _, plain := ix.Settings[s.Name]; plain && s.Secret() {
				errs = append(errs, admin.FieldError{Field: fmt.Sprintf("indexers[%d].settings.%s", i, s.Name), Message: "is a password; store it in secretSettings"})
			}
		}
	}
	return errs
}
