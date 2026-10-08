package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/officialmelon/betterdecompiler/internal/config"
	"github.com/officialmelon/betterdecompiler/internal/engine"
	"github.com/officialmelon/betterdecompiler/internal/llm"
	"github.com/officialmelon/betterdecompiler/internal/mockllm"
)

const script = `local v1 = game:GetService("Players")
local v2 = v1.LocalPlayer
print(v2)`

func newServer(t *testing.T, token string) (*httptest.Server, *mockllm.Server) {
	t.Helper()
	m := mockllm.New("k")
	up := httptest.NewServer(m)
	t.Cleanup(up.Close)
	c, err := llm.New(llm.Config{Name: "mock", Type: llm.TypeOpenAI, BaseURL: up.URL + "/v1", APIKey: "k", Model: "mock-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := engine.New(engine.Settings{Header: true, Heuristics: true, Version: "test"},
		map[string]llm.Client{"mock": c}, []string{"mock", llm.Offline}, engine.NewCache(16, ""))
	s := &Server{Engine: e, Token: token, Version: "test",
		Providers: []config.ProviderInfo{{Name: "mock", Model: "mock-1", Ready: true}, {Name: "unconfigured"}}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, m
}

func post(t *testing.T, url, body string, hdr map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	data, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(data, &out)
	return resp, out
}

func jsonBody(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestClean(t *testing.T) {
	srv, m := newServer(t, "")
	resp, out := post(t, srv.URL+"/v1/clean", jsonBody(map[string]any{"source": script, "comments": "none"}), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %v", resp.StatusCode, out)
	}
	src, _ := out["source"].(string)
	if !strings.Contains(src, "local mockV1 = game:GetService") || out["provider"] != "mock" || out["cached"] != false {
		t.Fatalf("unexpected: %v", out)
	}
	_, out2 := post(t, srv.URL+"/v1/clean", jsonBody(map[string]any{"source": script, "comments": "none"}), nil)
	if out2["cached"] != true || m.Count() != 1 {
		t.Fatalf("second call should be cached: %v", out2)
	}
	// Offline mode through the API.
	_, out3 := post(t, srv.URL+"/v1/clean", jsonBody(map[string]any{"source": script, "mode": "offline"}), nil)
	if s, _ := out3["source"].(string); !strings.Contains(s, "local Players = game:GetService") {
		t.Fatalf("offline: %v", out3)
	}
}

func TestLegacyEndpoint(t *testing.T) {
	srv, _ := newServer(t, "")
	resp, out := post(t, srv.URL+"/fix_script", jsonBody(map[string]any{"script": script}), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	fixed, _ := out["fixed_script"].(string)
	if !strings.HasPrefix(fixed, "-- Cleaned by BetterDecompiler") || !strings.Contains(fixed, "mockV1") {
		t.Fatalf("legacy output: %q", fixed)
	}
	// Empty script returns empty output, as in v1.
	_, out = post(t, srv.URL+"/fix_script", `{"script": ""}`, nil)
	if out["fixed_script"] != "" {
		t.Fatalf("empty: %v", out)
	}
}

func TestErrors(t *testing.T) {
	srv, _ := newServer(t, "")
	cases := []struct {
		body   string
		status int
	}{
		{`{"source":"x()","provider":"nope"}`, 400},
		{`{"source":"x()","mode":"weird"}`, 400},
		{`not json`, 400},
	}
	for _, c := range cases {
		if resp, out := post(t, srv.URL+"/v1/clean", c.body, nil); resp.StatusCode != c.status || out["error"] == nil {
			t.Errorf("%s: status %d %v", c.body, resp.StatusCode, out)
		}
	}
	resp, _ := http.Get(srv.URL + "/v1/clean")
	if resp.StatusCode != 405 {
		t.Errorf("GET /v1/clean: %d", resp.StatusCode)
	}
	resp, _ = http.Get(srv.URL + "/nope")
	if resp.StatusCode != 404 {
		t.Errorf("unknown path: %d", resp.StatusCode)
	}
}

func TestBrowserProtections(t *testing.T) {
	srv, m := newServer(t, "")
	// A cross-site page cannot call the API.
	resp, _ := post(t, srv.URL+"/v1/clean", jsonBody(map[string]any{"source": script}), map[string]string{"Origin": "https://evil.example"})
	if resp.StatusCode != 403 {
		t.Fatalf("cross-origin: %d", resp.StatusCode)
	}
	// Simple (non-JSON) cross-site form posts are rejected.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/fix_script", strings.NewReader(`{"script":"x"}`))
	req.Header.Set("Content-Type", "text/plain")
	r2, _ := http.DefaultClient.Do(req)
	if r2.StatusCode != 415 {
		t.Fatalf("text/plain: %d", r2.StatusCode)
	}
	// DNS rebinding: a foreign Host header is refused.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v1/health", nil)
	req.Host = "attacker.example:5000"
	r3, _ := http.DefaultClient.Do(req)
	if r3.StatusCode != 403 {
		t.Fatalf("foreign host: %d", r3.StatusCode)
	}
	// Same-origin requests from the web UI work.
	resp, _ = post(t, srv.URL+"/v1/clean", jsonBody(map[string]any{"source": script}), map[string]string{"Origin": srv.URL})
	if resp.StatusCode != 200 {
		t.Fatalf("same-origin: %d", resp.StatusCode)
	}
	if m.Count() != 1 {
		t.Fatalf("blocked requests must not reach the provider (calls=%d)", m.Count())
	}
}

func TestToken(t *testing.T) {
	srv, _ := newServer(t, "s3cret")
	if resp, _ := post(t, srv.URL+"/v1/clean", `{"source":"x()"}`, nil); resp.StatusCode != 401 {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	if resp, _ := post(t, srv.URL+"/v1/clean", `{"source":"x()"}`, map[string]string{"Authorization": "Bearer wrong"}); resp.StatusCode != 401 {
		t.Fatalf("bad token: %d", resp.StatusCode)
	}
	if resp, _ := post(t, srv.URL+"/v1/clean", `{"source":"x()"}`, map[string]string{"X-BD-Token": "s3cret"}); resp.StatusCode != 200 {
		t.Fatalf("good token: %d", resp.StatusCode)
	}
	resp, _ := http.Get(srv.URL + "/v1/health")
	var h map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&h)
	if resp.StatusCode != 200 || h["auth"] != true {
		t.Fatalf("health must stay public and report auth: %v", h)
	}
}

func TestProvidersAndUI(t *testing.T) {
	srv, _ := newServer(t, "")
	resp, _ := http.Get(srv.URL + "/v1/providers")
	var out struct {
		Chain     []string              `json:"chain"`
		Providers []config.ProviderInfo `json:"providers"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Providers) != 1 || out.Providers[0].Name != "mock" || len(out.Chain) != 2 {
		t.Fatalf("providers: %+v", out)
	}
	resp, _ = http.Get(srv.URL + "/")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<title>BetterDecompiler</title>") {
		t.Fatalf("ui: %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("missing CSP")
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for h, want := range map[string]bool{"localhost:5000": true, "127.0.0.1:5000": true, "[::1]:5000": true,
		"LOCALHOST": true, "127.0.0.2": true, "example.com": false, "192.168.1.5:5000": false} {
		if got := isLoopbackHost(h); got != want {
			t.Errorf("isLoopbackHost(%q) = %v", h, got)
		}
	}
}
