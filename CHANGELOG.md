# Changelog

## [v2.0.0]

A complete rewrite of BetterDecompiler.

### Highlights

- **Use your own API key** with Anthropic, OpenAI, Google Gemini, OpenRouter, Groq, DeepSeek, Mistral or xAI,
  any OpenAI-, Anthropic- or Gemini-compatible endpoint (Azure, proxies, self-hosted), or free local models via
  Ollama and LM Studio. The unofficial g4f proxy is gone.
- **Offline mode**: no key needed. Names are inferred from Roblox API usage (services, `WaitForChild` names,
  `Instance.new`, event callbacks, loops, constructors).
- **Rename mode (new default)**: the AI only proposes names and comments as compact JSON, and a scope-aware Luau
  parser applies them. Every rename is verified so the code's behavior cannot change. It is faster and much cheaper
  than having the AI re-type the whole script.
- **Rewrite mode** is still available for full AI rewrites. Its output is checked for syntax errors.
- **One self-contained program** for Windows, macOS (Intel and Apple Silicon) and Linux (x64, ARM64, ARMv7). No
  Python or pip.
- **Built-in web UI**, a **CLI** for files, folders and stdin, and a **JSON HTTP API**.
- **New executor client** (`betterdecompiler.lua`) that hooks the global `decompile`, so Dex and other tools get
  cleaned output automatically. It supports `request`, `http_request`, `syn.request`, `http.request` and
  `fluxus.request`, keeps the v1 `decompile(script, enabled, url)` signature, and always falls back to raw output on
  errors.

### Speed and reliability

- Results are cached in memory and on disk; identical concurrent requests share one AI call.
- Large scripts are split at top-level statements and processed in parallel.
- Retries with exponential backoff (honoring `Retry-After`), timeouts, and a configurable provider fallback chain
  that ends with the offline renamer.
- Connections to providers are pooled and reused.

### Security

- The server listens on `127.0.0.1` by default, rejects cross-site browser requests and DNS-rebinding attempts,
  and supports an optional access token.
- API keys are stored in a config file readable only by you, or referenced from environment variables.

### Compatibility

- The v1 `POST /fix_script` endpoint is still supported, so `dex_betterdecompiler.lua` and v1 clients work with the
  v2 server unchanged. The default port is still 5000.
- `flask_ai_server.py` and `main.lua` were removed; `client/betterdecompiler.lua` replaces `main.lua`.
