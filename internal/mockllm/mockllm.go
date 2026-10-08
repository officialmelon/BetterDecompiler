// Package mockllm is a fake AI provider that speaks the OpenAI, Anthropic and
// Gemini HTTP APIs. It lets the whole pipeline be tested without API keys.
package mockllm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

// Request is a recorded incoming request.
type Request struct {
	Method string
	Path   string
	Header http.Header
	Body   map[string]any
}

// Server is an http.Handler emulating AI providers.
type Server struct {
	Key string // required API key; empty disables auth checks

	mu       sync.Mutex
	requests []Request
	failNext []int
	// Responder, when set, produces the reply text for a prompt.
	Responder func(system, prompt string) string
}

// New returns a mock server requiring key.
func New(key string) *Server { return &Server{Key: key} }

// FailNext makes the next requests fail with the given HTTP statuses.
func (s *Server) FailNext(statuses ...int) {
	s.mu.Lock()
	s.failNext = append(s.failNext, statuses...)
	s.mu.Unlock()
}

// Requests returns the requests received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Count returns the number of generation requests (not model listings).
func (s *Server) Count() int {
	n := 0
	for _, r := range s.Requests() {
		if r.Method == http.MethodPost {
			n++
		}
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if r.Body != nil {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
	}
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
	var fail int
	if len(s.failNext) > 0 {
		fail, s.failNext = s.failNext[0], s.failNext[1:]
	}
	s.mu.Unlock()

	path := r.URL.Path
	if fail != 0 {
		w.Header().Set("Retry-After", "0")
		writeJSON(w, fail, map[string]any{"error": map[string]any{"message": fmt.Sprintf("injected failure %d", fail)}})
		return
	}
	if s.Key != "" {
		got := r.Header.Get("x-api-key") + r.Header.Get("x-goog-api-key") + r.Header.Get("api-key") +
			strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got != s.Key {
			writeJSON(w, 401, map[string]any{"error": map[string]any{"message": "invalid api key"}})
			return
		}
	}
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/models") && strings.Contains(path, "v1beta"):
		writeJSON(w, 200, map[string]any{"models": []map[string]any{
			{"name": "models/mock-gemini", "supportedGenerationMethods": []string{"generateContent"}},
			{"name": "models/mock-embed", "supportedGenerationMethods": []string{"embedContent"}},
		}})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/models"):
		writeJSON(w, 200, map[string]any{"data": []map[string]any{{"id": "mock-1"}, {"id": "mock-2"}}})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/chat/completions"):
		s.openAI(w, body)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/v1/messages"):
		if r.Header.Get("anthropic-version") == "" {
			writeJSON(w, 400, map[string]any{"type": "error", "error": map[string]any{"message": "missing anthropic-version"}})
			return
		}
		s.anthropic(w, body)
	case r.Method == http.MethodPost && strings.HasSuffix(path, ":generateContent"):
		model := path[strings.LastIndex(path, "/")+1 : strings.LastIndex(path, ":")]
		s.gemini(w, model, body)
	default:
		writeJSON(w, 404, map[string]any{"error": map[string]any{"message": "not found: " + path}})
	}
}

func str(v any) string { s, _ := v.(string); return s }

func (s *Server) reply(system, prompt string) string {
	if s.Responder != nil {
		return s.Responder(system, prompt)
	}
	return DefaultReply(system, prompt)
}

func (s *Server) openAI(w http.ResponseWriter, body map[string]any) {
	model := str(body["model"])
	var system, prompt string
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		switch str(mm["role"]) {
		case "system", "developer":
			system = str(mm["content"])
		case "user":
			prompt = str(mm["content"])
		}
	}
	text, finish := s.reply(system, prompt), "stop"
	msg := map[string]any{"role": "assistant", "content": text}
	switch model {
	case "mock-refuse":
		msg = map[string]any{"role": "assistant", "content": nil, "refusal": "I can't help with that."}
	case "mock-truncate":
		finish = "length"
	case "mock-garbage":
		msg["content"] = "I am not JSON at all"
	}
	writeJSON(w, 200, map[string]any{
		"id": "chatcmpl-mock", "model": model,
		"choices": []map[string]any{{"index": 0, "message": msg, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": len(prompt) / 4, "completion_tokens": len(text) / 4},
	})
}

func (s *Server) anthropic(w http.ResponseWriter, body map[string]any) {
	model := str(body["model"])
	if _, ok := body["max_tokens"]; !ok {
		writeJSON(w, 400, map[string]any{"type": "error", "error": map[string]any{"message": "max_tokens: Field required"}})
		return
	}
	system := str(body["system"])
	var prompt string
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		prompt = str(mm["content"])
	}
	text := s.reply(system, prompt)
	stop := "end_turn"
	content := []map[string]any{{"type": "text", "text": text}}
	resp := map[string]any{"id": "msg_mock", "type": "message", "role": "assistant", "model": model}
	switch model {
	case "mock-refuse":
		stop, content = "refusal", []map[string]any{}
		resp["stop_details"] = map[string]any{"type": "refusal", "category": "cyber", "explanation": "mock refusal"}
	case "mock-truncate":
		stop = "max_tokens"
	case "mock-fallback":
		// The first model declined mid-stream; the fallback model answered.
		content = []map[string]any{
			{"type": "text", "text": "partial output that must be discarded"},
			{"type": "fallback", "from": map[string]any{"model": model}, "to": map[string]any{"model": "mock-backup"}},
			{"type": "text", "text": text},
		}
		resp["model"] = "mock-backup"
	case "mock-garbage":
		content = []map[string]any{{"type": "text", "text": "I am not JSON at all"}}
	}
	resp["content"], resp["stop_reason"] = content, stop
	resp["usage"] = map[string]any{"input_tokens": len(prompt) / 4, "output_tokens": len(text) / 4}
	writeJSON(w, 200, resp)
}

func (s *Server) gemini(w http.ResponseWriter, model string, body map[string]any) {
	var system, prompt string
	if si, ok := body["systemInstruction"].(map[string]any); ok {
		if parts, ok := si["parts"].([]any); ok && len(parts) > 0 {
			system = str(parts[0].(map[string]any)["text"])
		}
	}
	if cs, ok := body["contents"].([]any); ok && len(cs) > 0 {
		if parts, ok := cs[0].(map[string]any)["parts"].([]any); ok && len(parts) > 0 {
			prompt = str(parts[0].(map[string]any)["text"])
		}
	}
	text := s.reply(system, prompt)
	finish := "STOP"
	switch model {
	case "mock-refuse":
		finish = "SAFETY"
	case "mock-truncate":
		finish = "MAX_TOKENS"
	case "mock-garbage":
		text = "I am not JSON at all"
	}
	writeJSON(w, 200, map[string]any{
		"candidates": []map[string]any{{
			"content": map[string]any{"role": "model", "parts": []map[string]any{
				{"text": "thinking about it...", "thought": true},
				{"text": text},
			}},
			"finishReason": finish,
		}},
		"usageMetadata": map[string]any{"promptTokenCount": len(prompt) / 4, "candidatesTokenCount": len(text) / 4},
		"modelVersion":  model,
	})
}

var (
	candidatesRe = regexp.MustCompile(`(?m)^CANDIDATES: (.*)$`)
	lineNumRe    = regexp.MustCompile(`(?m)^\s*(\d+)\| `)
)

// DefaultReply imitates a model: for rename prompts it answers with a JSON
// rename map (candidate x -> mockX) inside a markdown fence; for rewrite
// prompts it returns the code in a fence with a leading comment.
func DefaultReply(system, prompt string) string {
	if m := candidatesRe.FindStringSubmatch(prompt); m != nil {
		renames := map[string]string{}
		for _, c := range strings.Split(m[1], ",") {
			c = strings.TrimSpace(c)
			if c == "" || c == "(none)" {
				continue
			}
			r := []rune(c)
			r[0] = unicode.ToUpper(r[0])
			renames[c] = "mock" + string(r)
		}
		comments := []map[string]any{}
		if ln := lineNumRe.FindStringSubmatch(prompt); ln != nil {
			var n int
			fmt.Sscan(ln[1], &n)
			comments = append(comments, map[string]any{"line": n, "text": "mock comment"})
		}
		out, _ := json.Marshal(map[string]any{"summary": "Mock summary of the script.", "renames": renames, "comments": comments})
		return "```json\n" + string(out) + "\n```"
	}
	code := prompt
	if i := strings.Index(prompt, "SOURCE:\n"); i >= 0 {
		code = prompt[i+len("SOURCE:\n"):]
	}
	return "Here is the cleaned script:\n```lua\n-- mock rewrite\n" + strings.TrimRight(code, "\n") + "\n```\n"
}
