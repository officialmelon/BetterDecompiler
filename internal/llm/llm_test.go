package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/officialmelon/betterdecompiler/internal/mockllm"
)

func init() {
	Backoff = func(int, time.Duration) time.Duration { return time.Millisecond }
}

func newMock(t *testing.T) (*mockllm.Server, string) {
	t.Helper()
	m := mockllm.New("sk-test")
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return m, srv.URL
}

func clients(url string) []Config {
	return []Config{
		{Name: "openai", Type: TypeOpenAI, BaseURL: url + "/v1", APIKey: "sk-test", Model: "mock-1", JSONMode: true, MaxTokens: 100, MaxTokensField: "max_completion_tokens"},
		{Name: "anthropic", Type: TypeAnthropic, BaseURL: url, APIKey: "sk-test", Model: "mock-1"},
		{Name: "gemini", Type: TypeGemini, BaseURL: url + "/v1beta", APIKey: "sk-test", Model: "mock-gemini", JSONMode: true},
	}
}

func TestCompleteAllProtocols(t *testing.T) {
	m, url := newMock(t)
	for _, cfg := range clients(url) {
		t.Run(cfg.Name, func(t *testing.T) {
			c, err := New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := c.Complete(context.Background(), Request{System: "sys", Prompt: "CANDIDATES: v1\nSOURCE:\n1| local v1 = 1", JSON: true})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(resp.Text, `"v1":"mockV1"`) {
				t.Fatalf("unexpected text %q", resp.Text)
			}
			if strings.Contains(resp.Text, "thinking about it") {
				t.Fatal("gemini thought parts must be dropped")
			}
			if resp.Usage.InputTokens == 0 || resp.Truncated {
				t.Fatalf("usage/truncation wrong: %+v", resp)
			}
		})
	}
	reqs := m.Requests()
	if got := reqs[0].Body["max_completion_tokens"]; got != float64(100) {
		t.Errorf("openai max tokens field: %v", reqs[0].Body)
	}
	if rf, _ := reqs[0].Body["response_format"].(map[string]any); rf["type"] != "json_object" {
		t.Errorf("openai json mode not requested: %v", reqs[0].Body)
	}
	if reqs[1].Header.Get("x-api-key") != "sk-test" || reqs[1].Header.Get("anthropic-version") == "" {
		t.Errorf("anthropic headers: %v", reqs[1].Header)
	}
	if _, ok := reqs[1].Body["output_config"]; ok {
		t.Errorf("effort must not be sent to unknown models: %v", reqs[1].Body)
	}
	if gc, _ := reqs[2].Body["generationConfig"].(map[string]any); gc["responseMimeType"] != "application/json" {
		t.Errorf("gemini json mode: %v", reqs[2].Body)
	}
	if reqs[2].Header.Get("x-goog-api-key") != "sk-test" {
		t.Errorf("gemini key header: %v", reqs[2].Header)
	}
}

func TestAnthropicDefaultsForCurrentModels(t *testing.T) {
	m, url := newMock(t)
	c, _ := New(Config{Name: "anthropic", Type: TypeAnthropic, BaseURL: url, APIKey: "sk-test", Model: "claude-opus-5-5"}, nil)
	if _, err := c.Complete(context.Background(), Request{System: "s", Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	r := m.Requests()[0]
	if r.Body["fallbacks"] != "default" || r.Header.Get("anthropic-beta") != "server-side-fallback-2026-07-01" {
		t.Errorf("fallbacks not enabled: %v %v", r.Body, r.Header)
	}
	if oc, _ := r.Body["output_config"].(map[string]any); oc["effort"] != "medium" {
		t.Errorf("effort: %v", r.Body)
	}
	if r.Body["max_tokens"] != float64(32000) {
		t.Errorf("max_tokens: %v", r.Body["max_tokens"])
	}
	if _, ok := r.Body["temperature"]; ok {
		t.Error("temperature must not be sent by default")
	}
}

func TestAnthropicFallbackBlocks(t *testing.T) {
	_, url := newMock(t)
	c, _ := New(Config{Name: "anthropic", Type: TypeAnthropic, BaseURL: url, APIKey: "sk-test", Model: "mock-fallback"}, nil)
	resp, err := c.Complete(context.Background(), Request{System: "s", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resp.Text, "partial output") || resp.Model != "mock-backup" {
		t.Fatalf("fallback handling: %+v", resp)
	}
}

func TestRefusalsAndTruncation(t *testing.T) {
	_, url := newMock(t)
	for _, cfg := range clients(url) {
		cfg.Model = "mock-refuse"
		c, _ := New(cfg, nil)
		if _, err := c.Complete(context.Background(), Request{Prompt: "x"}); err == nil {
			t.Errorf("%s: refusal not reported", cfg.Name)
		}
		cfg.Model = "mock-truncate"
		c, _ = New(cfg, nil)
		resp, err := c.Complete(context.Background(), Request{Prompt: "x"})
		if err != nil || !resp.Truncated {
			t.Errorf("%s: truncation not reported: %v %+v", cfg.Name, err, resp)
		}
	}
}

func TestRetries(t *testing.T) {
	m, url := newMock(t)
	c, _ := New(clients(url)[0], nil)
	m.FailNext(429, 503)
	if _, err := c.Complete(context.Background(), Request{Prompt: "x"}); err != nil {
		t.Fatalf("should succeed after retries: %v", err)
	}
	if m.Count() != 3 {
		t.Fatalf("expected 3 attempts, got %d", m.Count())
	}
	m.FailNext(500, 500, 500)
	_, err := c.Complete(context.Background(), Request{Prompt: "x"})
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != 500 {
		t.Fatalf("expected final 500, got %v", err)
	}
	// Client errors are not retried.
	before := m.Count()
	m.FailNext(400)
	if _, err := c.Complete(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Fatal("expected error")
	}
	if m.Count() != before+1 {
		t.Fatal("400 must not be retried")
	}
}

func TestBadKey(t *testing.T) {
	_, url := newMock(t)
	cfg := clients(url)[1]
	cfg.APIKey = "wrong"
	c, _ := New(cfg, nil)
	_, err := c.Complete(context.Background(), Request{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "check your API key") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestListModelsAndAuto(t *testing.T) {
	m, url := newMock(t)
	for _, cfg := range clients(url) {
		c, _ := New(cfg, nil)
		models, err := c.ListModels(context.Background())
		if err != nil || len(models) == 0 {
			t.Fatalf("%s: %v %v", cfg.Name, models, err)
		}
		if cfg.Type == TypeGemini && (len(models) != 1 || models[0] != "mock-gemini") {
			t.Fatalf("gemini filtering: %v", models)
		}
	}
	cfg := clients(url)[0]
	cfg.Model = "auto"
	cfg.BaseURL = url + "/auto/v1"
	c, _ := New(cfg, nil)
	resp, err := c.Complete(context.Background(), Request{Prompt: "x"})
	if err != nil || resp.Model != "mock-1" {
		t.Fatalf("auto model: %v %+v", err, resp)
	}
	last := m.Requests()[len(m.Requests())-1]
	if last.Body["model"] != "mock-1" {
		t.Fatalf("auto model not used: %v", last.Body)
	}
}

func TestAuthHeaderOverride(t *testing.T) {
	m, url := newMock(t)
	cfg := clients(url)[0]
	cfg.AuthHeader = "api-key" // Azure style
	c, _ := New(cfg, nil)
	if _, err := c.Complete(context.Background(), Request{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	h := m.Requests()[0].Header
	if h.Get("api-key") != "sk-test" || h.Get("Authorization") != "" {
		t.Fatalf("headers: %v", h)
	}
}

func TestTimeout(t *testing.T) {
	done := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	defer slow.Close()
	defer close(done)
	c, _ := New(Config{Name: "slow", Type: TypeOpenAI, BaseURL: slow.URL, Model: "m", Timeout: 100 * time.Millisecond, MaxRetries: -1}, nil)
	start := time.Now()
	if _, err := c.Complete(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout not enforced")
	}
}

func TestPresets(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Presets {
		if seen[p.Name] {
			t.Fatalf("duplicate preset %s", p.Name)
		}
		seen[p.Name] = true
		if _, err := New(p.Config(), nil); err != nil {
			t.Fatalf("preset %s: %v", p.Name, err)
		}
	}
	t.Setenv("GOOGLE_API_KEY", "g-key")
	p, _ := FindPreset("gemini")
	if p.EnvKey() != "g-key" {
		t.Fatal("GOOGLE_API_KEY fallback not used")
	}
}

func TestErrorMessage(t *testing.T) {
	cases := map[string]string{
		`{"error":{"message":"bad model"}}`:                      "bad model",
		`{"type":"error","error":{"type":"x","message":"nope"}}`: "nope",
		`{"error":"plain"}`:                                      "plain",
		`{"detail":"det"}`:                                       "det",
		`<html>oops</html>`:                                      "<html>oops</html>",
	}
	for in, want := range cases {
		if got := errorMessage([]byte(in)); got != want {
			t.Errorf("errorMessage(%s) = %q, want %q", in, got, want)
		}
	}
}
