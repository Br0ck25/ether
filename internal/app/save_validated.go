package app

import (
	"fmt"
	"strings"

	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/resolver"
)

// saveValidated applies mutate to the current settings, runs the same
// validation as an owner's config save and saves the result. Update applies
// mutate again to settings another node changed meanwhile, so mutate must
// only edit its argument.
func (a *App) saveValidated(mutate func(*config.Config) error) error {
	next := a.cfg.Get()
	if err := mutate(&next); err != nil {
		return err
	}
	if errs := append(admin.ValidateConfig(next), a.validateExtra(next)...); len(errs) > 0 {
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Field+" "+e.Message)
		}
		return fmt.Errorf("%w: %s", resolver.ErrImportRejected, strings.Join(msgs, "; "))
	}
	return a.cfg.Update(mutate)
}
