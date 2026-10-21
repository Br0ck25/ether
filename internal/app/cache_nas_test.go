package app

import (
	"context"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/segcache"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestStandaloneNASValidationAndIdentity(t *testing.T) {
	db, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &App{db: db}
	cfg := config.Defaults(t.TempDir())
	policy := cfg.Cache
	policy.Storage, policy.Dir = "shared", t.TempDir()
	cfg.SetCachePolicy(policy)
	if errs := a.validateExtra(cfg); len(errs) != 0 {
		t.Fatalf("standalone NAS rejected: %v", errs)
	}
	c, err := segcache.OpenShared(policy.Dir, policy.BudgetBytes, db, a.cacheNodeID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Put("episode", 0, []byte("opening")); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.Get("episode", 0); !ok || string(got) != "opening" {
		t.Fatalf("NAS read: %q %v", got, ok)
	}
	if errs := a.validateExtra(cfg); len(errs) != 0 {
		t.Fatalf("existing NAS rejected: %v", errs)
	}
	cfg.Cache.Dir = t.TempDir()
	if errs := a.validateExtra(cfg); len(errs) == 0 {
		t.Fatal("wrong mount accepted")
	}
}
