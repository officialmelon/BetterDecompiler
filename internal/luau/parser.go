package luau

import "fmt"

// BindKind describes how a variable was introduced.
type BindKind uint8

const (
	BindLocal     BindKind = iota // local x = ...
	BindLocalFunc                 // local function x() end
	BindParam                     // function(x) end
	BindSelf                      // implicit self of a method
	BindFor                       // for k, v in ... do
	BindForNum                    // for i = 1, n do
)

// Binding is one variable: a declaration plus every reference to it.
type Binding struct {
	ID   int
	Name string
	Kind BindKind
	Decl int   // index into File.Toks of the declaring name, -1 if implicit
	Refs []int // indices into File.Toks of every occurrence, including Decl

	// Hints used for name inference.
	Init       [2]int // initializer expression token range [start, end), or {-1, -1}
	Event      string // for callback parameters: the event that was connected (e.g. "Touched")
	ParamIndex int    // position of a parameter in its function's parameter list
	Iter       [2]int // generic for: the iterator expression list token range
	IterPos    int    // generic for: position of this name in the name list
}

// Statement is a top-level statement location.
type Statement struct {
	Start int // byte offset
	Line  int
}

// File is a parsed and scope-resolved Luau source file.
type File struct {
	Src      string
	Toks     []Token // significant tokens (no comments), terminated by EOF
	Comments []Token
	Bindings []*Binding
	// Resolve maps each token to the binding it refers to: a binding ID,
	// Global (-1) for a free/global variable, or NotVar (-2) for anything else.
	Resolve    []int
	Statements []Statement
}

const (
	Global = -1
	NotVar = -2
)

// Parse lexes, parses and resolves src.
func Parse(src string) (*File, error) {
	all, err := Lex(src)
	if err != nil {
		return nil, err
	}
	f := &File{Src: src}
	f.Toks = make([]Token, 0, len(all))
	for _, t := range all {
		if t.Kind == Comment {
			f.Comments = append(f.Comments, t)
		} else {
			f.Toks = append(f.Toks, t)
		}
	}
	names := make([]string, len(f.Toks))
	for i, t := range f.Toks {
		names[i] = t.Text
	}
	p, err := analyze(f.Toks, names)
	if err != nil {
		return nil, err
	}
	f.Bindings, f.Resolve, f.Statements = p.bindings, p.resolve, p.stmts
	return f, nil
}

type parser struct {
	toks     []Token
	names    []string // effective name of each token (allows trial renames)
	pos      int
	scopes   []map[string]int
	bindings []*Binding
	resolve  []int
	stmts    []Statement
	depth    int

	eventPos  int    // token position where a callback function may start
	event     string // event name for that callback
	lastField string // last .Field seen by suffixedExp
}

func analyze(toks []Token, names []string) (*parser, error) {
	p := &parser{toks: toks, names: names, eventPos: -1}
	p.resolve = make([]int, len(toks))
	for i := range p.resolve {
		p.resolve[i] = NotVar
	}
	p.push()
	if err := p.block(); err != nil {
		return nil, err
	}
	if p.cur().Kind != EOF {
		return nil, p.errf("unexpected %s", p.desc(p.cur()))
	}
	return p, nil
}

// ---- token helpers ----

func (p *parser) cur() Token { return p.toks[p.pos] }

func (p *parser) peek(n int) Token {
	if p.pos+n < len(p.toks) {
		return p.toks[p.pos+n]
	}
	return p.toks[len(p.toks)-1]
}

func (p *parser) isOp(s string) bool {
	t := p.toks[p.pos]
	return t.Kind == Op && t.Text == s
}

func (p *parser) isKw(s string) bool {
	t := p.toks[p.pos]
	return t.Kind == Keyword && t.Text == s
}

func (p *parser) desc(t Token) string {
	if t.Kind == EOF {
		return "end of file"
	}
	return fmt.Sprintf("%q", t.Text)
}

func (p *parser) errf(format string, args ...any) error {
	return &SyntaxError{Line: p.cur().Line, Msg: fmt.Sprintf(format, args...)}
}

func (p *parser) expectOp(s string) error {
	if !p.isOp(s) {
		return p.errf("expected %q near %s", s, p.desc(p.cur()))
	}
	p.pos++
	return nil
}

func (p *parser) expectKw(s string) error {
	if !p.isKw(s) {
		return p.errf("expected %q near %s", s, p.desc(p.cur()))
	}
	p.pos++
	return nil
}

func (p *parser) expectName() (int, error) {
	if p.cur().Kind != Name {
		return 0, p.errf("expected a name near %s", p.desc(p.cur()))
	}
	p.pos++
	return p.pos - 1, nil
}

// ---- scopes ----

func (p *parser) push() { p.scopes = append(p.scopes, map[string]int{}) }
func (p *parser) pop()  { p.scopes = p.scopes[:len(p.scopes)-1] }

func (p *parser) declare(tok int, kind BindKind) *Binding {
	b := &Binding{ID: len(p.bindings), Name: p.names[tok], Kind: kind, Decl: tok, Refs: []int{tok},
		Init: [2]int{-1, -1}, Iter: [2]int{-1, -1}}
	p.bindings = append(p.bindings, b)
	p.scopes[len(p.scopes)-1][b.Name] = b.ID
	p.resolve[tok] = b.ID
	return b
}

func (p *parser) declareSelf() {
	b := &Binding{ID: len(p.bindings), Name: "self", Kind: BindSelf, Decl: -1,
		Init: [2]int{-1, -1}, Iter: [2]int{-1, -1}}
	p.bindings = append(p.bindings, b)
	p.scopes[len(p.scopes)-1]["self"] = b.ID
}

func (p *parser) ref(tok int) {
	name := p.names[tok]
	for i := len(p.scopes) - 1; i >= 0; i-- {
		if id, ok := p.scopes[i][name]; ok {
			p.resolve[tok] = id
			p.bindings[id].Refs = append(p.bindings[id].Refs, tok)
			return
		}
	}
	p.resolve[tok] = Global
}

// ---- statements ----

func (p *parser) blockEnd() bool {
	t := p.cur()
	if t.Kind == EOF {
		return true
	}
	if t.Kind == Keyword {
		switch t.Text {
		case "end", "else", "elseif", "until":
			return true
		}
	}
	return false
}

func (p *parser) block() error {
	for !p.blockEnd() {
		if p.depth == 0 {
			t := p.cur()
			p.stmts = append(p.stmts, Statement{Start: t.Start, Line: t.Line})
		}
		if err := p.statement(); err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) scopedBlock() error {
	p.push()
	p.depth++
	err := p.block()
	p.depth--
	p.pop()
	return err
}

func (p *parser) statement() error {
	t := p.cur()
	switch t.Kind {
	case Keyword:
		switch t.Text {
		case "if":
			return p.ifStat()
		case "while":
			p.pos++
			if err := p.expr(); err != nil {
				return err
			}
			if err := p.expectKw("do"); err != nil {
				return err
			}
			if err := p.scopedBlock(); err != nil {
				return err
			}
			return p.expectKw("end")
		case "do":
			p.pos++
			if err := p.scopedBlock(); err != nil {
				return err
			}
			return p.expectKw("end")
		case "for":
			return p.forStat()
		case "repeat":
			p.pos++
			p.push()
			p.depth++
			defer func() { p.depth--; p.pop() }()
			if err := p.block(); err != nil {
				return err
			}
			if err := p.expectKw("until"); err != nil {
				return err
			}
			return p.expr() // the condition sees the loop body's locals
		case "function":
			return p.funcStat()
		case "local":
			return p.localStat()
		case "return":
			p.pos++
			if !p.blockEnd() && !p.isOp(";") {
				if _, err := p.exprList(); err != nil {
					return err
				}
			}
			if p.isOp(";") {
				p.pos++
			}
			return nil
		case "break":
			p.pos++
			return nil
		}
	case Op:
		switch t.Text {
		case ";":
			p.pos++
			return nil
		case "@": // attribute such as @native
			p.pos++
			if p.isOp("[") {
				return p.skipBalanced("[", "]")
			}
			_, err := p.expectName()
			return err
		case "::": // ::label:: (Lua 5.2 syntax some decompilers emit)
			p.pos++
			if _, err := p.expectName(); err != nil {
				return err
			}
			return p.expectOp("::")
		}
	case Name:
		next := p.peek(1)
		switch t.Text {
		case "continue":
			if !continuesExpr(next) {
				p.pos++
				return nil
			}
		case "type":
			if next.Kind == Name {
				return p.typeAlias()
			}
		case "export":
			if next.Kind == Name && next.Text == "type" && p.peek(2).Kind == Name {
				p.pos++
				return p.typeAlias()
			}
		case "goto":
			if next.Kind == Name && p.peek(1).Line == t.Line && !continuesExpr(p.peek(2)) {
				p.pos += 2
				return nil
			}
		}
	}
	return p.exprStat()
}

// continuesExpr reports whether t can follow a name inside an expression or
// assignment, which distinguishes `continue` the statement from a variable.
func continuesExpr(t Token) bool {
	switch t.Kind {
	case String, InterpBegin:
		return true
	case Op:
		switch t.Text {
		case "(", ".", ":", "[", "=", ",", "{", "+=", "-=", "*=", "/=", "//=", "%=", "^=", "..=":
			return true
		}
	}
	return false
}

func (p *parser) ifStat() error {
	p.pos++
	if err := p.expr(); err != nil {
		return err
	}
	if err := p.expectKw("then"); err != nil {
		return err
	}
	if err := p.scopedBlock(); err != nil {
		return err
	}
	for p.isKw("elseif") {
		p.pos++
		if err := p.expr(); err != nil {
			return err
		}
		if err := p.expectKw("then"); err != nil {
			return err
		}
		if err := p.scopedBlock(); err != nil {
			return err
		}
	}
	if p.isKw("else") {
		p.pos++
		if err := p.scopedBlock(); err != nil {
			return err
		}
	}
	return p.expectKw("end")
}

func (p *parser) forStat() error {
	p.pos++
	first, err := p.expectName()
	if err != nil {
		return err
	}
	if p.isOp(":") {
		p.pos++
		if err := p.skipType(); err != nil {
			return err
		}
	}
	if p.isOp("=") {
		p.pos++
		if _, err := p.exprList(); err != nil {
			return err
		}
		if err := p.expectKw("do"); err != nil {
			return err
		}
		p.push()
		p.depth++
		p.declare(first, BindForNum)
		err := p.block()
		p.depth--
		p.pop()
		if err != nil {
			return err
		}
		return p.expectKw("end")
	}
	names := []int{first}
	for p.isOp(",") {
		p.pos++
		n, err := p.expectName()
		if err != nil {
			return err
		}
		if p.isOp(":") {
			p.pos++
			if err := p.skipType(); err != nil {
				return err
			}
		}
		names = append(names, n)
	}
	if err := p.expectKw("in"); err != nil {
		return err
	}
	ranges, err := p.exprList()
	if err != nil {
		return err
	}
	if err := p.expectKw("do"); err != nil {
		return err
	}
	p.push()
	p.depth++
	for k, n := range names {
		b := p.declare(n, BindFor)
		b.Iter = [2]int{ranges[0][0], ranges[len(ranges)-1][1]}
		b.IterPos = k
	}
	err = p.block()
	p.depth--
	p.pop()
	if err != nil {
		return err
	}
	return p.expectKw("end")
}

func (p *parser) funcStat() error {
	p.pos++
	first, err := p.expectName()
	if err != nil {
		return err
	}
	p.ref(first)
	method := false
	for p.isOp(".") || p.isOp(":") {
		colon := p.isOp(":")
		p.pos++
		if _, err := p.expectName(); err != nil {
			return err
		}
		if colon {
			method = true
			break
		}
	}
	return p.funcBody(method, "")
}

func (p *parser) localStat() error {
	p.pos++
	if p.isKw("function") {
		p.pos++
		n, err := p.expectName()
		if err != nil {
			return err
		}
		p.declare(n, BindLocalFunc) // visible inside its own body (recursion)
		return p.funcBody(false, "")
	}
	var names []int
	for {
		n, err := p.expectName()
		if err != nil {
			return err
		}
		names = append(names, n)
		if p.isOp(":") {
			p.pos++
			if err := p.skipType(); err != nil {
				return err
			}
		}
		// Lua 5.4 attributes: <const>, <close>
		if p.isOp("<") && p.peek(1).Kind == Name && p.peek(2).Kind == Op && p.peek(2).Text == ">" {
			p.pos += 3
		}
		if !p.isOp(",") {
			break
		}
		p.pos++
	}
	var ranges [][2]int
	if p.isOp("=") {
		p.pos++
		var err error
		if ranges, err = p.exprList(); err != nil {
			return err
		}
	}
	// Locals become visible only after the whole statement.
	for k, n := range names {
		b := p.declare(n, BindLocal)
		if k < len(ranges) {
			b.Init = ranges[k]
		}
	}
	return nil
}

func (p *parser) typeAlias() error {
	p.pos++ // type
	if _, err := p.expectName(); err != nil {
		return err
	}
	if p.isOp("<") {
		if err := p.skipBalanced("<", ">"); err != nil {
			return err
		}
	}
	if err := p.expectOp("="); err != nil {
		return err
	}
	return p.skipType()
}

var invokeCallbacks = map[string]bool{"OnServerInvoke": true, "OnClientInvoke": true, "OnInvoke": true}

func (p *parser) exprStat() error {
	if err := p.suffixedExp(); err != nil {
		return err
	}
	if p.isOp(",") || p.isOp("=") {
		for p.isOp(",") {
			p.pos++
			if err := p.suffixedExp(); err != nil {
				return err
			}
		}
		field := p.lastField
		if err := p.expectOp("="); err != nil {
			return err
		}
		if invokeCallbacks[field] && p.isKw("function") {
			p.eventPos, p.event = p.pos, field
		}
		_, err := p.exprList()
		return err
	}
	switch t := p.cur(); {
	case t.Kind == Op && len(t.Text) >= 2 && t.Text[len(t.Text)-1] == '=' && t.Text != "==" && t.Text != "~=" && t.Text != "<=" && t.Text != ">=":
		p.pos++ // compound assignment
		return p.expr()
	}
	return nil
}

// ---- functions ----

func (p *parser) funcBody(method bool, event string) error {
	p.push()
	p.depth++
	defer func() { p.depth--; p.pop() }()
	if method {
		p.declareSelf()
	}
	if p.isOp("<") {
		if err := p.skipBalanced("<", ">"); err != nil {
			return err
		}
	}
	if err := p.expectOp("("); err != nil {
		return err
	}
	idx := 0
	for !p.isOp(")") {
		if p.isOp("...") {
			p.pos++
		} else {
			n, err := p.expectName()
			if err != nil {
				return err
			}
			b := p.declare(n, BindParam)
			b.Event, b.ParamIndex = event, idx
			idx++
		}
		if p.isOp(":") {
			p.pos++
			if err := p.skipType(); err != nil {
				return err
			}
		}
		if !p.isOp(",") {
			break
		}
		p.pos++
	}
	if err := p.expectOp(")"); err != nil {
		return err
	}
	if p.isOp(":") {
		p.pos++
		if err := p.skipType(); err != nil {
			return err
		}
	}
	if err := p.block(); err != nil {
		return err
	}
	return p.expectKw("end")
}

// ---- expressions ----

func (p *parser) exprList() ([][2]int, error) {
	var ranges [][2]int
	for {
		start := p.pos
		if err := p.expr(); err != nil {
			return nil, err
		}
		ranges = append(ranges, [2]int{start, p.pos})
		if !p.isOp(",") {
			return ranges, nil
		}
		p.pos++
	}
}

func isBinop(t Token) bool {
	switch t.Kind {
	case Keyword:
		return t.Text == "and" || t.Text == "or"
	case Op:
		switch t.Text {
		case "+", "-", "*", "/", "//", "%", "^", "..", "==", "~=", "<", "<=", ">", ">=":
			return true
		}
	}
	return false
}

func (p *parser) expr() error {
	if err := p.unary(); err != nil {
		return err
	}
	for isBinop(p.cur()) {
		p.pos++
		if err := p.unary(); err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) unary() error {
	for p.isKw("not") || p.isOp("-") || p.isOp("#") {
		p.pos++
	}
	if err := p.simpleExp(); err != nil {
		return err
	}
	for p.isOp("::") {
		p.pos++
		if err := p.skipType(); err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) simpleExp() error {
	t := p.cur()
	switch t.Kind {
	case Number, String:
		p.pos++
		return nil
	case InterpBegin:
		p.pos++
		for {
			if err := p.expr(); err != nil {
				return err
			}
			switch p.cur().Kind {
			case InterpMid:
				p.pos++
			case InterpEnd:
				p.pos++
				return nil
			default:
				return p.errf("malformed interpolated string")
			}
		}
	case Keyword:
		switch t.Text {
		case "nil", "true", "false":
			p.pos++
			return nil
		case "function":
			event := ""
			if p.eventPos == p.pos {
				event = p.event
			}
			p.pos++
			return p.funcBody(false, event)
		case "if":
			return p.ifExp()
		}
	case Op:
		switch t.Text {
		case "...":
			p.pos++
			return nil
		case "{":
			return p.table()
		}
	}
	return p.suffixedExp()
}

func (p *parser) ifExp() error {
	p.pos++
	if err := p.expr(); err != nil {
		return err
	}
	if err := p.expectKw("then"); err != nil {
		return err
	}
	if err := p.expr(); err != nil {
		return err
	}
	for p.isKw("elseif") {
		p.pos++
		if err := p.expr(); err != nil {
			return err
		}
		if err := p.expectKw("then"); err != nil {
			return err
		}
		if err := p.expr(); err != nil {
			return err
		}
	}
	if err := p.expectKw("else"); err != nil {
		return err
	}
	return p.expr()
}

var connectMethods = map[string]bool{"Connect": true, "Once": true, "ConnectParallel": true}

func (p *parser) suffixedExp() error {
	t := p.cur()
	switch {
	case t.Kind == Name:
		p.ref(p.pos)
		p.pos++
	case t.Kind == Op && t.Text == "(":
		p.pos++
		if err := p.expr(); err != nil {
			return err
		}
		if err := p.expectOp(")"); err != nil {
			return err
		}
	default:
		return p.errf("unexpected %s", p.desc(t))
	}
	last := ""
	for {
		t := p.cur()
		switch {
		case t.Kind == Op && t.Text == ".":
			p.pos++
			n, err := p.expectName()
			if err != nil {
				return err
			}
			last = p.toks[n].Text
		case t.Kind == Op && t.Text == "[":
			p.pos++
			if err := p.expr(); err != nil {
				return err
			}
			if err := p.expectOp("]"); err != nil {
				return err
			}
			last = ""
		case t.Kind == Op && t.Text == ":":
			p.pos++
			m, err := p.expectName()
			if err != nil {
				return err
			}
			event := ""
			if connectMethods[p.toks[m].Text] {
				event = last
			}
			if err := p.callArgs(event); err != nil {
				return err
			}
			last = ""
		case t.Kind == Op && (t.Text == "(" || t.Text == "{"), t.Kind == String:
			if err := p.callArgs(""); err != nil {
				return err
			}
			last = ""
		default:
			p.lastField = last
			return nil
		}
	}
}

func (p *parser) callArgs(event string) error {
	t := p.cur()
	switch {
	case t.Kind == String:
		p.pos++
		return nil
	case t.Kind == Op && t.Text == "{":
		return p.table()
	case t.Kind == Op && t.Text == "(":
		p.pos++
		if p.isOp(")") {
			p.pos++
			return nil
		}
		if event != "" {
			p.eventPos, p.event = p.pos, event
		}
		if _, err := p.exprList(); err != nil {
			return err
		}
		return p.expectOp(")")
	}
	return p.errf("expected function arguments near %s", p.desc(t))
}

func (p *parser) table() error {
	p.pos++ // {
	for !p.isOp("}") {
		switch {
		case p.isOp("["):
			p.pos++
			if err := p.expr(); err != nil {
				return err
			}
			if err := p.expectOp("]"); err != nil {
				return err
			}
			if err := p.expectOp("="); err != nil {
				return err
			}
			if err := p.expr(); err != nil {
				return err
			}
		case p.cur().Kind == Name && p.peek(1).Kind == Op && p.peek(1).Text == "=":
			p.pos += 2 // field key, not a variable
			if err := p.expr(); err != nil {
				return err
			}
		default:
			if err := p.expr(); err != nil {
				return err
			}
		}
		if !p.isOp(",") && !p.isOp(";") {
			break
		}
		p.pos++
	}
	return p.expectOp("}")
}

// ---- types (skipped, except typeof expressions which reference variables) ----

func (p *parser) skipType() error {
	if p.isOp("|") || p.isOp("&") {
		p.pos++
	}
	if err := p.simpleType(); err != nil {
		return err
	}
	for {
		switch {
		case p.isOp("?"):
			p.pos++
		case p.isOp("|") || p.isOp("&"):
			p.pos++
			if err := p.simpleType(); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

func (p *parser) simpleType() error {
	t := p.cur()
	switch {
	case t.Kind == Name && t.Text == "typeof" && p.peek(1).Kind == Op && p.peek(1).Text == "(":
		p.pos += 2
		if err := p.expr(); err != nil {
			return err
		}
		return p.expectOp(")")
	case t.Kind == Name:
		p.pos++
		if p.isOp(".") {
			p.pos++
			if _, err := p.expectName(); err != nil {
				return err
			}
		}
		if p.isOp("<") {
			return p.skipBalanced("<", ">")
		}
		return nil
	case t.Kind == String:
		p.pos++
		return nil
	case t.Kind == Keyword && (t.Text == "nil" || t.Text == "true" || t.Text == "false"):
		p.pos++
		return nil
	case t.Kind == Op && t.Text == "{":
		return p.skipBalanced("{", "}")
	case t.Kind == Op && t.Text == "(":
		if err := p.skipBalanced("(", ")"); err != nil {
			return err
		}
		if p.isOp("->") {
			p.pos++
			return p.skipType()
		}
		return nil
	case t.Kind == Op && t.Text == "<": // generic function type <T>(T) -> T
		if err := p.skipBalanced("<", ">"); err != nil {
			return err
		}
		return p.simpleType()
	case t.Kind == Op && t.Text == "...":
		p.pos++
		if p.cur().Kind == Name || p.isOp("(") || p.isOp("{") {
			return p.simpleType()
		}
		return nil
	}
	return p.errf("unexpected %s in type", p.desc(t))
}

func (p *parser) skipBalanced(open, close string) error {
	if err := p.expectOp(open); err != nil {
		return err
	}
	depth := 1
	for depth > 0 {
		t := p.cur()
		if t.Kind == EOF {
			return p.errf("unclosed %q", open)
		}
		if t.Kind == Op {
			switch t.Text {
			case open:
				depth++
			case close:
				depth--
			}
		}
		p.pos++
	}
	return nil
}
