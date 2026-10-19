package llm

import (
	"context"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"testing"
)

func TestRuntimeUpdateDiscardsOldFlight(t *testing.T) {
	c := NewClient(ClientOptions{Config: config.LLM{Enabled: true, BaseURL: "http://old.invalid", Model: "old"}})
	s := New(ServiceOptions{Client: c, Catalog: fakeCatalog{}})
	ready, release := make(chan struct{}), make(chan struct{})
	s.mu.Lock()
	done := s.refreshLocked("x", "", func(context.Context) ([]core.Hub, []core.Title, error) {
		close(ready)
		<-release
		return nil, []core.Title{{Title: "old"}}, nil
	})
	s.mu.Unlock()
	<-ready
	s.UpdateConfig(config.LLM{Enabled: true, BaseURL: "http://new.invalid", Model: "new"})
	close(release)
	<-done
	s.mu.Lock()
	_, cached := s.cache["x"]
	s.mu.Unlock()
	if cached {
		t.Fatal("old settings repopulated cache")
	}
	if !s.Enabled() || c.cfg.Model != "new" {
		t.Fatal("new settings not active")
	}
	s.UpdateConfig(config.LLM{})
	if s.Enabled() {
		t.Fatal("runtime disable ignored")
	}
}
