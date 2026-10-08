// Package llm talks to AI providers over their native HTTP APIs using only
// the standard library: OpenAI-compatible chat completions (OpenAI,
// OpenRouter, Groq, DeepSeek, Mistral, xAI, Ollama, LM Studio, Azure, ...),
// the Anthropic Messages API and the Google Gemini API.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Type identifies a wire protocol.
type Type string

const (
	TypeOpenAI    Type = "openai"
	TypeAnthropic Type = "anthropic"
	TypeGemini    Type = "gemini"
)

// Config configures one provider.
type Config struct {
	Name        string
	Type        Type
	BaseURL     string
	APIKey      string
	Model       string
	MaxTokens   int      // 0 = provider default
	Temperature *float64 // nil = provider default (many new models reject it)
	Effort      string   // reasoning effort; "none" disables the default
	Headers     map[string]string
	AuthHeader  string // send the key in this header instead of the default
	JSONMode    bool   // request JSON output natively when asked
	Timeout     time.Duration
	MaxRetries  int
	// MaxTokensField overrides the request field used for MaxTokens on
	// OpenAI-compatible APIs ("max_tokens" or "max_completion_tokens").
	MaxTokensField string
	// NoFallbacks disables Anthropic server-side refusal fallbacks.
	NoFallbacks bool
}

// Request is a single-turn completion request.
type Request struct {
	System string
	Prompt string
	Model  string // overrides Config.Model when set
	JSON   bool   // the caller expects a JSON object back
}

// Usage reports token consumption.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Add accumulates u2 into u.
func (u *Usage) Add(u2 Usage) {
	u.InputTokens += u2.InputTokens
	u.OutputTokens += u2.OutputTokens
}

// Response is a completion result.
type Response struct {
	Text      string
	Model     string
	Usage     Usage
	Truncated bool // output hit the token limit
}

// Client is an AI provider.
type Client interface {
	Name() string
	Model() string
	Complete(ctx context.Context, req Request) (*Response, error)
	ListModels(ctx context.Context) ([]string, error)
}

// Error is an API failure.
type Error struct {
	Provider   string
	Status     int
	Message    string
	retryAfter time.Duration
}

func (e *Error) Error() string {
	hint := ""
	switch e.Status {
	case 401, 403:
		hint = " (check your API key)"
	case 404:
		hint = " (check the model name; `betterdecompiler models -p " + e.Provider + "` lists available models)"
	case 429:
		hint = " (rate limited or out of credits)"
	}
	if e.Status == 0 {
		return fmt.Sprintf("%s: %s", e.Provider, e.Message)
	}
	return fmt.Sprintf("%s: HTTP %d: %s%s", e.Provider, e.Status, e.Message, hint)
}

// Retryable reports whether the request may succeed if repeated.
func (e *Error) Retryable() bool {
	switch e.Status {
	case 408, 409, 425, 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}

// New creates a client for cfg.
func New(cfg Config, hc *http.Client) (Client, error) {
	if hc == nil {
		hc = DefaultHTTPClient
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Minute
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	} else if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 2
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("%s: base_url is not set", cfg.Name)
	}
	b := base{cfg: cfg, hc: hc}
	switch cfg.Type {
	case TypeOpenAI, "":
		return &openAI{b}, nil
	case TypeAnthropic:
		return &anthropic{b}, nil
	case TypeGemini:
		return &gemini{b}, nil
	}
	return nil, fmt.Errorf("%s: unknown provider type %q (want openai, anthropic or gemini)", cfg.Name, cfg.Type)
}

// DefaultHTTPClient is shared by all providers so connections are pooled and
// reused (HTTP/2 where available). Proxies from the environment are honored.
var DefaultHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
	},
}

type base struct {
	cfg Config
	hc  *http.Client
}

func (b *base) Name() string  { return b.cfg.Name }
func (b *base) Model() string { return b.cfg.Model }

func (b *base) model(req Request) string {
	if req.Model != "" {
		return req.Model
	}
	return b.cfg.Model
}

// Backoff computes the delay before retry attempt n (0-based). Tests replace it.
var Backoff = func(n int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > 30*time.Second {
			retryAfter = 30 * time.Second
		}
		return retryAfter
	}
	d := time.Duration(750*(1<<n)) * time.Millisecond
	return d + time.Duration(rand.Int63n(int64(d/2)+1))
}

const maxResponseBytes = 64 << 20

// do sends a JSON request with retries and decodes a JSON response into out.
func (b *base) do(ctx context.Context, method, url string, headers map[string]string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, b.cfg.Timeout)
	defer cancel()
	var lastErr error
	for attempt := 0; attempt <= b.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			var ra time.Duration
			var ae *Error
			if errors.As(lastErr, &ae) {
				ra = ae.retryAfter
			}
			select {
			case <-time.After(Backoff(attempt-1, ra)):
			case <-ctx.Done():
				return fmt.Errorf("%s: %w (last error: %v)", b.cfg.Name, ctx.Err(), lastErr)
			}
		}
		err := b.once(ctx, method, url, headers, payload, out)
		if err == nil {
			return nil
		}
		lastErr = err
		var ae *Error
		if errors.As(err, &ae) {
			if !ae.Retryable() {
				return err
			}
		} else if ctx.Err() != nil {
			return fmt.Errorf("%s: %w", b.cfg.Name, ctx.Err())
		}
	}
	return lastErr
}

func (b *base) once(ctx context.Context, method, url string, headers map[string]string, payload []byte, out any) error {
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "BetterDecompiler/2 (+https://github.com/officialmelon/BetterDecompiler)")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for k, v := range b.cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := b.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		// Network errors are worth retrying.
		return &Error{Provider: b.cfg.Name, Status: 503, Message: "network error: " + err.Error()}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return &Error{Provider: b.cfg.Name, Status: 503, Message: "reading response: " + err.Error()}
	}
	if resp.StatusCode/100 != 2 {
		return &Error{Provider: b.cfg.Name, Status: resp.StatusCode, Message: errorMessage(data),
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &Error{Provider: b.cfg.Name, Message: fmt.Sprintf("invalid JSON response: %v: %s", err, snippet(data))}
	}
	return nil
}

func (b *base) authHeaders(defaultHeader, prefix string) map[string]string {
	h := map[string]string{}
	if b.cfg.APIKey == "" {
		return h
	}
	if b.cfg.AuthHeader != "" {
		h[b.cfg.AuthHeader] = b.cfg.APIKey
	} else {
		h[defaultHeader] = prefix + b.cfg.APIKey
	}
	return h
}

// errorMessage extracts a human-readable message from the error bodies used
// by OpenAI, Anthropic, Gemini and most compatible servers.
func errorMessage(data []byte) string {
	var v struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  string          `json:"detail"`
	}
	if json.Unmarshal(data, &v) == nil {
		var inner struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		}
		if len(v.Error) > 0 {
			if json.Unmarshal(v.Error, &inner) == nil && inner.Message != "" {
				return inner.Message
			}
			var s string
			if json.Unmarshal(v.Error, &s) == nil && s != "" {
				return s
			}
		}
		if v.Message != "" {
			return v.Message
		}
		if v.Detail != "" {
			return v.Detail
		}
	}
	return snippet(data)
}

func snippet(data []byte) string {
	s := strings.TrimSpace(string(data))
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	if s == "" {
		s = "(empty response)"
	}
	return s
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
