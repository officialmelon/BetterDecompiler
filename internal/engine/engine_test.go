package engine

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/officialmelon/betterdecompiler/internal/llm"
	"github.com/officialmelon/betterdecompiler/internal/luau"
	"github.com/officialmelon/betterdecompiler/internal/mockllm"
)

func init() {
	llm.Backoff = func(int, time.Duration) time.Duration { return time.Millisecond }
}

func sample(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "decompiled.lua"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type fixture struct {
	mock *mockllm.Server
	url  string
}

func newFixture(t *testing.T) *fixture {
	m := mockllm.New("sk-test")
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return &fixture{m, srv.URL}
}

func (fx *fixture) client(t *testing.T, name string, typ llm.Type, key string) llm.Client {
	t.Helper()
	base := fx.url
	switch typ {
	case llm.TypeOpenAI:
		base += "/v1"
	case llm.TypeGemini:
		base += "/v1beta"
	}
	model := "mock-1"
	if typ == llm.TypeGemini {
		model = "mock-gemini"
	}
	c, err := llm.New(llm.Config{Name: name, Type: typ, BaseURL: base, APIKey: key, Model: model, JSONMode: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func defaultSettings() Settings {
	return Settings{Header: true, Heuristics: true, Version: "test", Comments: "light"}
}

// stripHeader removes the leading header comment block.
func stripHeader(s string) string {
	if i := strings.Index(s, "\n\n"); strings.HasPrefix(s, "-- Cleaned by") && i >= 0 {
		return s[i+2:]
	}
	return s
}

func assertSameBehavior(t *testing.T, before, after string) {
	t.Helper()
	a, err := luau.Parse(before)
	if err != nil {
		t.Fatal(err)
	}
	b, err := luau.Parse(after)
	if err != nil {
		t.Fatalf("output does not parse: %v\n%s", err, after)
	}
	if len(a.Toks) != len(b.Toks) {
		t.Fatalf("token count changed %d -> %d", len(a.Toks), len(b.Toks))
	}
	for i := range a.Resolve {
		if a.Resolve[i] != b.Resolve[i] {
			t.Fatalf("token %d (%s->%s) resolution changed", i, a.Toks[i].Text, b.Toks[i].Text)
		}
		if a.Toks[i].Kind != luau.Name && a.Toks[i].Text != b.Toks[i].Text {
			t.Fatalf("non-identifier token %d changed: %q -> %q", i, a.Toks[i].Text, b.Toks[i].Text)
		}
	}
}

func TestOfflineMode(t *testing.T) {
	e := New(defaultSettings(), nil, nil, nil)
	src := sample(t)
	res, err := e.Clean(context.Background(), src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeOffline || res.Renamed < 10 {
		t.Fatalf("offline result: %+v", res)
	}
	if !strings.HasPrefix(res.Source, "-- Cleaned by BetterDecompiler test | offline | offline mode") {
		t.Fatalf("header missing:\n%s", res.Source)
	}
	assertSameBehavior(t, src, stripHeader(res.Source))
}

func TestRenameModeAllProtocols(t *testing.T) {
	src := sample(t)
	for _, typ := range []llm.Type{llm.TypeOpenAI, llm.TypeAnthropic, llm.TypeGemini} {
		t.Run(string(typ), func(t *testing.T) {
			fx := newFixture(t)
			s := defaultSettings()
			s.Heuristics = false
			e := New(s, map[string]llm.Client{"p": fx.client(t, "p", typ, "sk-test")}, []string{"p"}, nil)
			res, err := e.Clean(context.Background(), src, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Mode != ModeRename || res.Provider != "p" || res.Summary != "Mock summary of the script." {
				t.Fatalf("result: %+v", res)
			}
			out := stripHeader(res.Source)
			for _, want := range []string{"local mockV1 = game:GetService", "function(mockP1_", "-- mock comment"} {
				if !strings.Contains(out, want) {
					t.Fatalf("missing %q:\n%s", want, out)
				}
			}
			if !strings.Contains(res.Source, "-- Summary: Mock summary of the script.") {
				t.Fatal("summary not in header")
			}
			// Remove the inserted comment and compare behavior.
			assertSameBehavior(t, src, strings.Replace(out, "-- mock comment\n", "", 1))
			if res.Usage.InputTokens == 0 {
				t.Fatal("usage not reported")
			}
		})
	}
}

func TestHeuristicsFillGaps(t *testing.T) {
	fx := newFixture(t)
	fx.mock.Responder = func(system, prompt string) string {
		return `{"summary":"s","renames":{"v4":"me"},"comments":[]}`
	}
	e := New(defaultSettings(), map[string]llm.Client{"p": fx.client(t, "p", llm.TypeOpenAI, "sk-test")}, []string{"p"}, nil)
	res, err := e.Clean(context.Background(), sample(t), Options{Comments: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Source, "local me = Players.LocalPlayer") {
		t.Fatalf("AI name + heuristic names expected:\n%s", res.Source)
	}
}

func TestRewriteMode(t *testing.T) {
	fx := newFixture(t)
	e := New(defaultSettings(), map[string]llm.Client{"p": fx.client(t, "p", llm.TypeAnthropic, "sk-test")}, []string{"p"}, nil)
	res, err := e.Clean(context.Background(), sample(t), Options{Mode: ModeRewrite})
	if err != nil {
		t.Fatal(err)
	}
	out := stripHeader(res.Source)
	if !strings.HasPrefix(out, "-- mock rewrite\n") || strings.Contains(out, "```") || strings.Contains(out, "Here is") {
		t.Fatalf("fences not stripped:\n%s", out)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", res.Warnings)
	}
}

func TestFallbackChain(t *testing.T) {
	fx := newFixture(t)
	providers := map[string]llm.Client{
		"bad":  fx.client(t, "bad", llm.TypeAnthropic, "wrong-key"),
		"good": fx.client(t, "good", llm.TypeGemini, "sk-test"),
	}
	e := New(defaultSettings(), providers, []string{"bad", "good", llm.Offline}, NewCache(10, ""))
	res, err := e.Clean(context.Background(), sample(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "good" || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "bad failed") {
		t.Fatalf("fallback: %+v", res)
	}
	// Explicit provider with offline fallback: not cached, so a later retry can use AI.
	res, err = e.Clean(context.Background(), sample(t), Options{Provider: "bad"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != llm.Offline || !strings.Contains(res.Source, "-- Warning: bad failed") {
		t.Fatalf("offline fallback: %+v", res)
	}
	res, _ = e.Clean(context.Background(), sample(t), Options{Provider: "bad"})
	if res.Cached {
		t.Fatal("degraded results must not be cached")
	}
	// Without offline fallback the error surfaces.
	e2 := New(defaultSettings(), providers, []string{"bad"}, nil)
	if _, err := e2.Clean(context.Background(), sample(t), Options{}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error, got %v", err)
	}
	if _, err := e2.Clean(context.Background(), "x()", Options{Provider: "nope"}); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("unknown provider: %v", err)
	}
}

func TestCacheMemoryAndDisk(t *testing.T) {
	fx := newFixture(t)
	dir := t.TempDir()
	c := fx.client(t, "p", llm.TypeOpenAI, "sk-test")
	e := New(defaultSettings(), map[string]llm.Client{"p": c}, []string{"p"}, NewCache(10, dir))
	src := sample(t)
	r1, err := e.Clean(context.Background(), src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := e.Clean(context.Background(), src, Options{})
	if r1.Cached || !r2.Cached || r1.Source != r2.Source || fx.mock.Count() != 1 {
		t.Fatalf("memory cache: cached=%v/%v calls=%d", r1.Cached, r2.Cached, fx.mock.Count())
	}
	// Different options are different entries.
	if r3, _ := e.Clean(context.Background(), src, Options{Comments: "none"}); r3.Cached {
		t.Fatal("comments level must be part of the key")
	}
	// A fresh engine with the same dir hits the disk cache.
	e2 := New(defaultSettings(), map[string]llm.Client{"p": c}, []string{"p"}, NewCache(10, dir))
	r4, _ := e2.Clean(context.Background(), src, Options{})
	if !r4.Cached || r4.Source != r1.Source {
		t.Fatal("disk cache miss")
	}
	// NoCache bypasses it.
	before := fx.mock.Count()
	if r5, _ := e2.Clean(context.Background(), src, Options{NoCache: true}); r5.Cached || fx.mock.Count() != before+1 {
		t.Fatal("no_cache ignored")
	}
}

func TestConcurrentDuplicatesShareOneCall(t *testing.T) {
	fx := newFixture(t)
	release := make(chan struct{})
	fx.mock.Responder = func(system, prompt string) string {
		<-release
		return mockllm.DefaultReply(system, prompt)
	}
	e := New(defaultSettings(), map[string]llm.Client{"p": fx.client(t, "p", llm.TypeOpenAI, "sk-test")}, []string{"p"}, nil)
	var wg sync.WaitGroup
	results := make([]*Result, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := e.Clean(context.Background(), sample(t), Options{})
			if err != nil {
				t.Error(err)
			}
			results[i] = r
		}(i)
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if fx.mock.Count() != 1 {
		t.Fatalf("expected 1 upstream call, got %d", fx.mock.Count())
	}
	for _, r := range results {
		if r == nil || r.Source != results[0].Source {
			t.Fatal("results differ")
		}
	}
}

func TestChunkingMergesResults(t *testing.T) {
	fx := newFixture(t)
	s := defaultSettings()
	s.ChunkChars = 300
	s.Heuristics = false
	e := New(s, map[string]llm.Client{"p": fx.client(t, "p", llm.TypeOpenAI, "sk-test")}, []string{"p"}, nil)
	src := sample(t)
	res, err := e.Clean(context.Background(), src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if fx.mock.Count() < 3 {
		t.Fatalf("expected several chunks, got %d calls", fx.mock.Count())
	}
	out := stripHeader(res.Source)
	for _, name := range []string{"mockV1", "mockV9", "mockU10", "mockV16"} {
		if !strings.Contains(out, name) {
			t.Fatalf("missing %s from a later chunk:\n%s", name, out)
		}
	}
	// One comment per chunk, at each chunk's first line.
	if res.Comments < 3 {
		t.Fatalf("comments from chunks not merged: %d", res.Comments)
	}
	clean := strings.ReplaceAll(out, "-- mock comment\n", "")
	clean = strings.ReplaceAll(clean, "\t-- mock comment\n", "")
	assertSameBehavior(t, src, clean)
}

func TestGarbageReplyRetriesThenFallsBack(t *testing.T) {
	fx := newFixture(t)
	c, _ := llm.New(llm.Config{Name: "p", Type: llm.TypeOpenAI, BaseURL: fx.url + "/v1", APIKey: "sk-test", Model: "mock-garbage"}, nil)
	e := New(defaultSettings(), map[string]llm.Client{"p": c}, []string{"p", llm.Offline}, nil)
	res, err := e.Clean(context.Background(), sample(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if fx.mock.Count() != 2 || res.Provider != llm.Offline || !strings.Contains(res.Warnings[0], "not") {
		t.Fatalf("calls=%d result=%+v", fx.mock.Count(), res)
	}
}

func TestUnparsableInputUsesRewrite(t *testing.T) {
	fx := newFixture(t)
	e := New(defaultSettings(), map[string]llm.Client{"p": fx.client(t, "p", llm.TypeOpenAI, "sk-test")}, []string{"p"}, nil)
	res, err := e.Clean(context.Background(), "local x = = 1\n", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeRewrite || len(res.Warnings) == 0 {
		t.Fatalf("%+v", res)
	}
	off := New(defaultSettings(), nil, nil, nil)
	res, _ = off.Clean(context.Background(), "local x = = 1\n", Options{Header: new(bool)})
	if res.Source != "local x = = 1\n" {
		t.Fatalf("offline must return unparsable input unchanged: %q", res.Source)
	}
}

func TestEmptyInputAndBadOptions(t *testing.T) {
	e := New(defaultSettings(), nil, nil, nil)
	res, err := e.Clean(context.Background(), "  \n", Options{})
	if err != nil || res.Source != "  \n" {
		t.Fatalf("%v %+v", err, res)
	}
	if _, err := e.Clean(context.Background(), "x()", Options{Mode: "bogus"}); err == nil {
		t.Fatal("bad mode accepted")
	}
	if _, err := e.Clean(context.Background(), "x()", Options{Comments: "loud"}); err == nil {
		t.Fatal("bad comments accepted")
	}
}

func TestParseRenameReply(t *testing.T) {
	cases := []string{
		"```json\n{\"renames\":{\"v1\":\"a\"}}\n```",
		"Sure! Here you go: {\"renames\": {\"v1\": \"a\"}} Hope that helps {}",
		`{"renames":[{"from":"v1","to":"a"}]}`,
		`{"renames":[{"old":"v1","new":"a"}],"comments":[{"line":3,"text":"x"}]}`,
	}
	for _, c := range cases {
		_, m, err := parseRenameReply(c)
		if err != nil || m["v1"] != "a" {
			t.Errorf("%q: %v %v", c, m, err)
		}
	}
	for _, bad := range []string{"no json here", "{\"renames\": ", `{"renames": 5}`} {
		if _, _, err := parseRenameReply(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestStripFences(t *testing.T) {
	cases := map[string]string{
		"print(1)\n":                                   "print(1)",
		"```lua\nprint(1)\n```":                        "print(1)",
		"Here:\n```\nprint(1)\n```\nDone":              "print(1)",
		"```lua\na()\n```\nand\n```lua\nlonger()\n```": "longer()",
		"```lua\ntruncated(":                           "truncated(",
	}
	for in, want := range cases {
		if got := stripFences(in); got != want {
			t.Errorf("stripFences(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChunks(t *testing.T) {
	src := "local a = 1\nlocal function f()\n\treturn 1\nend\nlocal b = 2\n"
	f, _ := luau.Parse(src)
	lines := strings.Split(src, "\n")
	if got := chunks(lines, f, 1000); len(got) != 1 || got[0] != [2]int{1, len(lines)} {
		t.Fatalf("single chunk: %v", got)
	}
	got := chunks(lines, f, 15)
	// Never split inside the function (lines 2-4).
	for _, r := range got {
		if r[0] == 3 || r[0] == 4 {
			t.Fatalf("split inside function: %v", got)
		}
	}
	if got[0][0] != 1 || got[len(got)-1][1] != len(lines) {
		t.Fatalf("chunks don't cover input: %v", got)
	}
}
