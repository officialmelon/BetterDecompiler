// Package config loads BetterDecompiler settings from a JSON file and the
// environment, and builds the provider clients.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/officialmelon/betterdecompiler/internal/engine"
	"github.com/officialmelon/betterdecompiler/internal/llm"
)

// Provider configures one AI provider. For built-in presets every field is
// optional; custom providers need at least type and base_url.
type Provider struct {
	Type           string            `json:"type,omitempty"`
	BaseURL        string            `json:"base_url,omitempty"`
	APIKey         string            `json:"api_key,omitempty"` // literal, "env:NAME" or "${NAME}"
	Model          string            `json:"model,omitempty"`
	MaxTokens      int               `json:"max_tokens,omitempty"`
	Temperature    *float64          `json:"temperature,omitempty"`
	Effort         string            `json:"effort,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	AuthHeader     string            `json:"auth_header,omitempty"`
	JSONMode       *bool             `json:"json_mode,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	MaxRetries     int               `json:"max_retries,omitempty"`
	NoFallbacks    bool              `json:"no_fallbacks,omitempty"`
}

// Cache configures result caching.
type Cache struct {
	Enabled    *bool  `json:"enabled,omitempty"`
	Dir        string `json:"dir,omitempty"`
	MaxEntries int    `json:"max_entries,omitempty"`
}

// Config is the config file.
type Config struct {
	Provider          string              `json:"provider,omitempty"`
	Fallback          []string            `json:"fallback,omitempty"`
	Mode              string              `json:"mode,omitempty"`
	Comments          string              `json:"comments,omitempty"`
	Header            *bool               `json:"header,omitempty"`
	Heuristics        *bool               `json:"heuristics,omitempty"`
	Listen            string              `json:"listen,omitempty"`
	Token             string              `json:"token,omitempty"`
	Concurrency       int                 `json:"concurrency,omitempty"`
	ChunkChars        int                 `json:"chunk_chars,omitempty"`
	RewriteChunkChars int                 `json:"rewrite_chunk_chars,omitempty"`
	Cache             Cache               `json:"cache,omitempty"`
	Providers         map[string]Provider `json:"providers,omitempty"`
}

// DefaultListen is the default server address (same port as v1).
const DefaultListen = "127.0.0.1:5000"

// DefaultPath returns the config file location: $BD_CONFIG, or
// <user config dir>/betterdecompiler/config.json.
func DefaultPath() string {
	if p := os.Getenv("BD_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "betterdecompiler.json"
	}
	return filepath.Join(dir, "betterdecompiler", "config.json")
}

// Load reads a config file. A missing file yields an empty config.
func Load(path string) (*Config, error) {
	c := &Config{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Save writes the config file with owner-only permissions (it may hold keys).
func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// ApplyEnv overlays BD_* environment variables.
func (c *Config) ApplyEnv() {
	if v := os.Getenv("BD_PROVIDER"); v != "" {
		c.Provider = v
	}
	if v := os.Getenv("BD_MODE"); v != "" {
		c.Mode = v
	}
	if v := os.Getenv("BD_TOKEN"); v != "" {
		c.Token = v
	}
	if v := os.Getenv("BD_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("BD_MODEL"); v != "" && c.Provider != "" {
		p := c.Providers[c.Provider]
		p.Model = v
		c.SetProvider(c.Provider, p)
	}
}

// SetProvider stores provider settings.
func (c *Config) SetProvider(name string, p Provider) {
	if c.Providers == nil {
		c.Providers = map[string]Provider{}
	}
	c.Providers[name] = p
}

// ResolveKey expands "env:NAME", "$NAME" and "${NAME}" references.
func ResolveKey(s string) string {
	switch {
	case strings.HasPrefix(s, "env:"):
		return os.Getenv(strings.TrimPrefix(s, "env:"))
	case strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}"):
		return os.Getenv(s[2 : len(s)-1])
	case strings.HasPrefix(s, "$") && len(s) > 1:
		return os.Getenv(s[1:])
	}
	return s
}

// ProviderInfo describes a provider for listings.
type ProviderInfo struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Model       string `json:"model"`
	BaseURL     string `json:"base_url"`
	Ready       bool   `json:"ready"`  // has a key (or needs none)
	Local       bool   `json:"local"`  // runs on this machine
	Custom      bool   `json:"custom"` // defined in the config file
	Description string `json:"description,omitempty"`
	KeyEnv      string `json:"key_env,omitempty"`
}

// providerConfig merges a preset (if any) with user settings.
func (c *Config) providerConfig(name string) (llm.Config, ProviderInfo, error) {
	user, hasUser := c.Providers[name]
	preset, isPreset := llm.FindPreset(name)
	if !isPreset && !hasUser {
		return llm.Config{}, ProviderInfo{}, fmt.Errorf("unknown provider %q", name)
	}
	var cfg llm.Config
	info := ProviderInfo{Name: name, Custom: !isPreset}
	needsKey := true
	if isPreset {
		cfg = preset.Config()
		needsKey = preset.NeedsKey
		info.Description = preset.Description
		if len(preset.KeyEnv) > 0 {
			info.KeyEnv = preset.KeyEnv[0]
		}
	} else {
		cfg = llm.Config{Name: name, Type: llm.Type(user.Type), Headers: map[string]string{}}
		if user.Type == "" {
			cfg.Type = llm.TypeOpenAI
		}
		cfg.JSONMode = cfg.Type == llm.TypeGemini
		if user.BaseURL == "" {
			return cfg, info, fmt.Errorf("provider %q: base_url is required for custom providers", name)
		}
		needsKey = false
	}
	if hasUser {
		if user.Type != "" {
			cfg.Type = llm.Type(user.Type)
		}
		if user.BaseURL != "" {
			cfg.BaseURL = user.BaseURL
		}
		if user.APIKey != "" {
			cfg.APIKey = ResolveKey(user.APIKey)
		}
		if user.Model != "" {
			cfg.Model = user.Model
		}
		if user.MaxTokens != 0 {
			cfg.MaxTokens = user.MaxTokens
		}
		if user.Temperature != nil {
			cfg.Temperature = user.Temperature
		}
		if user.Effort != "" {
			cfg.Effort = user.Effort
		}
		for k, v := range user.Headers {
			cfg.Headers[k] = ResolveKey(v)
		}
		if user.AuthHeader != "" {
			cfg.AuthHeader = user.AuthHeader
		}
		if user.JSONMode != nil {
			cfg.JSONMode = *user.JSONMode
		}
		if user.TimeoutSeconds > 0 {
			cfg.Timeout = time.Duration(user.TimeoutSeconds) * time.Second
		}
		if user.MaxRetries != 0 {
			cfg.MaxRetries = user.MaxRetries
		}
		cfg.NoFallbacks = user.NoFallbacks
	}
	info.Type, info.Model, info.BaseURL = string(cfg.Type), cfg.Model, cfg.BaseURL
	info.Local = strings.Contains(cfg.BaseURL, "://localhost") || strings.Contains(cfg.BaseURL, "://127.0.0.1")
	info.Ready = !needsKey || cfg.APIKey != ""
	return cfg, info, nil
}

// List describes every known provider (presets first, then custom ones).
func (c *Config) List() []ProviderInfo {
	var out []ProviderInfo
	for _, p := range llm.Presets {
		if _, info, err := c.providerConfig(p.Name); err == nil {
			out = append(out, info)
		}
	}
	var custom []string
	for name := range c.Providers {
		if _, ok := llm.FindPreset(name); !ok {
			custom = append(custom, name)
		}
	}
	sort.Strings(custom)
	for _, name := range custom {
		_, info, err := c.providerConfig(name)
		if err != nil {
			info = ProviderInfo{Name: name, Custom: true, Description: err.Error()}
		}
		out = append(out, info)
	}
	return out
}

// Build creates the provider clients and the default provider chain.
//
// Without an explicit provider, the first built-in cloud provider that has an
// API key (from the config file or the environment) is used; with none, the
// offline renamer is used. Unless "fallback" says otherwise, the chain ends
// with the offline renamer so a failing AI never leaves you empty-handed.
func (c *Config) Build() (map[string]llm.Client, []string, error) {
	clients := map[string]llm.Client{}
	ready := map[string]bool{}
	for _, info := range c.List() {
		if !info.Ready {
			continue
		}
		cfg, _, err := c.providerConfig(info.Name)
		if err != nil {
			if info.Name == c.Provider {
				return nil, nil, err
			}
			continue
		}
		cl, err := llm.New(cfg, nil)
		if err != nil {
			return nil, nil, err
		}
		clients[info.Name], ready[info.Name] = cl, true
	}
	primary := c.Provider
	if primary == "" {
		for _, p := range llm.Presets {
			if p.NeedsKey && ready[p.Name] {
				primary = p.Name
				break
			}
		}
	}
	if primary == "" || primary == llm.Offline {
		return clients, []string{llm.Offline}, nil
	}
	if !ready[primary] {
		if _, _, err := c.providerConfig(primary); err != nil {
			return nil, nil, err
		}
		p, _ := llm.FindPreset(primary)
		env := ""
		if len(p.KeyEnv) > 0 {
			env = " or set " + p.KeyEnv[0]
		}
		return nil, nil, fmt.Errorf("provider %q has no API key: run `betterdecompiler setup`%s", primary, env)
	}
	chain := []string{primary}
	fallback := c.Fallback
	if fallback == nil {
		fallback = []string{llm.Offline}
	}
	for _, name := range fallback {
		if name == primary || contains(chain, name) {
			continue
		}
		if name != llm.Offline && !ready[name] {
			return nil, nil, fmt.Errorf("fallback provider %q is unknown or has no API key", name)
		}
		chain = append(chain, name)
	}
	return clients, chain, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func boolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

// EngineSettings converts the config to engine settings.
func (c *Config) EngineSettings(version string) engine.Settings {
	return engine.Settings{
		Mode: c.Mode, Comments: c.Comments, Header: boolOr(c.Header, true), Heuristics: boolOr(c.Heuristics, true),
		ChunkChars: c.ChunkChars, RewriteChunkChars: c.RewriteChunkChars, Concurrency: c.Concurrency, Version: version,
	}
}

// NewCache builds the result cache described by the config (nil if disabled).
func (c *Config) NewCache() *engine.Cache {
	if !boolOr(c.Cache.Enabled, true) {
		return nil
	}
	dir := c.Cache.Dir
	if dir == "" {
		if base, err := os.UserCacheDir(); err == nil {
			dir = filepath.Join(base, "betterdecompiler")
		}
	}
	return engine.NewCache(c.Cache.MaxEntries, dir)
}

// ListenAddr returns the server address.
func (c *Config) ListenAddr() string {
	if c.Listen != "" {
		return c.Listen
	}
	return DefaultListen
}

// Redacted returns a copy safe to print: keys and tokens are masked.
func (c *Config) Redacted() *Config {
	cp := *c
	cp.Token = mask(c.Token)
	cp.Providers = map[string]Provider{}
	for k, p := range c.Providers {
		if !strings.HasPrefix(p.APIKey, "env:") && !strings.HasPrefix(p.APIKey, "$") {
			p.APIKey = mask(p.APIKey)
		}
		cp.Providers[k] = p
	}
	return &cp
}

func mask(s string) string {
	switch {
	case s == "":
		return ""
	case len(s) <= 10:
		return "****"
	}
	return s[:4] + "…" + s[len(s)-4:]
}
