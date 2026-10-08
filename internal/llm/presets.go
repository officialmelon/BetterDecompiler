package llm

import "os"

// Preset is a built-in provider definition. Users only need to supply a key.
type Preset struct {
	Name           string
	Type           Type
	BaseURL        string
	Model          string
	KeyEnv         []string // environment variables checked for an API key
	NeedsKey       bool
	JSONMode       bool
	MaxTokens      int
	MaxTokensField string
	Headers        map[string]string
	Description    string
	KeyURL         string // where to get a key
}

// Offline is the name of the built-in, no-AI renamer.
const Offline = "offline"

// Presets lists every built-in provider, in auto-detection order.
var Presets = []Preset{
	{Name: "anthropic", Type: TypeAnthropic, BaseURL: "https://api.anthropic.com", Model: "claude-opus-5-5",
		KeyEnv: []string{"ANTHROPIC_API_KEY"}, NeedsKey: true, MaxTokens: 32000,
		Description: "Anthropic Claude", KeyURL: "https://console.anthropic.com/settings/keys"},
	{Name: "openai", Type: TypeOpenAI, BaseURL: "https://api.openai.com/v1", Model: "gpt-5.6-terra",
		KeyEnv: []string{"OPENAI_API_KEY"}, NeedsKey: true, JSONMode: true, MaxTokensField: "max_completion_tokens",
		Description: "OpenAI GPT", KeyURL: "https://platform.openai.com/api-keys"},
	{Name: "gemini", Type: TypeGemini, BaseURL: "https://generativelanguage.googleapis.com/v1beta", Model: "gemini-3.7-flash",
		KeyEnv: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, NeedsKey: true, JSONMode: true,
		Description: "Google Gemini", KeyURL: "https://aistudio.google.com/apikey"},
	{Name: "openrouter", Type: TypeOpenAI, BaseURL: "https://openrouter.ai/api/v1", Model: "openrouter/auto",
		KeyEnv: []string{"OPENROUTER_API_KEY"}, NeedsKey: true,
		Headers:     map[string]string{"HTTP-Referer": "https://github.com/officialmelon/BetterDecompiler", "X-Title": "BetterDecompiler"},
		Description: "OpenRouter (hundreds of models, one key)", KeyURL: "https://openrouter.ai/keys"},
	{Name: "groq", Type: TypeOpenAI, BaseURL: "https://api.groq.com/openai/v1", Model: "openai/gpt-oss-120b",
		KeyEnv: []string{"GROQ_API_KEY"}, NeedsKey: true, JSONMode: true,
		Description: "Groq (very fast, free tier)", KeyURL: "https://console.groq.com/keys"},
	{Name: "deepseek", Type: TypeOpenAI, BaseURL: "https://api.deepseek.com", Model: "deepseek-chat",
		KeyEnv: []string{"DEEPSEEK_API_KEY"}, NeedsKey: true, JSONMode: true, MaxTokens: 8192,
		Description: "DeepSeek", KeyURL: "https://platform.deepseek.com/api_keys"},
	{Name: "mistral", Type: TypeOpenAI, BaseURL: "https://api.mistral.ai/v1", Model: "mistral-large-latest",
		KeyEnv: []string{"MISTRAL_API_KEY"}, NeedsKey: true, JSONMode: true,
		Description: "Mistral AI", KeyURL: "https://console.mistral.ai/api-keys"},
	{Name: "xai", Type: TypeOpenAI, BaseURL: "https://api.x.ai/v1", Model: "grok-code-fast-1",
		KeyEnv: []string{"XAI_API_KEY"}, NeedsKey: true,
		Description: "xAI Grok", KeyURL: "https://console.x.ai"},
	{Name: "ollama", Type: TypeOpenAI, BaseURL: "http://localhost:11434/v1", Model: "qwen2.5-coder:7b",
		Description: "Ollama (local models, free, private)", KeyURL: "https://ollama.com/download"},
	{Name: "lmstudio", Type: TypeOpenAI, BaseURL: "http://localhost:1234/v1", Model: "auto",
		Description: "LM Studio (local models, free, private)", KeyURL: "https://lmstudio.ai"},
}

// FindPreset returns the preset with the given name.
func FindPreset(name string) (Preset, bool) {
	for _, p := range Presets {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}

// EnvKey returns the first API key found in the preset's environment variables.
func (p Preset) EnvKey() string {
	for _, k := range p.KeyEnv {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// Config converts the preset to a client configuration.
func (p Preset) Config() Config {
	h := map[string]string{}
	for k, v := range p.Headers {
		h[k] = v
	}
	return Config{
		Name: p.Name, Type: p.Type, BaseURL: p.BaseURL, Model: p.Model, APIKey: p.EnvKey(),
		JSONMode: p.JSONMode, MaxTokens: p.MaxTokens, MaxTokensField: p.MaxTokensField, Headers: h,
	}
}
