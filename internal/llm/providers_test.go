package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
)

func TestAnthropicChatNative(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "sk-ant" || r.Header.Get("anthropic-version") != anthropicVersion {
			t.Errorf("request: %s %v", r.URL.Path, r.Header)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"content":[{"type":"thinking","thinking":""},{"type":"text","text":"{\"titles\":"},{"type":"text","text":"[]}"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()
	c := NewClient(ClientOptions{})
	out, err := c.anthropicChat(context.Background(), srv.URL+"/v1", config.LLM{Model: "claude-x", APIKey: "sk-ant"},
		[]Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "hi"}})
	if err != nil || out != `{"titles":[]}` {
		t.Fatalf("chat: %q %v", out, err)
	}
	if got["system"] != "be brief" || got["temperature"] != nil || got["max_tokens"] == nil {
		t.Fatalf("body: %v", got)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Fatalf("messages: %v", msgs)
	}
}

func TestAnthropicRefusalAndErrors(t *testing.T) {
	body := `{"content":[],"stop_reason":"refusal"}`
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	c := NewClient(ClientOptions{})
	if _, err := c.anthropicChat(context.Background(), srv.URL, config.LLM{}, nil); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("refusal: %v", err)
	}
	status, body = 401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
	if _, err := c.anthropicChat(context.Background(), srv.URL, config.LLM{}, nil); err == nil || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Fatalf("auth error should carry provider message: %v", err)
	}
}

func TestOpenAIDropsRejectedTemperature(t *testing.T) {
	var temps []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		temps = append(temps, req["temperature"])
		if req["temperature"] != nil {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"message":"Unsupported value: 'temperature' does not support 0.4 with this model."}}`))
			return
		}
		if req["response_format"] == nil {
			t.Error("response_format dropped although only temperature was rejected")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()
	c := NewClient(ClientOptions{Config: config.LLM{Enabled: true, BaseURL: srv.URL, Model: "gpt-5"}})
	out, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, true)
	if err != nil || out != "ok" || len(temps) != 2 {
		t.Fatalf("got %q %v after %v", out, err, temps)
	}
}

func TestGeminiChatsThroughOpenAISurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/openai/chat/completions" || r.Header.Get("Authorization") != "Bearer AIza" {
			t.Errorf("request: %s %v", r.URL.Path, r.Header)
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()
	c := NewClient(ClientOptions{})
	if out, err := c.openAIChat(context.Background(), srv.URL+"/v1beta/openai", config.LLM{Model: "gemini-2.5-flash", APIKey: "AIza"}, nil, false); err != nil || out != "ok" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestEnabledResolvesProviderEndpoint(t *testing.T) {
	c := NewClient(ClientOptions{Config: config.LLM{Enabled: true, Provider: "openai", Model: "gpt-4o"}})
	if !c.Enabled() {
		t.Fatal("hosted provider needs no base URL")
	}
	c.UpdateConfig(config.LLM{Enabled: true, Provider: "custom", Model: "m"})
	if c.Enabled() {
		t.Fatal("custom without a base URL is not usable")
	}
	c.UpdateConfig(config.LLM{Enabled: true, Provider: "nope", BaseURL: "http://x", Model: "m"})
	if c.Enabled() {
		t.Fatal("unknown provider enabled")
	}
}

func TestModelsOpenAICompatible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Write([]byte(`{"data":[{"id":"zeta"},{"id":"alpha","name":"Alpha Model"},{"id":"nomic-embed-text"},{"id":"alpha"},{"id":"whisper-large-v3"},{"id":"beta","name":"beta"}]}`))
	}))
	defer srv.Close()
	c := NewClient(ClientOptions{Config: config.LLM{Provider: "ollama", BaseURL: srv.URL + "/v1/"}})
	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Model{{ID: "alpha", Label: "Alpha Model"}, {ID: "beta"}, {ID: "zeta"}}
	if len(models) != len(want) {
		t.Fatalf("models: %+v", models)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Fatalf("models[%d] = %+v, want %+v", i, models[i], want[i])
		}
	}
}

func TestModelsLocalServerModelsKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"name":"llama3.1:8b"}]}`))
	}))
	defer srv.Close()
	c := NewClient(ClientOptions{Config: config.LLM{Provider: "custom", BaseURL: srv.URL}})
	models, err := c.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "llama3.1:8b" {
		t.Fatalf("%+v %v", models, err)
	}
}

func TestOpenAIChatModelFilter(t *testing.T) {
	keep := []string{"gpt-4o", "gpt-5-mini", "o3", "o4-mini", "chatgpt-4o-latest"}
	drop := []string{"text-embedding-3-large", "gpt-image-1", "tts-1", "whisper-1", "dall-e-3", "gpt-4o-realtime-preview",
		"gpt-4o-audio-preview", "omni-moderation-latest", "davinci-002", "gpt-4o-search-preview", "ft:custom"}
	for _, id := range keep {
		if !openAIChatModel(id) {
			t.Errorf("dropped %s", id)
		}
	}
	for _, id := range drop {
		if openAIChatModel(id) {
			t.Errorf("kept %s", id)
		}
	}
}

func TestModelsAnthropicPaginates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "k" || r.URL.Query().Get("limit") != "1000" {
			t.Errorf("request: %v %v", r.Header, r.URL)
		}
		if r.URL.Query().Get("after_id") == "" {
			w.Write([]byte(`{"data":[{"id":"claude-b","display_name":"Claude B"}],"has_more":true,"last_id":"claude-b"}`))
			return
		}
		w.Write([]byte(`{"data":[{"id":"claude-a","display_name":"Claude A"}],"has_more":false}`))
	}))
	defer srv.Close()
	c := NewClient(ClientOptions{})
	models, err := c.anthropicModels(context.Background(), srv.URL, "k")
	if err != nil || len(models) != 2 || models[1].Label != "Claude A" {
		t.Fatalf("%+v %v", models, err)
	}
}

func TestModelsGoogleNative(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "AIza" || r.URL.Query().Get("key") != "" {
			t.Errorf("key must travel in a header: %v", r.URL)
		}
		w.Write([]byte(`{"models":[
			{"name":"models/gemini-2.5-flash","displayName":"Gemini 2.5 Flash","supportedGenerationMethods":["generateContent","countTokens"]},
			{"name":"models/text-embedding-004","displayName":"Embedding","supportedGenerationMethods":["embedContent"]}]}`))
	}))
	defer srv.Close()
	c := NewClient(ClientOptions{})
	models, err := c.googleModels(context.Background(), srv.URL, "AIza")
	if err != nil || len(models) != 1 || models[0].ID != "gemini-2.5-flash" || models[0].Label != "Gemini 2.5 Flash" {
		t.Fatalf("%+v %v", models, err)
	}
}

func TestModelsNeedsKeyAndEndpoint(t *testing.T) {
	c := NewClient(ClientOptions{Config: config.LLM{Provider: "anthropic"}})
	if _, err := c.Models(context.Background()); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("missing key: %v", err)
	}
	c.UpdateConfig(config.LLM{Provider: "custom"})
	if _, err := c.Models(context.Background()); err == nil {
		t.Fatal("custom without endpoint should error")
	}
}
