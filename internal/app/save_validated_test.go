package app

import (
	"errors"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/resolver"
)

// Imports save through the owner's config validation: a change that would
// break release scoring or name a duplicate profile is rejected unsaved.
func TestSaveValidatedRunsConfigValidation(t *testing.T) {
	store, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: store}
	before := len(store.Get().Quality.Profiles)
	err = a.saveValidated(func(c *config.Config) error {
		c.Resolver.CustomFormats = append(c.Resolver.CustomFormats, core.CustomFormat{ID: "bad", Name: "Bad"})
		return nil
	})
	if !errors.Is(err, resolver.ErrImportRejected) {
		t.Fatalf("invalid format saved: %v", err)
	}
	dup := store.Get().Quality.Profiles[0]
	if err := a.saveValidated(func(c *config.Config) error {
		c.Quality.Profiles = append(c.Quality.Profiles, dup)
		return nil
	}); !errors.Is(err, resolver.ErrImportRejected) {
		t.Fatalf("duplicate profile saved: %v", err)
	}
	if err := a.saveValidated(func(c *config.Config) error {
		c.Quality.Profiles = append(c.Quality.Profiles, core.QualityProfile{Name: "Imported", MaxHeight: 1080, MinHeight: 720})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.Get().Quality.Profiles; len(got) != before+1 || got[before].Name != "Imported" {
		t.Fatalf("profiles %+v", got)
	}
}
