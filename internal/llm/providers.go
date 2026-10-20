package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/braedonsaunders/ether/internal/config"
)

// anthropicVersion is the Messages API version header.
const anthropicVersion = "2023-06-01"

// anthropicMaxTokens bounds one reply; the features here ask for short
// JSON lists or a 150-word recap.
const anthropicMaxTokens = 4096

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
}

func anthropicHeaders(key string) http.Header {
	h := http.Header{}
	h.Set("x-api-key", key)
	h.Set("anthropic-version", anthropicVersion)
	return h
}

// anthropicChat speaks the native Messages API. System messages move to
// the top-level system field. No temperature is sent (current Claude
// models reject it) and no assistant prefill (also rejected), so JSON
// comes from the prompt alone; the parsers are tolerant.
func (c *Client) anthropicChat(ctx context.Context, base string, cfg config.LLM, msgs []Message) (string, error) {
	req := anthropicRequest{Model: cfg.Model, MaxTokens: anthropicMaxTokens}
	var system []string
	for _, m := range msgs {
		if m.Role == "system" {
			system = append(system, m.Content)
			continue
		}
		req.Messages = append(req.Messages, anthropicMessage{Role: m.Role, Content: m.Content})
	}
	req.System = strings.Join(system, "\n\n")
	var out anthropicResponse
	if _, err := c.postJSON(ctx, base+"/messages", anthropicHeaders(cfg.APIKey), req, &out); err != nil {
		return "", fmt.Errorf("llm chat: %w", err)
	}
	if out.StopReason == "refusal" {
		return "", fmt.Errorf("llm chat: the model declined the request")
	}
	var text strings.Builder
	for _, b := range out.Content {
		if b.Type == "text" {
			text.WriteString(b.Text)
		}
	}
	if text.Len() == 0 {
		return "", fmt.Errorf("llm chat: empty reply")
	}
	return text.String(), nil
}

// Model is one model a provider offers.
type Model struct {
	ID string `json:"id"`
	// Label is the provider's display name when it differs from the ID.
	Label string `json:"label,omitempty"`
}

// Models lists the chat models the configured provider offers, sorted by
// ID. It works whether or not the LLM is enabled, so the owner can pick a
// model before switching it on. Discovery follows appkit-ai's listModels:
// OpenAI keeps only GPT/o-series chat models, Gemini only models that
// support generateContent, and OpenAI-compatible servers drop embedding,
// speech and image models.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	cfg := c.config()
	p, base := cfg.Endpoint()
	if base == "" {
		return nil, fmt.Errorf("an endpoint URL is required")
	}
	if p.NeedsKey && cfg.APIKey == "" {
		return nil, fmt.Errorf("%s needs an API key to list models", p.Label)
	}
	var models []Model
	var err error
	switch {
	case p.API == config.LLMAPIAnthropic:
		models, err = c.anthropicModels(ctx, base, cfg.APIKey)
	case p.API == config.LLMAPIGoogle:
		models, err = c.googleModels(ctx, base, cfg.APIKey)
	default:
		models, err = c.openAIModels(ctx, base, cfg.APIKey, p.ID == "openai")
	}
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	out := make([]Model, 0, len(models))
	seen := map[string]bool{}
	for _, m := range models {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		if m.Label == m.ID {
			m.Label = ""
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (c *Client) getJSON(ctx context.Context, url string, h http.Header, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header = h
	_, err = c.do(req, out)
	return err
}

// openAIModels reads GET {base}/models. Hosted services answer {"data":
// [...]}; some local servers answer {"models": [...]} keyed by name.
func (c *Client) openAIModels(ctx context.Context, base, key string, openAI bool) ([]Model, error) {
	type entry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var out struct {
		Data   []entry `json:"data"`
		Models []entry `json:"models"`
	}
	if err := c.getJSON(ctx, base+"/models", openAIHeaders(key), &out); err != nil {
		return nil, err
	}
	list := out.Data
	if len(list) == 0 {
		list = out.Models
	}
	models := make([]Model, 0, len(list))
	for _, d := range list {
		id := d.ID
		if id == "" {
			id = d.Name
		}
		if openAI && !openAIChatModel(id) || !openAI && !chatModel(id) {
			continue
		}
		models = append(models, Model{ID: id, Label: d.Name})
	}
	return models, nil
}

// googleModels reads Gemini's native model list. The key goes in a header,
// not the query string, so it stays out of proxy and access logs.
func (c *Client) googleModels(ctx context.Context, base, key string) ([]Model, error) {
	h := http.Header{}
	h.Set("x-goog-api-key", key)
	var models []Model
	token := ""
	for page := 0; page < 10; page++ {
		q := url.Values{"pageSize": {"1000"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		var out struct {
			Models []struct {
				Name                       string   `json:"name"`
				DisplayName                string   `json:"displayName"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.getJSON(ctx, base+"/models?"+q.Encode(), h, &out); err != nil {
			return nil, err
		}
		for _, m := range out.Models {
			if slices.Contains(m.SupportedGenerationMethods, "generateContent") {
				models = append(models, Model{ID: strings.TrimPrefix(m.Name, "models/"), Label: m.DisplayName})
			}
		}
		if out.NextPageToken == "" {
			break
		}
		token = out.NextPageToken
	}
	return models, nil
}

// anthropicModels pages through GET /v1/models.
func (c *Client) anthropicModels(ctx context.Context, base, key string) ([]Model, error) {
	var models []Model
	after := ""
	for page := 0; page < 10; page++ {
		q := url.Values{"limit": {"1000"}}
		if after != "" {
			q.Set("after_id", after)
		}
		var out struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := c.getJSON(ctx, base+"/models?"+q.Encode(), anthropicHeaders(key), &out); err != nil {
			return nil, err
		}
		for _, d := range out.Data {
			models = append(models, Model{ID: d.ID, Label: d.DisplayName})
		}
		if !out.HasMore || out.LastID == "" {
			break
		}
		after = out.LastID
	}
	return models, nil
}

var (
	// nonChatRe marks model IDs that cannot answer a chat request.
	nonChatRe = regexp.MustCompile(`(?i)embed|whisper|tts|audio|dall-e|image|moderation|realtime|transcribe|rerank|babbage|davinci`)
	// openAINonChatRe adds OpenAI's search/similarity/edit families.
	openAINonChatRe = regexp.MustCompile(`(?i)search|similarity|edit`)
	openAIChatRe    = regexp.MustCompile(`(?i)^(gpt-|o1|o3|o4|chatgpt)`)
)

func chatModel(id string) bool { return !nonChatRe.MatchString(id) }

// openAIChatModel keeps OpenAI's GPT and o-series chat models; the list
// also carries embedding, speech, image and legacy completion models.
func openAIChatModel(id string) bool {
	return chatModel(id) && !openAINonChatRe.MatchString(id) && openAIChatRe.MatchString(id)
}
