package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultAnthropicBaseURL is used when no base URL is configured.
const DefaultAnthropicBaseURL = "https://api.anthropic.com"

// DefaultAnthropicModel is the model used when none is specified.
const DefaultAnthropicModel = "claude-opus-5-5"

const anthropicVersion = "2023-06-01"

// Anthropic talks to the Messages API (POST {base}/v1/messages).
type Anthropic struct {
	BaseURL string
	APIKey  string
	model   string
	HTTP    *http.Client
}

// NewAnthropic constructs a Messages API client. An empty baseURL means
// DefaultAnthropicBaseURL; a nil hc means http.DefaultClient.
func NewAnthropic(baseURL, apiKey, model string, hc *http.Client) *Anthropic {
	if baseURL == "" {
		baseURL = DefaultAnthropicBaseURL
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Anthropic{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, model: model, HTTP: hc}
}

func (a *Anthropic) Model() string { return a.model }

type antMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type antRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    string       `json:"system,omitempty"`
	Messages  []antMessage `json:"messages"`
	// Temperature is only sent when set; current Claude models reject
	// sampling parameters, so callers normally leave it nil.
	Temperature *float64 `json:"temperature,omitempty"`
}

type antResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason  string `json:"stop_reason"`
	StopDetails *struct {
		Category    string `json:"category"`
		Explanation string `json:"explanation"`
	} `json:"stop_details"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Complete implements Client.
func (a *Anthropic) Complete(ctx context.Context, req Request) (string, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	body := antRequest{
		Model:       a.model,
		MaxTokens:   maxTokens,
		System:      req.System,
		Messages:    []antMessage{{Role: "user", Content: req.User}},
		Temperature: req.Temperature,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/v1/messages", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("anthropic-version", anthropicVersion)
	if a.APIKey != "" {
		hreq.Header.Set("x-api-key", a.APIKey)
	}
	resp, err := a.HTTP.Do(hreq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", &HTTPError{Status: resp.StatusCode, Body: string(raw)}
	}
	var out antResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("anthropic: bad response: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("anthropic: %s: %s", out.Error.Type, out.Error.Message)
	}
	if out.StopReason == "refusal" {
		if out.StopDetails != nil {
			return "", fmt.Errorf("anthropic: %w by safety classifier (%s: %s)", ErrRefused, out.StopDetails.Category, out.StopDetails.Explanation)
		}
		return "", fmt.Errorf("anthropic: %w by safety classifier", ErrRefused)
	}
	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	if strings.TrimSpace(sb.String()) == "" {
		return "", errors.New("anthropic: empty completion")
	}
	return sb.String(), nil
}
