// Package luau implements a small, fast Luau front end: a lexer, a
// scope-resolving parser and a semantics-preserving renamer. It is not a
// full compiler; it understands exactly enough of the language to know which
// identifier refers to which variable.
package luau

import (
	"fmt"
	"strings"
)

// Kind classifies a token.
type Kind uint8

const (
	EOF Kind = iota
	Name
	Keyword
	Number
	String      // "..." '...' [[...]] and `...` without interpolation
	InterpBegin // `text{
	InterpMid   // }text{
	InterpEnd   // }text`
	Op
	Comment
)

func (k Kind) String() string {
	switch k {
	case EOF:
		return "end of file"
	case Name:
		return "name"
	case Keyword:
		return "keyword"
	case Number:
		return "number"
	case String:
		return "string"
	case InterpBegin, InterpMid, InterpEnd:
		return "interpolated string"
	case Op:
		return "symbol"
	case Comment:
		return "comment"
	}
	return "token"
}

// Token is a lexical token. Start and End are byte offsets into the source.
type Token struct {
	Kind  Kind
	Text  string
	Start int
	End   int
	Line  int // 1-based line of Start
}

// SyntaxError reports a lexing or parsing failure.
type SyntaxError struct {
	Line int
	Msg  string
}

func (e *SyntaxError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

var keywords = map[string]bool{
	"and": true, "break": true, "do": true, "else": true, "elseif": true,
	"end": true, "false": true, "for": true, "function": true, "if": true,
	"in": true, "local": true, "nil": true, "not": true, "or": true,
	"repeat": true, "return": true, "then": true, "true": true, "until": true,
	"while": true,
}

// IsKeyword reports whether s is a reserved Luau keyword.
func IsKeyword(s string) bool { return keywords[s] }

var ops3 = []string{"...", "..=", "//="}
var ops2 = []string{"..", "==", "~=", "<=", ">=", "//", "+=", "-=", "*=", "/=", "%=", "^=", "::", "->"}

const ops1 = "+-*/%^#&~|<>=(){}[];:,.?@!"

// Lex splits src into tokens. Comments are included as Comment tokens.
func Lex(src string) ([]Token, error) {
	lx := lexer{src: src, line: 1}
	return lx.run()
}

type lexer struct {
	src    string
	pos    int
	line   int
	toks   []Token
	interp []int // brace depth for each open interpolated string
}

func (lx *lexer) errf(format string, args ...any) error {
	return &SyntaxError{Line: lx.line, Msg: fmt.Sprintf(format, args...)}
}

func (lx *lexer) emit(k Kind, start, line int) {
	lx.toks = append(lx.toks, Token{Kind: k, Text: lx.src[start:lx.pos], Start: start, End: lx.pos, Line: line})
}

// advanceTo moves pos to p, counting newlines.
func (lx *lexer) advanceTo(p int) {
	lx.line += strings.Count(lx.src[lx.pos:p], "\n")
	lx.pos = p
}

func isAlpha(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isAlnum(c byte) bool { return isAlpha(c) || isDigit(c) }

func (lx *lexer) run() ([]Token, error) {
	src := lx.src
	// Skip a UTF-8 BOM and a shebang line.
	if strings.HasPrefix(src, "\xef\xbb\xbf") {
		lx.pos = 3
	}
	if strings.HasPrefix(src[lx.pos:], "#!") {
		for lx.pos < len(src) && src[lx.pos] != '\n' {
			lx.pos++
		}
	}
	for lx.pos < len(src) {
		c := src[lx.pos]
		start, line := lx.pos, lx.line
		switch {
		case c == '\n':
			lx.line++
			lx.pos++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			lx.pos++
		case c == '-' && strings.HasPrefix(src[lx.pos:], "--"):
			lx.pos += 2
			if lvl, ok := longBracket(src, lx.pos); ok {
				end := strings.Index(src[lx.pos:], "]"+strings.Repeat("=", lvl)+"]")
				if end < 0 {
					return nil, lx.errf("unfinished long comment")
				}
				lx.advanceTo(lx.pos + end + lvl + 2)
			} else {
				for lx.pos < len(src) && src[lx.pos] != '\n' {
					lx.pos++
				}
			}
			lx.emit(Comment, start, line)
		case isAlpha(c) || c >= 0x80:
			for lx.pos < len(src) && (isAlnum(src[lx.pos]) || src[lx.pos] >= 0x80) {
				lx.pos++
			}
			k := Name
			if keywords[src[start:lx.pos]] {
				k = Keyword
			}
			lx.emit(k, start, line)
		case isDigit(c) || (c == '.' && lx.pos+1 < len(src) && isDigit(src[lx.pos+1])):
			lx.number()
			lx.emit(Number, start, line)
		case c == '"' || c == '\'':
			if err := lx.quoted(c); err != nil {
				return nil, err
			}
			lx.emit(String, start, line)
		case c == '[':
			if lvl, ok := longBracket(src, lx.pos); ok {
				end := strings.Index(src[lx.pos:], "]"+strings.Repeat("=", lvl)+"]")
				if end < 0 {
					return nil, lx.errf("unfinished long string")
				}
				lx.advanceTo(lx.pos + end + lvl + 2)
				lx.emit(String, start, line)
			} else {
				lx.pos++
				lx.emit(Op, start, line)
			}
		case c == '`':
			lx.pos++
			k, err := lx.interpSegment(true)
			if err != nil {
				return nil, err
			}
			lx.emit(k, start, line)
		case c == '{':
			if n := len(lx.interp); n > 0 {
				lx.interp[n-1]++
			}
			lx.pos++
			lx.emit(Op, start, line)
		case c == '}':
			if n := len(lx.interp); n > 0 && lx.interp[n-1] == 0 {
				lx.interp = lx.interp[:n-1]
				lx.pos++
				k, err := lx.interpSegment(false)
				if err != nil {
					return nil, err
				}
				lx.emit(k, start, line)
				continue
			} else if n > 0 {
				lx.interp[n-1]--
			}
			lx.pos++
			lx.emit(Op, start, line)
		default:
			rest := src[lx.pos:]
			matched := false
			for _, group := range [][]string{ops3, ops2} {
				for _, op := range group {
					if strings.HasPrefix(rest, op) {
						lx.pos += len(op)
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
			if !matched {
				if strings.IndexByte(ops1, c) < 0 {
					return nil, lx.errf("unexpected character %q", c)
				}
				lx.pos++
			}
			lx.emit(Op, start, line)
		}
	}
	if len(lx.interp) > 0 {
		return nil, lx.errf("unfinished interpolated string")
	}
	lx.toks = append(lx.toks, Token{Kind: EOF, Start: len(src), End: len(src), Line: lx.line})
	return lx.toks, nil
}

// longBracket reports whether src[p:] starts a long bracket ([[ or [==[)
// and returns its level.
func longBracket(src string, p int) (int, bool) {
	if p >= len(src) || src[p] != '[' {
		return 0, false
	}
	q := p + 1
	for q < len(src) && src[q] == '=' {
		q++
	}
	if q < len(src) && src[q] == '[' {
		return q - p - 1, true
	}
	return 0, false
}

func (lx *lexer) number() {
	src := lx.src
	hex := strings.HasPrefix(src[lx.pos:], "0x") || strings.HasPrefix(src[lx.pos:], "0X")
	if hex {
		lx.pos += 2
	}
	for lx.pos < len(src) {
		c := src[lx.pos]
		if isAlnum(c) || c == '.' {
			// Stop before a ".." concatenation operator.
			if c == '.' && lx.pos+1 < len(src) && src[lx.pos+1] == '.' {
				return
			}
			lx.pos++
			exp := (!hex && (c == 'e' || c == 'E')) || (hex && (c == 'p' || c == 'P'))
			if exp && lx.pos < len(src) && (src[lx.pos] == '+' || src[lx.pos] == '-') {
				lx.pos++
			}
			continue
		}
		return
	}
}

func (lx *lexer) quoted(q byte) error {
	src := lx.src
	lx.pos++
	for lx.pos < len(src) {
		c := src[lx.pos]
		switch c {
		case '\\':
			lx.pos++
			if lx.pos < len(src) {
				if src[lx.pos] == '\n' {
					lx.line++
				} else if src[lx.pos] == 'z' {
					// \z skips following whitespace, including newlines.
					lx.pos++
					for lx.pos < len(src) && strings.IndexByte(" \t\r\n\f\v", src[lx.pos]) >= 0 {
						if src[lx.pos] == '\n' {
							lx.line++
						}
						lx.pos++
					}
					continue
				}
				lx.pos++
			}
		case '\n':
			return lx.errf("unfinished string")
		default:
			lx.pos++
			if c == q {
				return nil
			}
		}
	}
	return lx.errf("unfinished string")
}

// interpSegment scans the body of an interpolated string starting at pos
// (just after ` or }) until the closing ` or an opening {.
func (lx *lexer) interpSegment(first bool) (Kind, error) {
	src := lx.src
	for lx.pos < len(src) {
		c := src[lx.pos]
		switch c {
		case '\\':
			lx.pos++
			if lx.pos < len(src) {
				if src[lx.pos] == '\n' {
					lx.line++
				}
				lx.pos++
			}
		case '\n':
			return 0, lx.errf("unfinished interpolated string")
		case '`':
			lx.pos++
			if first {
				return String, nil
			}
			return InterpEnd, nil
		case '{':
			lx.pos++
			lx.interp = append(lx.interp, 0)
			if first {
				return InterpBegin, nil
			}
			return InterpMid, nil
		default:
			lx.pos++
		}
	}
	return 0, lx.errf("unfinished interpolated string")
}
