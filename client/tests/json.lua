-- Minimal JSON encoder/decoder used to emulate Roblox's HttpService in tests.
local json = {}

local escapes = { ['"'] = '\\"', ["\\"] = "\\\\", ["\b"] = "\\b", ["\f"] = "\\f", ["\n"] = "\\n", ["\r"] = "\\r", ["\t"] = "\\t" }

local function encodeString(s)
	return '"' .. s:gsub('[%c"\\]', function(c)
		return escapes[c] or string.format("\\u%04x", c:byte())
	end) .. '"'
end

local function isArray(t)
	local n = 0
	for k in pairs(t) do
		if type(k) ~= "number" then
			return false
		end
		n = n + 1
	end
	return n == #t
end

function json.encode(v)
	local t = type(v)
	if t == "nil" then
		return "null"
	elseif t == "boolean" or t == "number" then
		return tostring(v)
	elseif t == "string" then
		return encodeString(v)
	elseif t == "table" then
		local parts = {}
		if next(v) ~= nil and isArray(v) then
			for i = 1, #v do
				parts[i] = json.encode(v[i])
			end
			return "[" .. table.concat(parts, ",") .. "]"
		end
		for k, val in pairs(v) do
			parts[#parts + 1] = encodeString(tostring(k)) .. ":" .. json.encode(val)
		end
		return "{" .. table.concat(parts, ",") .. "}"
	end
	error("cannot encode " .. t)
end

function json.decode(s)
	local pos = 1
	local function ws()
		pos = s:find("[^ \t\r\n]", pos) or #s + 1
	end
	local value
	local function str()
		pos = pos + 1
		local out = {}
		while true do
			local c = s:sub(pos, pos)
			if c == "" then
				error("unterminated string")
			elseif c == '"' then
				pos = pos + 1
				return table.concat(out)
			elseif c == "\\" then
				local e = s:sub(pos + 1, pos + 1)
				local map = { b = "\b", f = "\f", n = "\n", r = "\r", t = "\t", ['"'] = '"', ["\\"] = "\\", ["/"] = "/" }
				if e == "u" then
					local code = tonumber(s:sub(pos + 2, pos + 5), 16)
					if code < 128 then
						out[#out + 1] = string.char(code)
					elseif code < 2048 then
						out[#out + 1] = string.char(192 + math.floor(code / 64), 128 + code % 64)
					else
						out[#out + 1] = string.char(224 + math.floor(code / 4096), 128 + math.floor(code / 64) % 64, 128 + code % 64)
					end
					pos = pos + 6
				else
					out[#out + 1] = map[e]
					pos = pos + 2
				end
			else
				out[#out + 1] = c
				pos = pos + 1
			end
		end
	end
	function value()
		ws()
		local c = s:sub(pos, pos)
		if c == "{" then
			pos = pos + 1
			local t = {}
			ws()
			if s:sub(pos, pos) == "}" then
				pos = pos + 1
				return t
			end
			while true do
				ws()
				local k = str()
				ws()
				pos = pos + 1 -- :
				t[k] = value()
				ws()
				local d = s:sub(pos, pos)
				pos = pos + 1
				if d == "}" then
					return t
				end
			end
		elseif c == "[" then
			pos = pos + 1
			local t = {}
			ws()
			if s:sub(pos, pos) == "]" then
				pos = pos + 1
				return t
			end
			while true do
				t[#t + 1] = value()
				ws()
				local d = s:sub(pos, pos)
				pos = pos + 1
				if d == "]" then
					return t
				end
			end
		elseif c == '"' then
			return str()
		elseif s:sub(pos, pos + 3) == "true" then
			pos = pos + 4
			return true
		elseif s:sub(pos, pos + 4) == "false" then
			pos = pos + 5
			return false
		elseif s:sub(pos, pos + 3) == "null" then
			pos = pos + 4
			return nil
		else
			local num = s:match("^-?%d+%.?%d*[eE]?[-+]?%d*", pos)
			if not num or num == "" then
				error("unexpected character at " .. pos .. ": " .. c)
			end
			pos = pos + #num
			return tonumber(num)
		end
	end
	local v = value()
	return v
end

return json
