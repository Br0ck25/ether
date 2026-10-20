package config

import "strings"

// LLM wire protocols.
const (
	LLMAPIOpenAI    = "openai"    // POST {base}/chat/completions, GET {base}/models
	LLMAPIAnthropic = "anthropic" // POST {base}/messages, GET {base}/models
	// LLMAPIGoogle chats through Gemini's OpenAI-compatible surface
	// ({base}/openai/chat/completions) and lists models natively.
	LLMAPIGoogle = "google"
)

// LLMProviderCustom is any OpenAI-compatible endpoint the owner supplies.
const LLMProviderCustom = "custom"

// LLMProvider is one language-model service the owner can pick. The
// hosted entries mirror @braedonsaunders/appkit-ai's AI_PROVIDER_SPECS
// (same IDs, labels and endpoints) so Ether and AppKit apps configure
// providers the same way; Ollama and LM Studio are Ether's local additions.
type LLMProvider struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// API is the wire protocol (LLMAPIOpenAI, LLMAPIAnthropic, LLMAPIGoogle).
	API string `json:"api"`
	// BaseURL is the default endpoint; empty for LLMProviderCustom.
	BaseURL string `json:"baseUrl"`
	// NeedsKey is set for hosted services that reject anonymous calls.
	NeedsKey bool `json:"needsKey"`
	// Local servers run on the owner's network, so their endpoint is
	// editable (BaseURL is only the usual default).
	Local bool `json:"local"`
	// RequiresBaseURL is set when the owner must supply the endpoint.
	RequiresBaseURL bool `json:"requiresBaseUrl"`
	// KeyHint is the API-key placeholder; ModelHint explains model IDs.
	KeyHint   string `json:"keyHint,omitempty"`
	ModelHint string `json:"modelHint,omitempty"`
}

// LLMProviders lists the supported services, hosted first.
var LLMProviders = []LLMProvider{
	{ID: "anthropic", Label: "Anthropic — Claude", API: LLMAPIAnthropic, BaseURL: "https://api.anthropic.com/v1", NeedsKey: true, KeyHint: "sk-ant-…"},
	{ID: "openai", Label: "OpenAI — GPT", API: LLMAPIOpenAI, BaseURL: "https://api.openai.com/v1", NeedsKey: true, KeyHint: "sk-…"},
	{ID: "google", Label: "Google — Gemini", API: LLMAPIGoogle, BaseURL: "https://generativelanguage.googleapis.com/v1beta", NeedsKey: true, KeyHint: "AIza…"},
	{ID: "openrouter", Label: "OpenRouter — any model, one key", API: LLMAPIOpenAI, BaseURL: "https://openrouter.ai/api/v1", NeedsKey: true, KeyHint: "sk-or-…",
		ModelHint: "Models are vendor/model slugs, e.g. openai/gpt-4o."},
	{ID: "groq", Label: "Groq — fast inference", API: LLMAPIOpenAI, BaseURL: "https://api.groq.com/openai/v1", NeedsKey: true, KeyHint: "gsk_…"},
	{ID: "xai", Label: "xAI — Grok", API: LLMAPIOpenAI, BaseURL: "https://api.x.ai/v1", NeedsKey: true, KeyHint: "xai-…"},
	{ID: "deepseek", Label: "DeepSeek", API: LLMAPIOpenAI, BaseURL: "https://api.deepseek.com", NeedsKey: true, KeyHint: "sk-…"},
	{ID: "mistral", Label: "Mistral", API: LLMAPIOpenAI, BaseURL: "https://api.mistral.ai/v1", NeedsKey: true, KeyHint: "Your Mistral API key"},
	{ID: "ollama", Label: "Ollama — on your network", API: LLMAPIOpenAI, BaseURL: "http://localhost:11434/v1", Local: true, KeyHint: "Not needed"},
	{ID: "lmstudio", Label: "LM Studio — on your network", API: LLMAPIOpenAI, BaseURL: "http://localhost:1234/v1", Local: true, KeyHint: "Not needed"},
	{ID: LLMProviderCustom, Label: "Custom (OpenAI-compatible)", API: LLMAPIOpenAI, Local: true, RequiresBaseURL: true, KeyHint: "Your provider API key",
		ModelHint: "Any OpenAI-compatible endpoint, such as Together, Fireworks, vLLM or llama.cpp."},
}

// LookupLLMProvider finds a provider by ID; empty means LLMProviderCustom.
func LookupLLMProvider(id string) (LLMProvider, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = LLMProviderCustom
	}
	for _, p := range LLMProviders {
		if p.ID == id {
			return p, true
		}
	}
	return LLMProvider{}, false
}

// Endpoint resolves the provider and the base URL requests go to: the
// owner's BaseURL for local and custom servers when set, otherwise the
// provider default. An unknown provider resolves to an empty base URL.
func (l LLM) Endpoint() (LLMProvider, string) {
	p, ok := LookupLLMProvider(l.Provider)
	if !ok {
		return p, ""
	}
	base := p.BaseURL
	if u := strings.TrimSpace(l.BaseURL); p.Local && u != "" {
		base = u
	}
	return p, strings.TrimRight(base, "/")
}
