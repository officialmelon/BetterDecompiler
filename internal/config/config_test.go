package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/officialmelon/betterdecompiler/internal/llm"
)

// clearEnv unsets every provider key so tests don't depend on the machine.
func clearEnv(t *testing.T) {
	for _, p := range llm.Presets {
		for _, k := range p.KeyEnv {
			t.Setenv(k, "")
		}
	}
	for _, k := range []string{"BD_PROVIDER", "BD_MODE", "BD_TOKEN", "BD_LISTEN", "BD_MODEL"} {
		t.Setenv(k, "")
	}
}

func TestNoKeysMeansOffline(t *testing.T) {
	clearEnv(t)
	c := &Config{}
	clients, chain, err := c.Build()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(chain, []string{llm.Offline}) {
		t.Fatalf("chain %v", chain)
	}
	// Local providers need no key and are still available on request.
	if _, ok := clients["ollama"]; !ok {
		t.Fatal("ollama should be available")
	}
}

func TestAutoDetectFromEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("GROQ_API_KEY", "gsk")
	t.Setenv("GEMINI_API_KEY", "gem")
	_, chain, err := (&Config{}).Build()
	if err != nil {
		t.Fatal(err)
	}
	// gemini comes before groq in preset order.
	if !reflect.DeepEqual(chain, []string{"gemini", llm.Offline}) {
		t.Fatalf("chain %v", chain)
	}
}

func TestExplicitProviderAndFallback(t *testing.T) {
	clearEnv(t)
	t.Setenv("MY_KEY", "secret")
	c := &Config{
		Provider: "anthropic",
		Fallback: []string{"proxy"},
		Providers: map[string]Provider{
			"anthropic": {APIKey: "sk-ant-direct", Model: "claude-haiku-5-5"},
			"proxy":     {Type: "openai", BaseURL: "https://proxy.example/v1", APIKey: "env:MY_KEY", Model: "m"},
		},
	}
	clients, chain, err := c.Build()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(chain, []string{"anthropic", "proxy"}) {
		t.Fatalf("chain %v (explicit fallback list replaces the default)", chain)
	}
	if clients["anthropic"].Model() != "claude-haiku-5-5" {
		t.Fatal("model override ignored")
	}
	cfg, info, _ := c.providerConfig("proxy")
	if cfg.APIKey != "secret" || !info.Custom || !info.Ready {
		t.Fatalf("custom provider: %+v %+v", cfg, info)
	}
}

func TestMissingKeyErrors(t *testing.T) {
	clearEnv(t)
	_, _, err := (&Config{Provider: "openai"}).Build()
	if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("expected helpful error, got %v", err)
	}
	if _, _, err := (&Config{Provider: "nope"}).Build(); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if _, _, err := (&Config{Provider: "custom", Providers: map[string]Provider{"custom": {Type: "openai"}}}).Build(); err == nil {
		t.Fatal("custom provider without base_url accepted")
	}
}

func TestSaveLoadRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	c := &Config{Provider: "gemini", Token: "tok"}
	c.SetProvider("gemini", Provider{APIKey: "AIza-secret-value"})
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("config readable by others: %v", st.Mode())
	}
	got, err := Load(path)
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	missing, err := Load(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || missing.Provider != "" {
		t.Fatal("missing file should be empty config")
	}
	r := got.Redacted()
	if strings.Contains(r.Providers["gemini"].APIKey, "secret") || r.Token == "tok" {
		t.Fatalf("not redacted: %+v", r)
	}
	if got.Providers["gemini"].APIKey != "AIza-secret-value" {
		t.Fatal("Redacted mutated the original")
	}
}

func TestApplyEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("BD_PROVIDER", "openai")
	t.Setenv("BD_MODEL", "gpt-x")
	t.Setenv("BD_LISTEN", "0.0.0.0:6000")
	c := &Config{}
	c.ApplyEnv()
	if c.Provider != "openai" || c.Providers["openai"].Model != "gpt-x" || c.ListenAddr() != "0.0.0.0:6000" {
		t.Fatalf("%+v", c)
	}
}

func TestResolveKey(t *testing.T) {
	t.Setenv("K", "v")
	for in, want := range map[string]string{"env:K": "v", "${K}": "v", "$K": "v", "literal": "literal", "$": "$"} {
		if got := ResolveKey(in); got != want {
			t.Errorf("ResolveKey(%q) = %q", in, got)
		}
	}
}

func TestEngineSettingsDefaults(t *testing.T) {
	s := (&Config{}).EngineSettings("v")
	if !s.Header || !s.Heuristics || s.Version != "v" {
		t.Fatalf("%+v", s)
	}
	off := false
	if (&Config{Cache: Cache{Enabled: &off}}).NewCache() != nil {
		t.Fatal("disabled cache created")
	}
}
