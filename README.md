# BetterDecompiler

[![CI](https://github.com/officialmelon/BetterDecompiler/actions/workflows/ci.yml/badge.svg)](https://github.com/officialmelon/BetterDecompiler/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/officialmelon/BetterDecompiler)](https://github.com/officialmelon/BetterDecompiler/releases/latest)

**Turn decompiled Roblox Luau into code you can actually read.** BetterDecompiler takes your executor's
decompiler output, recovers meaningful variable names, and adds short comments. It works with **your own API key**
for any major AI provider, with **free local models**, or **fully offline**, and runs on **Windows, macOS and Linux**
as a single small program with nothing else to install.

```lua
-- before (raw decompiler output)                 -- after (example, rename mode with an AI provider)
local v1 = game:GetService("Players")             local Players = game:GetService("Players")
local v2 = v1.LocalPlayer                         local localPlayer = Players.LocalPlayer
v2.CharacterAdded:Connect(function(p1)            -- Disable the shop UI when the character dies.
    local v3 = p1:WaitForChild("Humanoid")        localPlayer.CharacterAdded:Connect(function(character)
    v3.Died:Connect(function()                        local humanoid = character:WaitForChild("Humanoid")
        u4("Shop", false)                             humanoid.Died:Connect(function()
    end)                                                  toggleShop("Shop", false)
end)                                                  end)
                                                  end)
```

## What's new in v2

v1 was a Python/Flask script that sent your code through a free reverse proxy (g4f). That approach broke often,
was slow, and sometimes changed what scripts did. v2 is a rewrite:

| | v1 | v2 |
|---|---|---|
| AI access | Unofficial free proxy (g4f), often broken | **Your own key**: Anthropic, OpenAI, Gemini, OpenRouter, Groq, DeepSeek, Mistral, xAI, any OpenAI-compatible API, plus local Ollama / LM Studio |
| Without an API key | Nothing | **Offline renamer** infers names from Roblox API usage, instantly and for free |
| Install | Python + pip packages | **One file** to download: no Python, Node or other runtime |
| Platforms | Wherever Python works | Prebuilt for Windows, macOS (Intel + Apple Silicon), Linux (x64, ARM64, ARMv7) |
| Correctness | AI rewrote the whole script and could invent or drop code | **Rename mode**: the AI only suggests names; a scope-aware Luau parser applies them and checks that **every identifier still refers to the same variable**, so behavior can't change |
| Speed and cost | AI re-typed the entire script | The AI returns a small JSON list of names instead of the whole script, large scripts are split and processed in parallel, and results are cached in memory and on disk |
| Reliability | One provider, no retries | Retries with backoff, a fallback chain across providers, and the original output returned if everything fails |
| Interfaces | Executor only | Executor client, built-in **web UI**, **CLI** for files and folders, JSON HTTP API |
| Security | Open to any website the browser loaded | Listens only on this computer, blocks cross-site and DNS-rebinding requests, optional token |

The old `/fix_script` endpoint still works, so the bundled Dark Dex (`dex_betterdecompiler.lua`) and v1 clients
work unchanged with the v2 server.

## Quick start

**1. Download** the archive for your computer from the
[latest release](https://github.com/officialmelon/BetterDecompiler/releases/latest) and extract it:

| Your computer | File |
|---|---|
| Windows | `betterdecompiler-*-windows-amd64.zip` (`arm64` for ARM laptops) |
| Mac with Apple Silicon (M1 or later) | `betterdecompiler-*-darwin-arm64.tar.gz` |
| Mac with Intel | `betterdecompiler-*-darwin-amd64.tar.gz` |
| Linux | `betterdecompiler-*-linux-amd64.tar.gz` (`arm64` / `arm` for Raspberry Pi and similar) |

**2. Run it.** On Windows, double-click `betterdecompiler.exe`. On macOS or Linux, run `./betterdecompiler` in a terminal.
The first time, it asks which AI to use and for your API key. You can skip this to use the offline renamer, and run
`betterdecompiler setup` later to change it.

> macOS may block a downloaded program the first time. Run `xattr -d com.apple.quarantine ./betterdecompiler` once,
> or right-click it in Finder and choose Open.

**3. In your executor**, run:

```lua
loadstring(game:HttpGet("https://github.com/officialmelon/BetterDecompiler/releases/latest/download/betterdecompiler.lua"))()
```

The global `decompile` is now hooked, so **Dex, script viewers and any other tool show cleaned code automatically**.
If the server isn't running or something fails, you get the normal decompiler output, so it's safe to leave enabled.

You can also open **http://localhost:5000** in a browser to paste code into the web UI.

## Providers

Bring a key from any of these. Keys are read from the config file (written by `betterdecompiler setup`) or from the
environment variable shown. If no provider is chosen, BetterDecompiler uses the first one it finds a key for.

| Provider | Default model | API key variable | Notes |
|---|---|---|---|
| `anthropic` | `claude-opus-5-5` | `ANTHROPIC_API_KEY` | Uses server-side refusal fallback automatically |
| `openai` | `gpt-5.6-terra` | `OPENAI_API_KEY` | |
| `gemini` | `gemini-3.7-flash` | `GEMINI_API_KEY` or `GOOGLE_API_KEY` | Has a free tier |
| `openrouter` | `openrouter/auto` | `OPENROUTER_API_KEY` | Hundreds of models with one key |
| `groq` | `openai/gpt-oss-120b` | `GROQ_API_KEY` | Very fast; has a free tier |
| `deepseek` | `deepseek-chat` | `DEEPSEEK_API_KEY` | Low cost |
| `mistral` | `mistral-large-latest` | `MISTRAL_API_KEY` | |
| `xai` | `grok-code-fast-1` | `XAI_API_KEY` | |
| `ollama` | `qwen2.5-coder:7b` | none | Local and free; install [Ollama](https://ollama.com) and `ollama pull qwen2.5-coder:7b` |
| `lmstudio` | the loaded model | none | Local and free; start LM Studio's server |
| `offline` | none | none | No AI: names come from Roblox API usage |
| *custom* | anything | any | Any OpenAI-compatible, Anthropic-compatible or Gemini-compatible endpoint (Azure, Together, Fireworks, vLLM, proxies, ...) |

Default models change as providers release new ones. Pick another with `-m`, in `setup`, or in the config file, and
run `betterdecompiler models -p <provider>` to see what your key can use. For the cheapest and fastest results, use a
small model (for example `claude-haiku-5-5`, a Gemini Flash model, or Groq). Renaming doesn't need the largest model.

## Modes

| Mode | What the AI does | Speed and cost | Can it change behavior? |
|---|---|---|---|
| **`rename`** (default) | Suggests names and comments as compact JSON; BetterDecompiler applies them with its own Luau parser | Fast and cheap: output is a small list of names, not the whole script | **No.** Each rename is checked against the program's scopes. Conflicting names get a suffix (`player2`) and anything unsafe is skipped |
| `rewrite` | Rewrites the code: names, comments and cleaner structure | Slower; the full script is generated | Possibly. AI output is checked for syntax errors, but you should still review it |
| `offline` | No AI. Names come from patterns like `GetService("X")`, `WaitForChild("Name")`, `Instance.new`, event callbacks (`PlayerAdded` → `player`), loops and constructors | Instant and free | **No** |

In `rename` mode, names the AI leaves out are filled in by the offline heuristics. Comments can be `none`, `light`
(default) or `detailed`.

## Executor client

```lua
getgenv().BetterDecompilerConfig = {   -- all optional; set before loading
    Url = "http://localhost:5000",     -- where the server runs
    Token = nil,                       -- if you started the server with --token
    Hook = true,                       -- replace the global decompile
    Mode = nil,                        -- "rename" | "rewrite" | "offline" (nil = server default)
    Provider = nil, Model = nil,       -- override the server's provider or model
    Comments = nil,                    -- "none" | "light" | "detailed"
    Cache = true, Silent = false,
}
local BD = loadstring(game:HttpGet("https://github.com/officialmelon/BetterDecompiler/releases/latest/download/betterdecompiler.lua"))()

print(decompile(script))                            -- hooked: cleaned output
print(BD.decompile(script, { Mode = "rewrite" }))   -- per-call options
local code, info = BD.clean(someSource)             -- clean code you already have
print(info.provider, info.model, info.renamed)
BD.unhook()                                         -- restore the original decompile
```

`BD.decompile(script, enabled, url)` keeps the v1 signature, so v1 scripts keep working. The client works with
executors that expose `request`, `http_request`, `syn.request`, `http.request` or `fluxus.request`. It uses
`getscripthash` (when available) so reopening a script doesn't decompile or clean it again.

**Dex:** load the client first, then any Dex. Because `decompile` is hooked, Dex shows cleaned code. The bundled
`dex_betterdecompiler.lua` also still works, since it talks to the v2 server's v1 endpoint:

```lua
loadstring(game:HttpGet("https://github.com/officialmelon/BetterDecompiler/raw/main/dex_betterdecompiler.lua"))()
```

## Command line

```sh
betterdecompiler                              # start the server (same as `serve`)
betterdecompiler serve --listen 127.0.0.1:5050 --token mysecret
betterdecompiler clean dumped.lua             # cleaned code to stdout
betterdecompiler clean dumped.lua -o clean.lua
betterdecompiler clean ./dump -o ./clean      # whole folder (.lua/.luau), processed in parallel
betterdecompiler clean ./dump --in-place -p groq --comments none
cat dumped.lua | betterdecompiler clean -     # stdin to stdout
betterdecompiler clean dumped.lua --mode offline   # no AI, instant
betterdecompiler clean x.lua --json           # machine-readable result (names, tokens, warnings)
betterdecompiler setup | providers | models -p gemini | config show | version
```

## Configuration

`betterdecompiler setup` writes the config file for you. `betterdecompiler config path` shows where it is (by default
`%AppData%\betterdecompiler\config.json` on Windows, `~/Library/Application Support/betterdecompiler/config.json` on
macOS and `~/.config/betterdecompiler/config.json` on Linux). The file is created readable only by you.

```jsonc
{
  "provider": "anthropic",            // default provider (omit to auto-detect from your keys)
  "fallback": ["gemini", "offline"],  // tried in order if the provider fails (default: ["offline"])
  "mode": "rename",                   // rename | rewrite | offline
  "comments": "light",                // none | light | detailed
  "header": true,                     // add the "-- Cleaned by BetterDecompiler" header
  "listen": "127.0.0.1:5000",
  "token": "",                        // require clients to send this token
  "concurrency": 4,                   // parallel AI requests
  "cache": { "enabled": true, "dir": "", "max_entries": 256 },
  "providers": {
    "anthropic": { "api_key": "sk-ant-...", "model": "claude-haiku-5-5", "effort": "low" },
    "gemini":    { "api_key": "env:GEMINI_API_KEY" },
    "my-azure": {                     // custom OpenAI-compatible endpoint
      "type": "openai",
      "base_url": "https://my-resource.openai.azure.com/openai/v1",
      "api_key": "env:AZURE_OPENAI_KEY",
      "auth_header": "api-key",
      "model": "my-deployment"
    }
  }
}
```

The real file is plain JSON, so leave out the `//` comments. Per-provider options: `type` (`openai`, `anthropic`,
`gemini`), `base_url`, `api_key` (a literal key, `env:NAME` or `${NAME}`), `model`, `max_tokens`, `temperature`,
`effort` (reasoning effort, or `"none"` to send nothing), `headers`, `auth_header`, `json_mode`, `timeout_seconds`,
`max_retries`, `no_fallbacks`.

These environment variables override the file: `BD_PROVIDER`, `BD_MODEL`, `BD_MODE`, `BD_TOKEN`, `BD_LISTEN`,
`BD_CONFIG` (path to the config file), and every provider key variable from the table above. Standard
`HTTPS_PROXY` and `NO_PROXY` are respected.

## HTTP API

| Method and path | Body | Response |
|---|---|---|
| `POST /v1/clean` | `{"source": "...", "mode"?, "provider"?, "model"?, "comments"?, "header"?, "no_cache"?}` | `{"source", "mode", "provider", "model", "summary", "renamed", "comments", "usage": {"input_tokens", "output_tokens"}, "warnings", "cached", "elapsed_ms"}` |
| `POST /fix_script` | `{"script": "..."}` (v1) | `{"fixed_script": "..."}` |
| `GET /v1/providers` | | Ready providers and the active fallback chain |
| `GET /v1/health` | | Version, mode, chain and usage statistics |
| `GET /` | | Web UI |

POST bodies must be `application/json`. When a token is set, send `Authorization: Bearer <token>` or
`X-BD-Token: <token>`. Errors come back as `{"error": "..."}` with status 400 (bad request), 401 (token) or 502
(every provider failed).

## Security and privacy

- Your API key is stored only on your computer, in a file only you can read, and is sent only to the provider you chose.
- The server listens on `127.0.0.1` (this computer only). It rejects requests from other websites (an `Origin` check
  plus JSON-only bodies), so a page open in your browser can't spend your credits, and it only answers to `localhost`
  host names, which blocks DNS-rebinding attacks.
- To use it from another device, start it with `--listen 0.0.0.0:5000 --token <secret>`. BetterDecompiler refuses to
  listen beyond this computer without a token.
- Choose `ollama`, `lmstudio` or `offline` if your code must not leave your machine.

## Troubleshooting

- **"address already in use" on port 5000.** On macOS, AirPlay Receiver uses port 5000. Run
  `betterdecompiler serve --listen 127.0.0.1:5050` and set `Url = "http://localhost:5050"` in the client config.
- **HTTP 401 or "check your API key".** Run `betterdecompiler setup` again, or check the key environment variable.
- **HTTP 404 or "check the model name".** The model was renamed or retired. Run `betterdecompiler models -p <provider>`
  and pick one from the list.
- **"this executor has no HTTP request function".** Your executor can't make HTTP requests to your computer. Use the
  CLI or the web UI instead.
- **The output says "offline".** No API key was found, or every AI provider failed. The warning lines in the header
  say why.

## Building and testing

You need Go 1.22 or newer. Everything is standard library, with no third-party Go dependencies.

```sh
go build ./cmd/betterdecompiler           # build for this computer
scripts/build.sh v2.0.0                   # cross-compile every platform into dist/
go test ./...                             # unit tests: parser, renamer, providers, engine, server, CLI
lua5.1 client/tests/run.lua               # executor client tests (fake executor environment)
scripts/e2e.sh                            # real server + mock AI provider + Lua client, end to end
```

The tests don't need an API key. `internal/mockllm` emulates the OpenAI, Anthropic and Gemini APIs, including errors,
rate limits, refusals and truncated output. The renamer is also checked by **executing** programs before and after
hundreds of randomized rename sets under Lua 5.1 and the official Luau VM (set `LUAU_BIN` to enable the Luau run).
Releases are built by GitHub Actions when a `v*` tag is pushed.

## Project layout

```
cmd/betterdecompiler   CLI and server entry point
internal/luau          Luau lexer, scope-resolving parser, verified renamer, offline name inference
internal/llm           Provider clients (OpenAI-compatible, Anthropic, Gemini), retries, presets
internal/engine        Rename/rewrite/offline pipelines, chunking, caching, fallback chain
internal/server        HTTP API, request protections, embedded web UI
internal/config        Config file, environment, provider setup
internal/mockllm       Fake AI provider used by the tests
client/                Executor client (Luau) and its tests
dex_betterdecompiler.lua  Dark Dex v4 with BetterDecompiler built in (v1 endpoint, still supported)
```

## Credits

Made by [@officiallymelon](https://github.com/officialmelon). v1 used [@xtekky](https://github.com/xtekky)'s g4f
reverse proxy. Thanks to everyone who used and reported issues with v1.

AI-chosen names and comments can be wrong. In rename mode they never change what the code does, but read them as
educated guesses.
