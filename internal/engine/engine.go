// Package engine turns decompiled Luau into readable Luau.
//
// Three modes are supported:
//
//   - rename (default): the AI only proposes names and comments as compact
//     JSON; they are applied locally by a scope-aware renamer that verifies
//     every identifier still refers to the same variable. Fast, cheap (tiny
//     outputs), works on huge scripts, and can never change behavior.
//   - rewrite: the AI rewrites the code (also restructuring it). Slower and
//     can hallucinate, but produces the most idiomatic result.
//   - offline: no AI at all; names are inferred from Roblox API usage.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/officialmelon/betterdecompiler/internal/llm"
	"github.com/officialmelon/betterdecompiler/internal/luau"
)

// Modes.
const (
	ModeRename  = "rename"
	ModeRewrite = "rewrite"
	ModeOffline = "offline"
)

// Settings are engine-wide defaults.
type Settings struct {
	Mode              string // default mode
	Comments          string // none | light | detailed
	Header            bool   // prepend an informational header comment
	Heuristics        bool   // fill names the AI skipped with offline inference
	ChunkChars        int    // max source characters per AI request in rename mode
	RewriteChunkChars int    // max source characters per AI request in rewrite mode
	Concurrency       int    // max concurrent AI requests
	Version           string // shown in the header
}

// Options customise a single request. Zero values use the Settings.
type Options struct {
	Mode     string `json:"mode,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Comments string `json:"comments,omitempty"`
	Header   *bool  `json:"header,omitempty"`
	NoCache  bool   `json:"no_cache,omitempty"`
}

// Result is the outcome of a cleanup.
type Result struct {
	Source    string    `json:"source"`
	Mode      string    `json:"mode"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Renamed   int       `json:"renamed"`
	Comments  int       `json:"comments"`
	Usage     llm.Usage `json:"usage"`
	Warnings  []string  `json:"warnings,omitempty"`
	Cached    bool      `json:"cached"`
	ElapsedMS int64     `json:"elapsed_ms"`

	cacheable bool
}

// Stats are cumulative engine counters.
type Stats struct {
	Requests     int64 `json:"requests"`
	CacheHits    int64 `json:"cache_hits"`
	AICalls      int64 `json:"ai_calls"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// Engine cleans scripts. It is safe for concurrent use.
type Engine struct {
	s         Settings
	providers map[string]llm.Client
	chain     []string
	cache     *Cache
	sem       chan struct{}

	mu     sync.Mutex
	flight map[string]*call

	requests, cacheHits, aiCalls, inTok, outTok atomic.Int64
}

type call struct {
	done chan struct{}
	res  *Result
	err  error
}

// ErrUnknownProvider is returned for requests naming an unconfigured provider.
var ErrUnknownProvider = errors.New("unknown or unconfigured provider")

// New creates an engine. chain is the default provider order; it may contain
// llm.Offline. providers maps names to clients.
func New(s Settings, providers map[string]llm.Client, chain []string, cache *Cache) *Engine {
	if s.Mode == "" {
		s.Mode = ModeRename
	}
	if _, ok := commentRules[s.Comments]; !ok {
		s.Comments = "light"
	}
	if s.ChunkChars <= 0 {
		s.ChunkChars = 120_000
	}
	if s.RewriteChunkChars <= 0 {
		s.RewriteChunkChars = 24_000
	}
	if s.Concurrency <= 0 {
		s.Concurrency = 4
	}
	if len(chain) == 0 {
		chain = []string{llm.Offline}
	}
	return &Engine{s: s, providers: providers, chain: chain, cache: cache,
		sem: make(chan struct{}, s.Concurrency), flight: map[string]*call{}}
}

// Chain returns the default provider order.
func (e *Engine) Chain() []string { return append([]string(nil), e.chain...) }

// Settings returns the engine settings.
func (e *Engine) Settings() Settings { return e.s }

// Provider returns a configured provider client.
func (e *Engine) Provider(name string) (llm.Client, bool) {
	c, ok := e.providers[name]
	return c, ok
}

// Stats returns cumulative counters.
func (e *Engine) Stats() Stats {
	return Stats{Requests: e.requests.Load(), CacheHits: e.cacheHits.Load(), AICalls: e.aiCalls.Load(),
		InputTokens: e.inTok.Load(), OutputTokens: e.outTok.Load()}
}

func (e *Engine) normalize(o Options) (Options, []string, error) {
	if o.Mode == "" {
		o.Mode = e.s.Mode
	}
	switch o.Mode {
	case ModeRename, ModeRewrite, ModeOffline:
	default:
		return o, nil, fmt.Errorf("unknown mode %q (want rename, rewrite or offline)", o.Mode)
	}
	if o.Comments == "" {
		o.Comments = e.s.Comments
	}
	if _, ok := commentRules[o.Comments]; !ok {
		return o, nil, fmt.Errorf("unknown comments level %q (want none, light or detailed)", o.Comments)
	}
	if o.Header == nil {
		h := e.s.Header
		o.Header = &h
	}
	if o.Mode == ModeOffline || o.Provider == llm.Offline {
		o.Mode, o.Provider = ModeOffline, llm.Offline
		return o, []string{llm.Offline}, nil
	}
	chain := e.chain
	if o.Provider != "" {
		if _, ok := e.providers[o.Provider]; !ok {
			return o, nil, fmt.Errorf("%w: %q", ErrUnknownProvider, o.Provider)
		}
		chain = []string{o.Provider}
		for _, n := range e.chain {
			if n == llm.Offline {
				chain = append(chain, n)
			}
		}
	}
	if len(chain) == 1 && chain[0] == llm.Offline {
		o.Mode = ModeOffline
	}
	return o, chain, nil
}

func (e *Engine) cacheKey(src string, o Options, chain []string) string {
	h := sha256.New()
	model := o.Model
	if model == "" && chain[0] != llm.Offline {
		model = e.providers[chain[0]].Model()
	}
	fmt.Fprintf(h, "v%s\x00%s\x00%s\x00%s\x00%s\x00%t\x00%t\x00", promptVersion, o.Mode, strings.Join(chain, ","), model, o.Comments, *o.Header, e.s.Heuristics)
	h.Write([]byte(src))
	return hex.EncodeToString(h.Sum(nil))
}

// Clean cleans up decompiled source.
func (e *Engine) Clean(ctx context.Context, src string, o Options) (*Result, error) {
	start := time.Now()
	e.requests.Add(1)
	o, chain, err := e.normalize(o)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(src) == "" {
		return &Result{Source: src, Mode: o.Mode, Provider: "none"}, nil
	}
	key := e.cacheKey(src, o, chain)
	if !o.NoCache {
		if r, ok := e.cache.Get(key); ok {
			e.cacheHits.Add(1)
			out := *r
			out.Cached, out.ElapsedMS = true, time.Since(start).Milliseconds()
			return &out, nil
		}
	}
	// Identical concurrent requests share one upstream call.
	e.mu.Lock()
	if c, ok := e.flight[key]; ok {
		e.mu.Unlock()
		select {
		case <-c.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if c.err != nil {
			return nil, c.err
		}
		out := *c.res
		out.ElapsedMS = time.Since(start).Milliseconds()
		return &out, nil
	}
	c := &call{done: make(chan struct{})}
	e.flight[key] = c
	e.mu.Unlock()

	c.res, c.err = e.run(ctx, src, o, chain, start)
	if c.err == nil && c.res.cacheable {
		e.cache.Put(key, c.res)
	}
	e.mu.Lock()
	delete(e.flight, key)
	e.mu.Unlock()
	close(c.done)
	if c.err != nil {
		return nil, c.err
	}
	out := *c.res
	return &out, nil
}

func (e *Engine) run(ctx context.Context, src string, o Options, chain []string, start time.Time) (*Result, error) {
	f, perr := luau.Parse(src)
	mode := o.Mode
	var warnings []string
	if mode == ModeRename && perr != nil {
		warnings = append(warnings, fmt.Sprintf("input could not be parsed (%v), so rewrite mode was used instead of rename mode", perr))
		mode = ModeRewrite
	}
	var errs []string
	for i, name := range chain {
		var res *Result
		var err error
		if name == llm.Offline {
			res = e.offline(f, perr, src)
			res.cacheable = len(errs) == 0
		} else {
			model := ""
			if i == 0 {
				model = o.Model
			}
			client := e.providers[name]
			if mode == ModeRename {
				res, err = e.renameAI(ctx, f, client, model, o)
			} else {
				res, err = e.rewriteAI(ctx, f, src, client, model, o)
			}
			if res != nil {
				res.cacheable = true
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			errs = append(errs, err.Error())
			warnings = append(warnings, fmt.Sprintf("%s failed: %v", name, err))
			continue
		}
		res.Warnings = append(warnings, res.Warnings...)
		res.ElapsedMS = time.Since(start).Milliseconds()
		if *o.Header {
			res.Source = e.header(res) + res.Source
		}
		return res, nil
	}
	return nil, fmt.Errorf("all providers failed: %s", strings.Join(errs, "; "))
}

func (e *Engine) offline(f *luau.File, perr error, src string) *Result {
	res := &Result{Mode: ModeOffline, Provider: llm.Offline}
	if f == nil {
		res.Source = src
		res.Warnings = []string{fmt.Sprintf("input could not be parsed (%v); returned unchanged", perr)}
		return res
	}
	out, applied := f.Rename(f.SuggestNames())
	res.Source, res.Renamed = out, len(applied)
	return res
}

func (e *Engine) complete(ctx context.Context, c llm.Client, req llm.Request) (*llm.Response, error) {
	select {
	case e.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-e.sem }()
	e.aiCalls.Add(1)
	resp, err := c.Complete(ctx, req)
	if err == nil {
		e.inTok.Add(int64(resp.Usage.InputTokens))
		e.outTok.Add(int64(resp.Usage.OutputTokens))
	}
	return resp, err
}

// chunks groups lines 1..n into ranges of at most max characters, breaking
// only before top-level statements.
func chunks(lines []string, f *luau.File, max int) [][2]int {
	n := len(lines)
	if f == nil || len(f.Statements) == 0 {
		return [][2]int{{1, n}}
	}
	breaks := []int{1}
	for _, s := range f.Statements {
		if s.Line > breaks[len(breaks)-1] {
			breaks = append(breaks, s.Line)
		}
	}
	breaks = append(breaks, n+1)
	var out [][2]int
	from, size := 1, 0
	for i := 0; i+1 < len(breaks); i++ {
		seg := 0
		for l := breaks[i]; l < breaks[i+1] && l <= n; l++ {
			seg += len(lines[l-1]) + 1
		}
		if size > 0 && size+seg > max {
			out = append(out, [2]int{from, breaks[i] - 1})
			from, size = breaks[i], 0
		}
		size += seg
	}
	return append(out, [2]int{from, n})
}

// parallel runs fn for each index with bounded concurrency (bounded further
// by the engine semaphore) and returns the first error.
func parallel(ctx context.Context, n int, fn func(ctx context.Context, i int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := fn(ctx, i); err != nil {
				once.Do(func() { first = err; cancel() })
			}
		}(i)
	}
	wg.Wait()
	return first
}

type renameReply struct {
	Summary  string          `json:"summary"`
	Renames  json.RawMessage `json:"renames"`
	Comments []struct {
		Line int    `json:"line"`
		Text string `json:"text"`
	} `json:"comments"`
}

// parseRenameReply extracts the JSON object from a model reply, tolerating
// markdown fences and surrounding prose, and accepting renames either as an
// object or as a list of {from,to} pairs.
func parseRenameReply(text string) (*renameReply, map[string]string, error) {
	i := strings.Index(text, "{")
	if i < 0 {
		return nil, nil, fmt.Errorf("reply contained no JSON object: %q", clip(text))
	}
	var r renameReply
	if err := json.NewDecoder(strings.NewReader(text[i:])).Decode(&r); err != nil {
		return nil, nil, fmt.Errorf("reply was not valid JSON (%v): %q", err, clip(text))
	}
	renames := map[string]string{}
	if len(r.Renames) > 0 && string(r.Renames) != "null" {
		if json.Unmarshal(r.Renames, &renames) != nil {
			var pairs []map[string]string
			if err := json.Unmarshal(r.Renames, &pairs); err != nil {
				return nil, nil, fmt.Errorf("unexpected renames format: %s", clip(string(r.Renames)))
			}
			for _, p := range pairs {
				from := firstOf(p, "from", "old", "name", "original")
				to := firstOf(p, "to", "new", "newName", "renamed")
				if from != "" && to != "" {
					renames[from] = to
				}
			}
		}
	}
	return &r, renames, nil
}

func firstOf(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

func (e *Engine) renameAI(ctx context.Context, f *luau.File, c llm.Client, model string, o Options) (*Result, error) {
	keyed, keys := f.UniqueKeys()
	idByKey := make(map[string]int, len(keys))
	type cand struct {
		key  string
		line int
	}
	var cands []cand
	for id, k := range keys {
		idByKey[k] = id
		cands = append(cands, cand{k, f.Toks[f.Bindings[id].Decl].Line})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].line != cands[j].line {
			return cands[i].line < cands[j].line
		}
		return cands[i].key < cands[j].key
	})
	lines := strings.Split(keyed, "\n")
	ranges := chunks(lines, f, e.s.ChunkChars)

	type part struct {
		reply   *renameReply
		renames map[string]string
		resp    *llm.Response
	}
	parts := make([]*part, len(ranges))
	system := renamePrompt(o.Comments)
	err := parallel(ctx, len(ranges), func(ctx context.Context, i int) error {
		r := ranges[i]
		var names []string
		for _, cd := range cands {
			if cd.line >= r[0] && cd.line <= r[1] {
				names = append(names, cd.key)
			}
		}
		if len(names) == 0 && o.Comments == "none" {
			parts[i] = &part{}
			return nil
		}
		req := llm.Request{System: system, Prompt: renameUserPrompt(names, numbered(lines, r[0], r[1])), Model: model, JSON: true}
		var lastErr error
		for attempt := 0; attempt < 2; attempt++ {
			resp, err := e.complete(ctx, c, req)
			if err != nil {
				return err
			}
			reply, renames, err := parseRenameReply(resp.Text)
			if err == nil {
				parts[i] = &part{reply, renames, resp}
				return nil
			}
			lastErr = err
			req.Prompt += "\nReply with the JSON object only."
		}
		return fmt.Errorf("%s: %w", c.Name(), lastErr)
	})
	if err != nil {
		return nil, err
	}

	res := &Result{Mode: ModeRename, Provider: c.Name(), Model: model}
	if res.Model == "" {
		res.Model = c.Model()
	}
	want := map[int]string{}
	comments := map[int]string{}
	var summaries []string
	for _, p := range parts {
		if p.resp != nil {
			res.Usage.Add(p.resp.Usage)
			if p.resp.Model != "" {
				res.Model = p.resp.Model
			}
		}
		if p.reply == nil {
			continue
		}
		if s := strings.TrimSpace(p.reply.Summary); s != "" {
			summaries = append(summaries, s)
		}
		for k, v := range p.renames {
			id, ok := idByKey[k]
			if !ok {
				continue
			}
			if _, dup := want[id]; !dup {
				want[id] = luau.ToIdent(v)
			}
		}
		for _, cm := range p.reply.Comments {
			if o.Comments != "none" && cm.Line > 0 && strings.TrimSpace(cm.Text) != "" {
				if _, dup := comments[cm.Line]; !dup {
					comments[cm.Line] = cm.Text
				}
			}
		}
	}
	if e.s.Heuristics {
		for id, n := range f.SuggestNames() {
			if _, ok := want[id]; !ok {
				want[id] = n
			}
		}
	}
	// Bindings the AI left alone keep their original names (not their keys).
	out, applied := f.Rename(want)
	out = f.InsertComments(out, comments)
	res.Source, res.Renamed, res.Comments = out, len(applied), len(comments)
	if len(summaries) > 0 {
		res.Summary = summaries[0]
	}
	return res, nil
}

// stripFences extracts code from a reply that may wrap it in markdown.
func stripFences(text string) string {
	if !strings.Contains(text, "```") {
		return strings.Trim(text, "\r\n")
	}
	best := ""
	rest := text
	for {
		i := strings.Index(rest, "```")
		if i < 0 {
			break
		}
		body := rest[i+3:]
		nl := strings.IndexByte(body, '\n')
		if nl < 0 {
			break
		}
		body = body[nl+1:] // skip the language tag line
		j := strings.Index(body, "```")
		if j < 0 {
			if len(body) > len(best) { // unterminated fence (truncated reply)
				best = body
			}
			break
		}
		if len(body[:j]) > len(best) {
			best = body[:j]
		}
		rest = body[j+3:]
	}
	return strings.Trim(best, "\r\n")
}

func (e *Engine) rewriteAI(ctx context.Context, f *luau.File, src string, c llm.Client, model string, o Options) (*Result, error) {
	lines := strings.Split(src, "\n")
	ranges := chunks(lines, f, e.s.RewriteChunkChars)
	outs := make([]*llm.Response, len(ranges))
	system := rewritePrompt(o.Comments)
	err := parallel(ctx, len(ranges), func(ctx context.Context, i int) error {
		code := strings.Join(lines[ranges[i][0]-1:ranges[i][1]], "\n")
		if strings.TrimSpace(code) == "" {
			outs[i] = &llm.Response{Text: code}
			return nil
		}
		resp, err := e.complete(ctx, c, llm.Request{System: system, Prompt: rewriteUserPrompt(code), Model: model})
		if err != nil {
			return err
		}
		resp.Text = stripFences(resp.Text)
		if strings.TrimSpace(resp.Text) == "" {
			return fmt.Errorf("%s returned an empty reply", c.Name())
		}
		outs[i] = resp
		return nil
	})
	if err != nil {
		return nil, err
	}
	res := &Result{Mode: ModeRewrite, Provider: c.Name(), Model: model}
	if res.Model == "" {
		res.Model = c.Model()
	}
	texts := make([]string, len(outs))
	for i, r := range outs {
		texts[i] = r.Text
		res.Usage.Add(r.Usage)
		if r.Model != "" {
			res.Model = r.Model
		}
		if r.Truncated {
			res.Warnings = append(res.Warnings, fmt.Sprintf("part %d of %d hit the output token limit and is incomplete", i+1, len(outs)))
		}
	}
	res.Source = strings.Join(texts, "\n\n") + "\n"
	if _, err := luau.Parse(res.Source); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("the rewritten code does not parse (%v); review it carefully", err))
	}
	return res, nil
}

func (e *Engine) header(r *Result) string {
	var sb strings.Builder
	who := r.Provider
	if r.Model != "" {
		who += "/" + r.Model
	}
	fmt.Fprintf(&sb, "-- Cleaned by BetterDecompiler %s | %s | %s mode | %.2fs\n", e.s.Version, who, r.Mode, float64(r.ElapsedMS)/1000)
	if r.Summary != "" {
		for _, l := range wrap("Summary: "+strings.Join(strings.Fields(r.Summary), " "), 100) {
			sb.WriteString("-- " + l + "\n")
		}
	}
	switch r.Mode {
	case ModeRewrite:
		sb.WriteString("-- Rewritten by AI: it can be wrong, so verify before relying on it.\n")
	case ModeRename:
		sb.WriteString("-- Only names and comments were changed; behavior is identical. AI-chosen names can be wrong.\n")
	}
	for _, w := range r.Warnings {
		sb.WriteString("-- Warning: " + strings.Join(strings.Fields(w), " ") + "\n")
	}
	sb.WriteString("\n")
	return sb.String()
}

func wrap(s string, width int) []string {
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		if cur != "" && len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = ""
		}
		if cur != "" {
			cur += " "
		}
		cur += w
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
