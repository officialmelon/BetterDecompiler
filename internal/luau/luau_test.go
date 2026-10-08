package luau

import (
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mustParse(t *testing.T, src string) *File {
	t.Helper()
	f, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	return f
}

func TestLexer(t *testing.T) {
	src := "local a = 0x1F + 1e-5 + .5 -- c\n" +
		"local s = [==[ long ]] ]==] .. \"q\\\"x\" .. 'y'\n" +
		"--[[ block\ncomment ]]\n" +
		"local i = `a{b}c{`x{y}`}d`\n" +
		"x //= 2; y ..= \"z\"; z = 1..2"
	toks, err := Lex(src)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, tk := range toks {
		if tk.Kind == Number || tk.Kind == String || tk.Kind == Comment || tk.Kind >= InterpBegin && tk.Kind <= InterpEnd {
			kinds = append(kinds, tk.Text)
		}
	}
	want := []string{"0x1F", "1e-5", ".5", "-- c", "[==[ long ]] ]==]", `"q\"x"`, "'y'", "--[[ block\ncomment ]]",
		"`a{", "}c{", "`x{", "}`", "}d`", "2", `"z"`, "1", "2"}
	if strings.Join(kinds, "|") != strings.Join(want, "|") {
		t.Fatalf("tokens:\n got %q\nwant %q", kinds, want)
	}
	if toks[len(toks)-1].Line != 6 {
		t.Fatalf("line tracking: got %d", toks[len(toks)-1].Line)
	}
}

func TestLexErrors(t *testing.T) {
	for _, src := range []string{`local s = "abc`, "local s = [[abc", "--[[ x", "local s = `abc{x"} {
		if _, err := Lex(src); err == nil {
			t.Errorf("expected error for %q", src)
		}
	}
}

func TestParseLuauSyntax(t *testing.T) {
	src := `
--!strict
export type Item = { name: string, price: number? }
type Map<K, V> = { [K]: V }
local Players: Players = game:GetService("Players")
local function f<T>(a: T, ...: number): (T, number)
	local b = if a then 1 elseif false then 2 else 3
	b += 1
	b //= 2
	for i: number = 1, 10 do
		if i == 2 then continue end
	end
	local cb: (number) -> () = function(x) end
	local t = { a = 1, ["b"] = 2, 3; [b] = b }
	local s = ` + "`value {t.a} and {`nested {b}`}`" + `
	local c = a :: any
	local v: typeof(b) = b
	return a, #t
end
@native
local function g() end
function Players.Thing:Method(x)
	return self, x
end
local continue = 1
continue += 1
repeat local z = 1 until z == 1
`
	f := mustParse(t, src)
	// self must resolve to the implicit binding of the method.
	found := false
	for i, tk := range f.Toks {
		if tk.Text == "self" {
			found = true
			if id := f.Resolve[i]; id < 0 || f.Bindings[id].Kind != BindSelf {
				t.Fatalf("self resolved to %d", id)
			}
		}
	}
	if !found {
		t.Fatal("self not found")
	}
	// `z` in the until condition sees the loop body's local.
	for i, tk := range f.Toks {
		if tk.Text == "z" && f.Resolve[i] == Global {
			t.Fatal("repeat-until scoping broken")
		}
	}
}

func TestLocalSeesOuterInInitializer(t *testing.T) {
	f := mustParse(t, "local x = 1\nlocal x = x + 1\nprint(x)")
	var xs []int
	for i, tk := range f.Toks {
		if tk.Text == "x" {
			xs = append(xs, f.Resolve[i])
		}
	}
	// decl1, decl2, rhs-ref(->decl1), print-ref(->decl2)
	if len(xs) != 4 || xs[2] != xs[0] || xs[3] != xs[1] || xs[0] == xs[1] {
		t.Fatalf("resolution: %v", xs)
	}
}

func TestRenameAvoidsCapture(t *testing.T) {
	src := `local v1 = game:GetService("Players")
local v2 = 1
local function f(p1)
	local Players = 5
	return v1, Players, p1, foo
end
print(f(v2))`
	f := mustParse(t, src)
	ids := map[string]int{}
	for _, b := range f.Bindings {
		ids[b.Name] = b.ID
	}
	out, applied := f.Rename(map[int]string{
		ids["v1"]: "Players", // would be captured by the inner local
		ids["v2"]: "foo",     // would capture the global foo used inside f? no: foo is global; v2 is top-level so f's foo would bind to it
		ids["p1"]: "print",   // reserved -> ignored
	})
	if applied[ids["v1"]] != "Players2" {
		t.Errorf("v1 -> %q, want Players2\n%s", applied[ids["v1"]], out)
	}
	if applied[ids["v2"]] != "foo2" {
		t.Errorf("v2 -> %q, want foo2\n%s", applied[ids["v2"]], out)
	}
	if _, ok := applied[ids["p1"]]; ok {
		t.Errorf("reserved name applied")
	}
	assertSameShape(t, f, out)
}

// assertSameShape checks that out resolves identically to f.
func assertSameShape(t *testing.T, f *File, out string) {
	t.Helper()
	g := mustParse(t, out)
	if len(g.Toks) != len(f.Toks) {
		t.Fatalf("token count changed: %d -> %d", len(f.Toks), len(g.Toks))
	}
	for i := range f.Resolve {
		if f.Resolve[i] != g.Resolve[i] {
			t.Fatalf("token %d (%q -> %q) resolves to %d, was %d\n%s", i, f.Toks[i].Text, g.Toks[i].Text, g.Resolve[i], f.Resolve[i], out)
		}
	}
}

func TestSuggestNamesOnSample(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "decompiled.lua"))
	if err != nil {
		t.Fatal(err)
	}
	f := mustParse(t, string(src))
	out, _ := f.Rename(f.SuggestNames())
	assertSameShape(t, f, out)
	for _, want := range []string{
		`local Players = game:GetService("Players")`,
		`local localPlayer = Players.LocalPlayer`,
		`local ShopConfig = require(`,
		`for key, item in pairs(ShopConfig.Items) do`,
		`local textButton = Instance.new("TextButton")`,
		`textButton.Text = ` + "`{item.DisplayName} - {item.Price}`",
		`CharacterAdded:Connect(function(character)`,
		`local humanoid = character:WaitForChild("Humanoid")`,
		`InputBegan:Connect(function(input, gameProcessedEvent)`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestUniqueKeys(t *testing.T) {
	src := "a(function(p1) return p1 end)\nb(function(p1) return p1 end)\nlocal p1_1 = 1\nlocal v = 2"
	f := mustParse(t, src)
	keyed, keys := f.UniqueKeys()
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			t.Fatalf("duplicate key %q", k)
		}
		seen[k] = true
	}
	if !seen["v"] || !seen["p1_1"] || !seen["p1_2"] || !seen["p1_3"] {
		t.Fatalf("keys: %v", keys)
	}
	assertSameShape(t, f, keyed)
}

func TestInsertComments(t *testing.T) {
	src := "local a = [[\nline2\n]]\nif a then\n\tprint(a)\nend\n"
	f := mustParse(t, src)
	out := f.InsertComments(src, map[int]string{2: "inside string", 4: "check a", 5: "print it\nnow", 99: "nope"})
	want := "local a = [[\nline2\n]]\n-- check a\nif a then\n\t-- print it now\n\tprint(a)\nend\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

// Lua 5.1 programs used to check that renaming preserves behavior when
// actually executed.
var execPrograms = []string{
	`local v1 = 10
local v2 = function(p1, p2) return p1 * v1 + p2 end
local v3 = {}
for v4 = 1, 5 do
	local v5 = v2(v4, v1)
	v3[#v3 + 1] = v5
end
local v6 = 0
for v7, v8 in ipairs(v3) do v6 = v6 + v7 * v8 end
print(v6, #v3)`,
	`local a = 1
local b = 2
local function c(d)
	local a = d + b
	local function e(f) return a + f + b end
	return e(a)
end
local g = c(a)
do local b = 100; g = g + b end
print(g, a, b)
x = 5 -- global
local function h() return x end
print(h())`,
	`local v1 = {}
function v1.new(p1)
	local v2 = setmetatable({}, {__index = v1})
	v2.n = p1
	return v2
end
function v1:get() return self.n end
local v3 = v1.new(7)
local v4 = 0
repeat local v5 = v4 + 1; v4 = v5 until v5 >= 3
local v6 = {a = 1, b = 2}
local v7 = 0
for v8, v9 in pairs(v6) do v7 = v7 + v9 end
print(v3:get(), v4, v7, v6.a)`,
	`local v1, v2 = 1, 2
v1, v2 = v2, v1
local v3 = (function(...) local v4 = select("#", ...) return v4 end)(1, 2, 3)
local v5 = v1 > v2 and "gt" or "le"
while v1 > 0 do local v6 = v1; v1 = v6 - 1 end
print(v1, v2, v3, v5, tostring(nil))`,
}

func TestRenamePreservesExecution(t *testing.T) {
	lua, err := exec.LookPath("lua5.1")
	if err != nil {
		if lua, err = exec.LookPath("lua"); err != nil {
			t.Skip("no Lua interpreter available")
		}
	}
	run := func(src string) string {
		cmd := exec.Command(lua, "-")
		cmd.Stdin = strings.NewReader(src)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("lua failed: %v\n%s\n%s", err, out, src)
		}
		return string(out)
	}
	pool := []string{"a", "b", "c", "x", "d", "n", "value", "v1", "v2", "p1", "setmetatable2", "print"}
	rng := rand.New(rand.NewSource(1))
	for pi, prog := range execPrograms {
		want := run(prog)
		f := mustParse(t, prog)
		for trial := 0; trial < 40; trial++ {
			ren := map[int]string{}
			for _, b := range f.Bindings {
				if b.Renamable() && rng.Intn(3) > 0 {
					ren[b.ID] = pool[rng.Intn(len(pool))]
				}
			}
			out, _ := f.Rename(ren)
			if got := run(out); got != want {
				t.Fatalf("program %d trial %d changed behavior:\nwant %s got %s\n%s", pi, trial, want, got, out)
			}
			assertSameShape(t, f, out)
		}
	}
}

func TestLooksGenerated(t *testing.T) {
	for _, n := range []string{"v1", "u12", "p3", "l_Players_0", "L0_1", "var_4", "upval2", "_5", "slot3", "v_u_1", "p_u_12"} {
		if !LooksGenerated(n) {
			t.Errorf("%s should look generated", n)
		}
	}
	for _, n := range []string{"player", "value", "Players", "i", "v", "x1y"} {
		if LooksGenerated(n) {
			t.Errorf("%s should not look generated", n)
		}
	}
}

func TestCamel(t *testing.T) {
	cases := map[string]string{"HumanoidRootPart": "humanoidRootPart", "UI": "ui", "Shop Gui": "shopGui",
		"kill_brick": "killBrick", "3D": "_3d", "": ""}
	for in, want := range cases {
		if got := camel(in); got != want {
			t.Errorf("camel(%q) = %q, want %q", in, got, want)
		}
	}
	if got := pascal("remote events"); got != "RemoteEvents" {
		t.Errorf("pascal: %q", got)
	}
}

func BenchmarkParseAndSuggest(b *testing.B) {
	src, _ := os.ReadFile(filepath.Join("..", "..", "examples", "decompiled.lua"))
	big := strings.Repeat(string(src)+"\n", 50)
	b.SetBytes(int64(len(big)))
	for i := 0; i < b.N; i++ {
		f, err := Parse(big)
		if err != nil {
			b.Fatal(err)
		}
		f.Rename(f.SuggestNames())
	}
}

// Luau-only programs, executed with the official Luau VM when available
// (set LUAU_BIN or put `luau` on PATH).
var luauPrograms = []string{
	`local v1: number = 3
local v2 = if v1 > 2 then "big" elseif v1 > 1 then "mid" else "small"
local v3 = 0
for v4 = 1, 10 do
	if v4 % 2 == 0 then continue end
	v3 += v4
end
local v5 = ` + "`{v2}:{v3}:{if v3 > 20 then \"many\" else \"few\"}`" + `
print(v5)`,
	`type Point = { x: number, y: number }
local function v1<T>(p1: T, ...: number): (T, number)
	local v2 = 0
	for _, v3 in { ... } do v2 += v3 end
	return p1, v2
end
local v4: Point = { x = 1, y = 2 }
local v5, v6 = v1(v4, 4, 5, 6)
local v7: typeof(v6) = v6 :: number
print(v5.x + v5.y, v7)`,
	`local v1 = {}
v1.__index = v1
function v1.new(p1) return setmetatable({ n = p1 }, v1) end
function v1:add(p1) self.n += p1; return self end
local v2 = v1.new(1):add(2):add(3)
local v3 = 0
repeat local v4 = v3 + 1; v3 = v4 until v4 >= 3
local v5 = function(...) local v6 = select("#", ...) return v6 end
print(v2.n, v3, v5(1, nil, 3), ` + "`nested {`inner {v3}`}`" + `)`,
}

func TestRenamePreservesLuauExecution(t *testing.T) {
	bin := os.Getenv("LUAU_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("luau"); err != nil {
			t.Skip("Luau VM not available (set LUAU_BIN)")
		}
	}
	dir := t.TempDir()
	run := func(src string) string {
		p := filepath.Join(dir, "prog.luau")
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(bin, p).CombinedOutput()
		if err != nil {
			t.Fatalf("luau failed: %v\n%s\n%s", err, out, src)
		}
		return string(out)
	}
	pool := []string{"a", "b", "x", "n", "v1", "p1", "value", "point", "count"}
	rng := rand.New(rand.NewSource(2))
	for pi, prog := range luauPrograms {
		want := run(prog)
		f := mustParse(t, prog)
		suggested, _ := f.Rename(f.SuggestNames())
		if got := run(suggested); got != want {
			t.Fatalf("program %d: suggested names changed behavior:\n%s", pi, suggested)
		}
		for trial := 0; trial < 25; trial++ {
			ren := map[int]string{}
			for _, b := range f.Bindings {
				if b.Renamable() && rng.Intn(3) > 0 {
					ren[b.ID] = pool[rng.Intn(len(pool))]
				}
			}
			out, _ := f.Rename(ren)
			if got := run(out); got != want {
				t.Fatalf("program %d trial %d changed behavior:\nwant %s got %s\n%s", pi, trial, want, got, out)
			}
		}
	}
}
