#!/usr/bin/env bash
# End-to-end test with no API keys: real server binary + mock AI provider +
# the Lua client (run in Lua 5.1 with a fake executor environment).
set -euo pipefail
cd "$(dirname "$0")/.."

LUA="${LUA:-$(command -v lua5.1 || command -v lua || true)}"
[[ -n "$LUA" ]] || { echo "lua5.1 is required"; exit 1; }
command -v curl >/dev/null || { echo "curl is required"; exit 1; }

tmp="$(mktemp -d)"
pids=()
cleanup() { for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done; rm -rf "$tmp"; }
trap cleanup EXIT

go build -o "$tmp/betterdecompiler" ./cmd/betterdecompiler
go build -o "$tmp/mockllm" ./cmd/mockllm

wait_up() { for _ in $(seq 1 50); do curl -fs "$1/v1/health" >/dev/null 2>&1 && return 0; sleep 0.1; done; echo "server at $1 did not start"; exit 1; }

"$tmp/mockllm" -addr 127.0.0.1:5199 -key e2e-key &
pids+=($!)
sleep 0.3

# 1) AI path: a custom OpenAI-compatible provider pointing at the mock.
cat > "$tmp/config.json" <<JSON
{
  "provider": "mock",
  "providers": { "mock": { "type": "openai", "base_url": "http://127.0.0.1:5199/v1", "api_key": "env:E2E_KEY", "model": "mock-1" } },
  "cache": { "enabled": false }
}
JSON
E2E_KEY=e2e-key "$tmp/betterdecompiler" serve --config "$tmp/config.json" --listen 127.0.0.1:5198 --quiet > "$tmp/ai.log" 2>&1 &
pids+=($!)
wait_up http://127.0.0.1:5198
echo "== Lua client against AI server (mock provider)"
BD_TEST_URL=http://127.0.0.1:5198 BD_TEST_EXPECT="mockV1" "$LUA" client/tests/run.lua

echo "== v1 Dex-style request to /fix_script"
out="$(curl -fs -X POST -H 'Content-Type: application/json' --data '{"script":"local v1 = game:GetService(\"Players\")"}' http://127.0.0.1:5198/fix_script)"
grep -q 'mockV1' <<<"$out" || { echo "legacy endpoint failed: $out"; exit 1; }
echo "ok"

echo "== CLI clean of a folder through the AI provider"
mkdir -p "$tmp/in/sub" && cp examples/decompiled.lua "$tmp/in/a.lua" && cp examples/decompiled.lua "$tmp/in/sub/b.luau"
E2E_KEY=e2e-key "$tmp/betterdecompiler" clean --config "$tmp/config.json" "$tmp/in" -o "$tmp/out" -q
grep -q 'mockV1' "$tmp/out/a.lua" && grep -q 'mockV1' "$tmp/out/sub/b.luau" || { echo "folder clean failed"; exit 1; }
echo "ok"

# 2) Offline path with a token.
cat > "$tmp/offline.json" <<JSON
{ "provider": "offline", "token": "tok123", "cache": { "enabled": false } }
JSON
"$tmp/betterdecompiler" serve --config "$tmp/offline.json" --listen 127.0.0.1:5197 --quiet > "$tmp/off.log" 2>&1 &
pids+=($!)
wait_up http://127.0.0.1:5197
echo "== Lua client against offline server with token"
BD_TEST_URL=http://127.0.0.1:5197 BD_TEST_TOKEN=tok123 BD_TEST_EXPECT='local Players = game:GetService("Players")' "$LUA" client/tests/run.lua
code="$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' --data '{"source":"x()"}' http://127.0.0.1:5197/v1/clean)"
[[ "$code" == 401 ]] || { echo "expected 401 without token, got $code"; exit 1; }

echo
echo "end-to-end: all passed"
