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

// DefaultOpenAIBaseURL is used when no base URL is configured.
const DefaultOpenAIBaseURL = "https://api.openai.com/v1"

// OpenAI talks to any server implementing POST {base}/chat/completions.
type OpenAI struct {
	BaseURL string
	APIKey  string
	model   string
	HTTP    *http.Client
}

// NewOpenAI constructs an OpenAI-compatible client. An empty baseURL means
// DefaultOpenAIBaseURL; a nil hc means http.DefaultClient.
func NewOpenAI(baseURL, apiKey, model string, hc *http.Client) *OpenAI {
	if baseURL == "" {
		baseURL = DefaultOpenAIBaseURL
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &OpenAI{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, model: model, HTTP: hc}
}

func (o *OpenAI) Model() string { return o.model }

type oaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type oaiRequest struct {
	Model       string       `json:"model"`
	Messages    []oaiMessage `json:"messages"`
	MaxTokens   int          `json:"max_tokens,omitempty"`
	Temperature *float64     `json:"temperature,omitempty"`
}

type oaiResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete implements Client.
func (o *OpenAI) Complete(ctx context.Context, req Request) (string, error) {
	body := oaiRequest{Model: o.model, MaxTokens: req.MaxTokens, Temperature: req.Temperature}
	if req.System != "" {
		body.Messages = append(body.Messages, oaiMessage{Role: "system", Content: req.System})
	}
	body.Messages = append(body.Messages, oaiMessage{Role: "user", Content: req.User})
	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	resp, err := o.HTTP.Do(hreq)
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
	var out oaiResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("openai: bad response: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("openai: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("openai: response has no choices")
	}
	text := out.Choices[0].Message.Content
	if strings.TrimSpace(text) == "" {
		return "", errors.New("openai: empty completion")
	}
	return text, nil
}
