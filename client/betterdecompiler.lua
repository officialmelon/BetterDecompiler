--[[
	BetterDecompiler client v2
	https://github.com/officialmelon/BetterDecompiler

	Sends your executor's decompiler output to the local BetterDecompiler
	server, which recovers variable names and adds comments with the AI provider
	you configured (or fully offline). If anything fails, you get the normal
	decompiler output back, so it is always safe to leave enabled.

	Quick start (the server must be running on your computer):

		loadstring(game:HttpGet("https://github.com/officialmelon/BetterDecompiler/releases/latest/download/betterdecompiler.lua"))()

	That hooks the global `decompile`, so Dex and every other tool get cleaned
	output automatically. Options (set before loading, all optional):

		getgenv().BetterDecompilerConfig = {
			Url = "http://localhost:5000", -- where the server runs
			Token = nil,                    -- if the server was started with --token
			Hook = true,                    -- replace the global decompile
			Enabled = true,                 -- false = pass raw decompiler output through
			Mode = nil,                     -- "rename" | "rewrite" | "offline" (nil = server default)
			Provider = nil,                 -- e.g. "anthropic", "openai", "gemini", "ollama"
			Model = nil,                    -- e.g. "claude-haiku-5-5"
			Comments = nil,                 -- "none" | "light" | "detailed"
			Cache = true,                   -- remember results for scripts already cleaned
			Silent = false,                 -- hide warnings
		}

	API (returned by loadstring, also at getgenv().BetterDecompiler):

		BD.decompile(script [, options])   -- decompile + clean; never errors
		BD.decompile(script, enabled, url) -- v1-compatible signature
		BD.clean(source [, options])       -- clean code you already have -> cleaned, info
		BD.hook() / BD.unhook()            -- install / remove the global decompile hook
		BD.health()                        -- server status table, or nil, error
		BD.configure({ ... })              -- change options at runtime
]]

local VERSION = "2.0.0"

local env = (type(getgenv) == "function" and getgenv()) or _G
local userConfig = rawget(env, "BetterDecompilerConfig")

-- Loading twice returns the existing instance (and applies new options).
local existing = rawget(env, "BetterDecompiler")
if type(existing) == "table" and existing.Version == VERSION then
	if type(userConfig) == "table" then
		existing.configure(userConfig)
	end
	return existing
end

local HttpService = game:GetService("HttpService")

local BD = {
	Version = VERSION,
	Hooked = false,
	Config = {
		Url = "http://localhost:5000",
		Token = nil,
		Hook = true,
		Enabled = true,
		Mode = nil,
		Provider = nil,
		Model = nil,
		Comments = nil,
		Cache = true,
		Silent = false,
	},
}

local requestFn = (type(syn) == "table" and syn.request)
	or (type(http) == "table" and http.request)
	or rawget(env, "http_request")
	or (type(fluxus) == "table" and fluxus.request)
	or rawget(env, "request")
	or request

-- The executor's own decompiler. If a previous version hooked it, keep the
-- real one rather than wrapping our own wrapper.
local original = rawget(env, "__BD_ORIGINAL_DECOMPILE") or rawget(env, "decompile") or decompile
if original then
	env.__BD_ORIGINAL_DECOMPILE = original
end

local function log(...)
	if not BD.Config.Silent then
		warn("[BetterDecompiler]", ...)
	end
end

function BD.configure(options)
	if type(options) ~= "table" then
		return BD.Config
	end
	for k, v in pairs(options) do
		BD.Config[k] = v
	end
	return BD.Config
end

local function send(method, url, payload)
	if not requestFn then
		return nil, "this executor has no HTTP request function"
	end
	local headers = { ["Content-Type"] = "application/json" }
	if BD.Config.Token then
		headers["Authorization"] = "Bearer " .. tostring(BD.Config.Token)
	end
	local options = { Url = url, Method = method, Headers = headers }
	if payload ~= nil then
		options.Body = HttpService:JSONEncode(payload)
	end
	local ok, res = pcall(requestFn, options)
	if not ok then
		return nil, "request failed: " .. tostring(res)
	end
	if type(res) ~= "table" then
		return nil, "no response (is the BetterDecompiler server running at " .. tostring(BD.Config.Url) .. "?)"
	end
	local decoded, data = pcall(function()
		return HttpService:JSONDecode(res.Body or "")
	end)
	local status = tonumber(res.StatusCode) or 0
	if status ~= 200 then
		local msg = (decoded and type(data) == "table" and data.error) or res.Body or res.StatusMessage
		if status == 0 then
			msg = "could not reach the server at " .. tostring(BD.Config.Url) .. " (is it running?)"
		end
		return nil, "HTTP " .. tostring(status) .. ": " .. tostring(msg)
	end
	if not decoded or type(data) ~= "table" then
		return nil, "invalid response from server"
	end
	return data
end

local function baseUrl(url)
	url = tostring(url or BD.Config.Url):gsub("/+$", "")
	return url
end

local cache, cacheSize = {}, 0
local MAX_CACHE = 128

local function cacheKey(prefix, opts)
	return table.concat({
		prefix,
		tostring(opts.Mode or BD.Config.Mode),
		tostring(opts.Provider or BD.Config.Provider),
		tostring(opts.Model or BD.Config.Model),
		tostring(opts.Comments or BD.Config.Comments),
		tostring(opts.Url or BD.Config.Url),
	}, "\0")
end

local function remember(key, value, info)
	if not BD.Config.Cache then
		return
	end
	if cacheSize >= MAX_CACHE then
		cache, cacheSize = {}, 0
	end
	if cache[key] == nil then
		cacheSize = cacheSize + 1
	end
	cache[key] = { value, info }
end

-- Cleans decompiled source. Returns the cleaned code and an info table
-- (provider, model, mode, renamed, warnings, ...). On failure returns the
-- original source plus an error message, so callers can always use result 1.
function BD.clean(source, opts)
	opts = opts or {}
	if type(source) ~= "string" or not source:find("%S") then
		return source
	end
	local key = cacheKey(source, opts)
	local hit = BD.Config.Cache and not opts.NoCache and cache[key]
	if hit then
		return hit[1], hit[2]
	end
	local url = opts.Url or BD.Config.Url
	local data, err
	if tostring(url):find("/fix_script/?$") then
		-- v1 server URL
		data, err = send("POST", url, { script = source })
		if data then
			data = { source = data.fixed_script, provider = "v1" }
		end
	else
		data, err = send("POST", baseUrl(url) .. "/v1/clean", {
			source = source,
			mode = opts.Mode or BD.Config.Mode,
			provider = opts.Provider or BD.Config.Provider,
			model = opts.Model or BD.Config.Model,
			comments = opts.Comments or BD.Config.Comments,
			no_cache = opts.NoCache or nil,
		})
	end
	if not data or type(data.source) ~= "string" then
		err = err or "server returned no code"
		log("could not clean script, showing raw decompiler output: " .. tostring(err))
		return source, err
	end
	if type(data.warnings) == "table" then
		for _, w in ipairs(data.warnings) do
			log(w)
		end
	end
	remember(key, data.source, data)
	return data.source, data
end

local function scriptHash(scr)
	local fn = rawget(env, "getscripthash") or getscripthash
	if type(fn) ~= "function" then
		return nil
	end
	local ok, hash = pcall(fn, scr)
	if ok and type(hash) == "string" and hash ~= "" then
		return hash
	end
	return nil
end

-- Decompiles a script and cleans the result. Accepts the v1 signature
-- (script, enabled, customUrl) or (script, options).
function BD.decompile(scr, enabled, customUrl)
	local opts = {}
	if type(enabled) == "table" then
		opts = enabled
		enabled = opts.Enabled
	end
	if customUrl ~= nil then
		opts.Url = customUrl
	end
	if enabled == nil then
		enabled = BD.Config.Enabled
	end
	if not original then
		return "-- BetterDecompiler: this executor has no decompile function"
	end

	local hash = enabled and scriptHash(scr)
	local hashKey = hash and cacheKey("hash:" .. hash, opts)
	if hashKey and BD.Config.Cache and not opts.NoCache and cache[hashKey] then
		return cache[hashKey][1]
	end

	local ok, source = pcall(original, scr)
	if not ok or type(source) ~= "string" then
		log("decompile failed: " .. tostring(source))
		return ok and source or ("-- Decompile failed: " .. tostring(source))
	end
	if not enabled then
		return source
	end
	local cleaned, info = BD.clean(source, opts)
	if hashKey and type(info) == "table" then
		remember(hashKey, cleaned, info)
	end
	return cleaned
end

local hookFn = function(scr, ...)
	return BD.decompile(scr)
end

function BD.hook()
	if not original then
		log("this executor has no decompile function to hook")
		return false
	end
	env.decompile = hookFn
	BD.Hooked = true
	return true
end

function BD.unhook()
	if original then
		env.decompile = original
	end
	BD.Hooked = false
end

function BD.health()
	return send("GET", baseUrl() .. "/v1/health")
end

function BD.clearCache()
	cache, cacheSize = {}, 0
end

BD.configure(userConfig)
if BD.Config.Hook then
	BD.hook()
end
env.BetterDecompiler = BD
return BD
