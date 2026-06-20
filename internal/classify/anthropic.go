package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Doer is the subset of *http.Client the package needs (injectable for tests).
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// anthropicVersion is the required API version header value.
const anthropicVersion = "2023-06-01"

// defaultBaseURL is the Anthropic Messages API endpoint.
const defaultBaseURL = "https://api.anthropic.com"

// Anthropic is a tiny client for the Messages API used for classification.
type Anthropic struct {
	doer    Doer
	key     string
	baseURL string
}

// AnthropicOption configures the Anthropic client.
type AnthropicOption func(*Anthropic)

// WithDoer overrides the HTTP client (used in tests).
func WithDoer(d Doer) AnthropicOption { return func(a *Anthropic) { a.doer = d } }

// WithBaseURL overrides the API base URL (used in tests).
func WithBaseURL(u string) AnthropicOption { return func(a *Anthropic) { a.baseURL = u } }

// NewAnthropic returns a Messages API client.
func NewAnthropic(apiKey string, opts ...AnthropicOption) *Anthropic {
	a := &Anthropic{doer: http.DefaultClient, key: apiKey, baseURL: defaultBaseURL}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Ping makes a minimal request to verify the API key works (used by the
// dashboard's test-connection button).
func (a *Anthropic) Ping(ctx context.Context) error {
	_, _, err := a.Message(ctx, "claude-haiku-4-5", "Reply with OK.", "ping", 1)
	return err
}

// Usage reports token accounting from a Messages response.
type Usage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadInputToks  int `json:"cache_read_input_tokens"`
	CacheCreationInputT int `json:"cache_creation_input_tokens"`
}

// Message sends a single-turn request. The system prompt is sent as a cacheable
// block (cache_control: ephemeral) so the stable classification prefix is
// reused across calls within the 5-minute window. Returns the concatenated text
// of the response plus usage.
func (a *Anthropic) Message(ctx context.Context, model, system, user string, maxTokens int) (string, Usage, error) {
	reqBody := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"system": []map[string]any{{
			"type":          "text",
			"text":          system,
			"cache_control": map[string]any{"type": "ephemeral"},
		}},
		"messages": []map[string]any{{
			"role":    "user",
			"content": user,
		}},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", Usage{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return "", Usage{}, err
	}
	req.Header.Set("x-api-key", a.key)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("content-type", "application/json")

	resp, err := a.doer.Do(req)
	if err != nil {
		return "", Usage{}, fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", Usage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", Usage{}, &APIError{StatusCode: resp.StatusCode, Body: string(body), RetryAfter: resp.Header.Get("Retry-After")}
	}

	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      Usage  `json:"usage"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", Usage{}, fmt.Errorf("anthropic decode: %w", err)
	}
	if out.StopReason == "refusal" {
		return "", out.Usage, ErrRefused
	}
	var text bytes.Buffer
	for _, c := range out.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	return text.String(), out.Usage, nil
}
