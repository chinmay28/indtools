// Package llm is a minimal chat-completion client abstraction with two
// backends: any OpenAI-compatible server (OpenAI, Ollama, vLLM, LM Studio,
// OpenRouter, DeepSeek, ...) and the Anthropic Messages API.
//
// Contract: Client.Complete sends one system prompt plus a user turn and
// returns the assistant's text. Transport failures and HTTP 408/409/429/5xx
// are retryable (see Retry); other HTTP errors are returned as *HTTPError
// and are not retried. Complete never returns an empty string without an
// error.
package llm

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

// Request is one chat completion request.
type Request struct {
	System    string
	User      string
	MaxTokens int
	// Temperature is sent only when non-nil (some models reject it).
	Temperature *float64
}

// Client completes chat requests.
type Client interface {
	Complete(ctx context.Context, req Request) (string, error)
	// Model returns the model identifier, for provenance in the dataset.
	Model() string
}

// ErrRefused is wrapped by errors for requests a provider declined on
// policy grounds; such requests are never retried.
var ErrRefused = errors.New("request refused by provider")

// HTTPError is a non-2xx response from the provider.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	body := e.Body
	if len(body) > 300 {
		body = body[:300] + "..."
	}
	return fmt.Sprintf("http %d: %s", e.Status, strings.TrimSpace(body))
}

// Retryable reports whether a request that failed with err is worth
// retrying: network errors, timeouts and 408/409/429/5xx responses.
func Retryable(err error) bool {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status == 408 || he.Status == 409 || he.Status == 429 || he.Status >= 500
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrRefused) {
		return false
	}
	return err != nil
}

// Config selects and configures a backend.
type Config struct {
	// Provider is "openai" or "anthropic".
	Provider string
	// BaseURL overrides the provider's default endpoint. For "openai" this
	// is the URL that /chat/completions is appended to, e.g.
	// http://localhost:11434/v1 for Ollama.
	BaseURL string
	APIKey  string
	Model   string
	// Timeout bounds one HTTP request. Zero means 5 minutes.
	Timeout time.Duration
	// Retries is the number of additional attempts after a retryable
	// failure. Zero means 3.
	Retries int
}

// New builds a retrying client for cfg.
func New(cfg Config) (Client, error) {
	if cfg.Model == "" {
		return nil, errors.New("llm: model is required")
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	hc := &http.Client{Timeout: timeout}
	var c Client
	switch strings.ToLower(cfg.Provider) {
	case "openai", "ollama", "openai-compatible", "":
		c = NewOpenAI(cfg.BaseURL, cfg.APIKey, cfg.Model, hc)
	case "anthropic", "claude":
		c = NewAnthropic(cfg.BaseURL, cfg.APIKey, cfg.Model, hc)
	default:
		return nil, fmt.Errorf("llm: unknown provider %q (want openai or anthropic)", cfg.Provider)
	}
	retries := cfg.Retries
	if retries == 0 {
		retries = 3
	}
	return Retry(c, retries, time.Second), nil
}

// Retry wraps c so that retryable failures are retried up to `retries`
// more times with exponential backoff (base, 2*base, 4*base, ...) plus
// jitter. A 429 response is backed off at least 5 seconds.
func Retry(c Client, retries int, base time.Duration) Client {
	return &retrying{c: c, retries: retries, base: base}
}

type retrying struct {
	c       Client
	retries int
	base    time.Duration
}

func (r *retrying) Model() string { return r.c.Model() }

func (r *retrying) Complete(ctx context.Context, req Request) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= r.retries; attempt++ {
		if attempt > 0 {
			delay := r.base * time.Duration(1<<uint(attempt-1))
			var he *HTTPError
			if errors.As(lastErr, &he) && he.Status == 429 && delay < 5*time.Second {
				delay = 5 * time.Second
			}
			delay += time.Duration(rand.Int63n(int64(r.base) + 1))
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
			}
		}
		out, err := r.c.Complete(ctx, req)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !Retryable(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("giving up after %d attempts: %w", r.retries+1, lastErr)
}
