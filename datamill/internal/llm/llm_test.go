package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAIComplete(t *testing.T) {
	var got oaiRequest
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi there"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	c := NewOpenAI(srv.URL+"/v1/", "key", "gpt-x", nil)
	temp := 0.7
	out, err := c.Complete(context.Background(), Request{System: "sys", User: "hello", MaxTokens: 10, Temperature: &temp, ReasoningEffort: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hi there" || auth != "Bearer key" || c.Model() != "gpt-x" {
		t.Fatalf("out=%q auth=%q", out, auth)
	}
	if got.Model != "gpt-x" || len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[1].Content != "hello" || got.MaxTokens != 10 || got.Temperature == nil || *got.Temperature != 0.7 || got.ReasoningEffort != "none" {
		t.Fatalf("request %+v", got)
	}
}

func TestOpenAIOmitsUnsetOptionals(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&raw)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()
	c := NewOpenAI(srv.URL, "", "m", nil)
	if _, err := c.Complete(context.Background(), Request{User: "hi"}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"temperature", "reasoning_effort", "max_tokens"} {
		if _, ok := raw[k]; ok {
			t.Errorf("%s should be omitted when unset: %v", k, raw)
		}
	}
}

func TestOpenAIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Case") {
		case "429":
			w.WriteHeader(429)
			w.Write([]byte(`{"error":{"message":"slow down"}}`))
		case "400":
			w.WriteHeader(400)
			w.Write([]byte(`bad`))
		case "empty":
			w.Write([]byte(`{"choices":[{"message":{"content":""}}]}`))
		case "nochoices":
			w.Write([]byte(`{"choices":[]}`))
		default:
			w.Write([]byte(`not json`))
		}
	}))
	defer srv.Close()
	for _, cse := range []struct {
		name      string
		retryable bool
	}{{"429", true}, {"400", false}, {"empty", true}, {"nochoices", true}, {"garbage", true}} {
		c := NewOpenAI(srv.URL, "", "m", &http.Client{Transport: headerRT{cse.name}})
		_, err := c.Complete(context.Background(), Request{User: "x"})
		if err == nil {
			t.Fatalf("%s: expected error", cse.name)
		}
		if Retryable(err) != cse.retryable {
			t.Fatalf("%s: Retryable=%v err=%v", cse.name, Retryable(err), err)
		}
		var he *HTTPError
		if (cse.name == "429" || cse.name == "400") && !errors.As(err, &he) {
			t.Fatalf("%s: expected HTTPError, got %T", cse.name, err)
		}
	}
}

type headerRT struct{ v string }

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("X-Case", h.v)
	return http.DefaultTransport.RoundTrip(r)
}

func TestAnthropicComplete(t *testing.T) {
	var got antRequest
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path %s", r.URL.Path)
		}
		hdr = r.Header.Clone()
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"content":[{"type":"thinking","thinking":""},{"type":"text","text":"part one "},{"type":"text","text":"part two"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()
	c := NewAnthropic(srv.URL, "sk", "claude-x", nil)
	out, err := c.Complete(context.Background(), Request{System: "sys", User: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "part one part two" {
		t.Fatalf("out %q", out)
	}
	if hdr.Get("x-api-key") != "sk" || hdr.Get("anthropic-version") == "" {
		t.Fatalf("headers %v", hdr)
	}
	if got.Model != "claude-x" || got.System != "sys" || got.MaxTokens != 4096 || len(got.Messages) != 1 || got.Messages[0].Role != "user" || got.Temperature != nil {
		t.Fatalf("request %+v", got)
	}
}

func TestAnthropicRefusalAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Case") {
		case "refusal":
			w.Write([]byte(`{"content":[],"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber","explanation":"nope"}}`))
		case "overloaded":
			w.WriteHeader(529)
			w.Write([]byte(`{"error":{"type":"overloaded_error","message":"busy"}}`))
		default:
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"type":"authentication_error","message":"bad key"}}`))
		}
	}))
	defer srv.Close()
	for _, cse := range []struct {
		name      string
		retryable bool
	}{{"refusal", false}, {"overloaded", true}, {"auth", false}} {
		c := NewAnthropic(srv.URL, "", "m", &http.Client{Transport: headerRT{cse.name}})
		_, err := c.Complete(context.Background(), Request{User: "x"})
		if err == nil {
			t.Fatalf("%s: expected error", cse.name)
		}
		if Retryable(err) != cse.retryable {
			t.Fatalf("%s: Retryable=%v err=%v", cse.name, Retryable(err), err)
		}
	}
}

type fakeClient struct {
	calls int32
	fn    func(n int32) (string, error)
}

func (f *fakeClient) Model() string { return "fake" }
func (f *fakeClient) Complete(ctx context.Context, req Request) (string, error) {
	return f.fn(atomic.AddInt32(&f.calls, 1))
}

func TestRetry(t *testing.T) {
	// Succeeds on the third attempt.
	f := &fakeClient{fn: func(n int32) (string, error) {
		if n < 3 {
			return "", &HTTPError{Status: 503}
		}
		return "ok", nil
	}}
	out, err := Retry(f, 3, time.Millisecond).Complete(context.Background(), Request{})
	if err != nil || out != "ok" || f.calls != 3 {
		t.Fatalf("out=%q err=%v calls=%d", out, err, f.calls)
	}

	// Non-retryable error returns immediately.
	f = &fakeClient{fn: func(int32) (string, error) { return "", &HTTPError{Status: 400} }}
	if _, err := Retry(f, 3, time.Millisecond).Complete(context.Background(), Request{}); err == nil || f.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}

	// Gives up after retries are exhausted.
	f = &fakeClient{fn: func(int32) (string, error) { return "", errors.New("net down") }}
	if _, err := Retry(f, 2, time.Millisecond).Complete(context.Background(), Request{}); err == nil || f.calls != 3 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}

	// Cancelled context stops the backoff wait.
	f = &fakeClient{fn: func(int32) (string, error) { return "", &HTTPError{Status: 429} }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Retry(f, 2, time.Millisecond).Complete(ctx, Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestNew(t *testing.T) {
	if _, err := New(Config{Provider: "openai"}); err == nil {
		t.Fatal("model required")
	}
	if _, err := New(Config{Provider: "nope", Model: "m"}); err == nil {
		t.Fatal("unknown provider")
	}
	c, err := New(Config{Provider: "anthropic", Model: "m"})
	if err != nil || c.Model() != "m" {
		t.Fatalf("%v %v", c, err)
	}
	if _, err := New(Config{Provider: "ollama", Model: "m", BaseURL: "http://localhost:11434/v1"}); err != nil {
		t.Fatal(err)
	}
}
