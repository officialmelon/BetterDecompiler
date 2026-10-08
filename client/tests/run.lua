--[[
	Tests for client/betterdecompiler.lua in plain Lua 5.1 with a fake
	Roblox/executor environment.

	  lua5.1 client/tests/run.lua                 -- unit tests (fake HTTP)
	  BD_TEST_URL=http://127.0.0.1:5000 \
	  BD_TEST_EXPECT="local Players" \
	  lua5.1 client/tests/run.lua                 -- also run against a real server (uses curl)
]]

local here = (arg and arg[0] or ""):match("^(.*)[/\\]") or "."
package.path = here .. "/?.lua;" .. package.path
local json = require("json")
local clientPath = here .. "/../betterdecompiler.lua"

local passed, failed = 0, 0
local function test(name, fn)
	local ok, err = pcall(fn)
	if ok then
		passed = passed + 1
		print("ok   " .. name)
	else
		failed = failed + 1
		print("FAIL " .. name .. "\n     " .. tostring(err))
	end
end
local function check(cond, msg)
	if not cond then
		error(msg or "check failed", 2)
	end
end

local SAMPLE = 'local v1 = game:GetService("Players")\nlocal v2 = v1.LocalPlayer\nprint(v2)\n'

-- Builds a fresh fake executor environment and loads the client into it.
local function load(opts)
	opts = opts or {}
	local warnings, requests = {}, {}
	local G = setmetatable({}, { __index = _G })
	G.game = {
		GetService = function(_, name)
			assert(name == "HttpService")
			return {
				JSONEncode = function(_, v) return json.encode(v) end,
				JSONDecode = function(_, s) return json.decode(s) end,
			}
		end,
	}
	G.warn = function(...)
		warnings[#warnings + 1] = table.concat({ ... }, " ")
	end
	G.getgenv = function() return G end
	G.decompile = opts.decompile or function(scr)
		return scr.Source
	end
	G.getscripthash = opts.getscripthash
	G.request = function(req)
		requests[#requests + 1] = req
		return opts.respond(req)
	end
	G.BetterDecompilerConfig = opts.config
	local chunk = assert(loadfile(clientPath))
	setfenv(chunk, G)
	local BD = chunk()
	return BD, G, warnings, requests
end

local function okResponse(source, extra)
	local body = { source = source, provider = "mock", mode = "rename", renamed = 2, warnings = extra }
	return { StatusCode = 200, Body = json.encode(body) }
end

local fakeScript = { Source = SAMPLE }

test("clean posts to /v1/clean with options and returns cleaned code", function()
	local BD, _, _, requests = load({
		config = { Url = "http://h:1/", Provider = "anthropic", Comments = "none", Token = "tok" },
		respond = function(req) return okResponse("CLEANED", nil) end,
	})
	local out, info = BD.clean(SAMPLE)
	check(out == "CLEANED", "got " .. tostring(out))
	check(info.provider == "mock")
	local req = requests[1]
	check(req.Url == "http://h:1/v1/clean", req.Url)
	check(req.Method == "POST")
	check(req.Headers["Authorization"] == "Bearer tok")
	local body = json.decode(req.Body)
	check(body.source == SAMPLE and body.provider == "anthropic" and body.comments == "none")
end)

test("results are cached per source and options", function()
	local BD, _, _, requests = load({ respond = function() return okResponse("X") end })
	BD.clean(SAMPLE)
	BD.clean(SAMPLE)
	check(#requests == 1, "expected 1 request, got " .. #requests)
	BD.clean(SAMPLE, { Mode = "rewrite" })
	check(#requests == 2)
	BD.clean(SAMPLE, { NoCache = true })
	check(#requests == 3)
	BD.clearCache()
	BD.clean(SAMPLE)
	check(#requests == 4)
end)

test("failures return the original source and warn", function()
	local BD, _, warnings = load({ respond = function() return { StatusCode = 502, Body = '{"error":"all providers failed"}' } end })
	local out, err = BD.clean(SAMPLE)
	check(out == SAMPLE)
	check(err:find("502") and err:find("all providers failed"), err)
	check(#warnings == 1 and warnings[1]:find("raw decompiler output"))
end)

test("request errors and missing servers never throw", function()
	local BD = load({ respond = function() error("connection refused") end })
	local out, err = BD.clean(SAMPLE)
	check(out == SAMPLE and err:find("connection refused"))
	BD = load({ respond = function() return { StatusCode = 0, Body = "" } end })
	out, err = BD.clean(SAMPLE)
	check(out == SAMPLE and err:find("is it running"), err)
end)

test("server warnings are surfaced", function()
	local BD, _, warnings = load({ respond = function() return okResponse("X", { "anthropic failed: 401" }) end })
	BD.clean(SAMPLE)
	check(#warnings == 1 and warnings[1]:find("anthropic failed"))
end)

test("Silent hides warnings", function()
	local BD, _, warnings = load({ config = { Silent = true }, respond = function() return { StatusCode = 500, Body = "" } end })
	BD.clean(SAMPLE)
	check(#warnings == 0)
end)

test("hook replaces global decompile and unhook restores it", function()
	local BD, G = load({ respond = function() return okResponse("HOOKED") end })
	check(BD.Hooked)
	check(G.decompile(fakeScript) == "HOOKED")
	BD.unhook()
	check(G.decompile(fakeScript) == SAMPLE)
end)

test("Hook = false leaves decompile alone", function()
	local BD, G = load({ config = { Hook = false }, respond = function() return okResponse("X") end })
	check(not BD.Hooked)
	check(G.decompile(fakeScript) == SAMPLE)
end)

test("v1 signature decompile(script, enabled, url)", function()
	local BD, _, _, requests = load({
		config = { Hook = false },
		respond = function(req)
			if req.Url:find("/fix_script$") then
				return { StatusCode = 200, Body = json.encode({ fixed_script = "LEGACY" }) }
			end
			return okResponse("NEW")
		end,
	})
	check(BD.decompile(fakeScript, false) == SAMPLE, "disabled must return raw output")
	check(#requests == 0)
	check(BD.decompile(fakeScript, true, "http://localhost:5000/fix_script") == "LEGACY")
	check(json.decode(requests[1].Body).script == SAMPLE)
	check(BD.decompile(fakeScript, true) == "NEW")
	check(BD.decompile(fakeScript, { Mode = "offline" }) == "NEW")
	check(json.decode(requests[#requests].Body).mode == "offline")
end)

test("decompiler errors are reported, not thrown", function()
	local BD = load({ decompile = function() error("unsupported script") end, respond = function() return okResponse("X") end })
	local out = BD.decompile(fakeScript)
	check(out:find("Decompile failed") and out:find("unsupported script"), out)
end)

test("script hash cache skips decompiling again", function()
	local decompiles = 0
	local BD, _, _, requests = load({
		decompile = function(scr) decompiles = decompiles + 1; return scr.Source end,
		getscripthash = function() return "abc123" end,
		respond = function() return okResponse("CLEAN") end,
	})
	check(BD.decompile(fakeScript) == "CLEAN")
	check(BD.decompile(fakeScript) == "CLEAN")
	check(decompiles == 1 and #requests == 1, decompiles .. " " .. #requests)
end)

test("loading twice reuses the instance and applies new config", function()
	local BD, G = load({ respond = function() return okResponse("X") end })
	G.BetterDecompilerConfig = { Mode = "rewrite" }
	local chunk = assert(loadfile(clientPath))
	setfenv(chunk, G)
	local again = chunk()
	check(again == BD and BD.Config.Mode == "rewrite")
	check(G.__BD_ORIGINAL_DECOMPILE ~= G.decompile, "must keep the real decompiler")
end)

test("empty input is returned untouched without a request", function()
	local BD, _, _, requests = load({ respond = function() return okResponse("X") end })
	check(BD.clean("") == "" and BD.clean("  \n") == "  \n" and #requests == 0)
end)

-- Integration tests against a real server ------------------------------------

local url = os.getenv("BD_TEST_URL")
if url then
	local tmp = os.tmpname()
	local function curlRequest(req)
		local f = assert(io.open(tmp, "wb"))
		f:write(req.Body or "")
		f:close()
		local cmd = { "curl -s -m 60 -X " .. req.Method }
		for k, v in pairs(req.Headers or {}) do
			cmd[#cmd + 1] = string.format("-H %q", k .. ": " .. v)
		end
		if req.Body then
			cmd[#cmd + 1] = "--data-binary @" .. tmp
		end
		cmd[#cmd + 1] = '-w "\\n%{http_code}"'
		cmd[#cmd + 1] = string.format("%q", req.Url)
		local p = assert(io.popen(table.concat(cmd, " ")))
		local out = p:read("*a")
		p:close()
		local body, code = out:match("^(.*)\n(%d+)$")
		return { StatusCode = tonumber(code) or 0, Body = body or "" }
	end
	local expect = os.getenv("BD_TEST_EXPECT") or "local"

	test("integration: health", function()
		local BD = load({ config = { Url = url }, respond = curlRequest })
		local h, err = BD.health()
		check(h and h.ok == true, tostring(err))
	end)

	test("integration: hooked decompile returns cleaned code", function()
		local BD, G = load({ config = { Url = url, Token = os.getenv("BD_TEST_TOKEN") }, respond = curlRequest })
		local out = G.decompile(fakeScript)
		check(out:find("Cleaned by BetterDecompiler", 1, true), out)
		check(out:find(expect, 1, true), "expected " .. expect .. " in:\n" .. out)
	end)

	test("integration: v1 /fix_script URL", function()
		local BD = load({ config = { Hook = false, Token = os.getenv("BD_TEST_TOKEN") }, respond = curlRequest })
		local out = BD.decompile(fakeScript, true, url .. "/fix_script")
		check(out:find(expect, 1, true), out)
	end)

	test("integration: offline mode via options", function()
		local BD = load({ config = { Url = url, Token = os.getenv("BD_TEST_TOKEN") }, respond = curlRequest })
		local out, info = BD.clean(SAMPLE, { Mode = "offline" })
		check(info and info.mode == "offline", tostring(info))
		check(out:find('local Players = game:GetService("Players")', 1, true), out)
	end)

	test("integration: bad server URL falls back to raw output", function()
		local BD = load({ config = { Url = "http://127.0.0.1:9" }, respond = curlRequest })
		local out, err = BD.clean(SAMPLE)
		check(out == SAMPLE and err, tostring(err))
	end)
	os.remove(tmp)
end

print(string.format("\n%d passed, %d failed", passed, failed))
os.exit(failed == 0 and 0 or 1)
