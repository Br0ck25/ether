package config

import "testing"

func TestLLMEndpoint(t *testing.T) {
	cases := []struct {
		cfg  LLM
		id   string
		base string
	}{
		{LLM{Provider: "openai", BaseURL: "http://ignored"}, "openai", "https://api.openai.com/v1"},
		{LLM{Provider: "ollama"}, "ollama", "http://localhost:11434/v1"},
		{LLM{Provider: "ollama", BaseURL: "http://nas:11434/v1/"}, "ollama", "http://nas:11434/v1"},
		{LLM{BaseURL: "http://box:8080/v1"}, LLMProviderCustom, "http://box:8080/v1"},
		{LLM{Provider: "custom"}, LLMProviderCustom, ""},
		{LLM{Provider: "bogus", BaseURL: "http://x"}, "", ""},
	}
	for _, c := range cases {
		p, base := c.cfg.Endpoint()
		if p.ID != c.id || base != c.base {
			t.Errorf("%+v: got %q %q, want %q %q", c.cfg, p.ID, base, c.id, c.base)
		}
	}
	seen := map[string]bool{}
	for _, p := range LLMProviders {
		if seen[p.ID] || p.Label == "" || (p.BaseURL == "" && !p.RequiresBaseURL) {
			t.Errorf("bad provider entry %+v", p)
		}
		seen[p.ID] = true
	}
}
