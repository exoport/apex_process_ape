package apexv1

import (
	"bytes"
	"errors"
	"fmt"
	"go/scanner"
	"go/token"
	"path"
	"regexp"
	"strings"
)

// The lift edits comments and nothing else (D37), so it has to know where
// the comments are. Each tokenizer below returns the comment and string
// spans of one file, in order; everything between them is code. A file a
// tokenizer cannot read to the end (an unterminated string, a heredoc with
// no delimiter, a line comment continued by a backslash) is skipped and
// listed, never guessed at.

// liftLang is a language the lift tokenizes.
type liftLang int

const (
	langNone liftLang = iota
	langGo
	langShell
	langPython
	langJS
	// langJSX is JS whose text content can hold `//` (a URL in markup), so
	// only comments that begin their line are edited.
	langJSX
	langDart
	langYAML
	langMake
	langC
)

var liftLangNames = map[liftLang]string{
	langGo: "go", langShell: "shell", langPython: "python", langJS: "js", langJSX: "jsx",
	langDart: "dart", langYAML: "yaml", langMake: "make", langC: "c",
}

func (l liftLang) String() string { return liftLangNames[l] }

var shebangRe = regexp.MustCompile(`^#!\s*\S*/(?:env\s+)?(sh|bash|zsh|dash|ksh|python[0-9.]*)\b`)

// liftLangOf picks the tokenizer for a project-relative path; head is the
// file's first bytes, for a script with no extension.
func liftLangOf(rel string, head []byte) liftLang {
	base := path.Base(rel)
	switch base {
	case "Makefile", "makefile", "GNUmakefile":
		return langMake
	}
	switch strings.ToLower(path.Ext(base)) {
	case ".go":
		return langGo
	case ".sh", ".bash", ".zsh":
		return langShell
	case ".py":
		return langPython
	case ".js", ".mjs", ".cjs", ".ts", ".mts", ".cts":
		return langJS
	case ".jsx", ".tsx":
		return langJSX
	case ".dart":
		return langDart
	case ".yml", ".yaml":
		return langYAML
	case ".mk":
		return langMake
	case ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hh", ".hxx":
		return langC
	case "":
		if m := shebangRe.FindSubmatch(head); m != nil {
			if bytes.HasPrefix(m[1], []byte("python")) {
				return langPython
			}
			return langShell
		}
	}
	return langNone
}

type segKind int

const (
	segComment segKind = iota + 1
	segString
)

// tokSeg is one comment or string literal: src[Start:End].
type tokSeg struct {
	Kind       segKind
	Start, End int
	// Block marks a /* */ comment; a line comment runs to its newline.
	Block bool
}

// blockDelims is the length of a block comment's /* and */.
const blockDelims = len("/*") + len("*/")

// untokenizableError says why a file was skipped.
type untokenizableError struct{ why string }

func (e *untokenizableError) Error() string { return e.why }

func untok(format string, args ...any) error {
	return &untokenizableError{why: fmt.Sprintf(format, args...)}
}

// tokenize returns the comment and string spans of src.
func tokenize(lang liftLang, src string) ([]tokSeg, error) {
	switch lang {
	case langGo:
		segs, _, err := tokenizeGo(src)
		return segs, err
	case langShell:
		return tokenizeShell(src)
	case langPython:
		return tokenizePython(src)
	case langJS, langJSX:
		return tokenizeJS(src)
	case langDart:
		return tokenizeDart(src)
	case langYAML:
		return tokenizeYAML(src)
	case langMake:
		return tokenizeMake(src)
	case langC:
		return tokenizeC(src)
	default:
		return nil, errors.New("no tokenizer")
	}
}

// goIdent is an identifier token of a Go file, for the ids-in-identifiers list.
type goIdent struct {
	Start int
	Name  string
}

// tokenizeGo uses go/scanner: comments, string and char literals, and the
// identifiers. A file the scanner reports an error in is not edited.
func tokenizeGo(src string) ([]tokSeg, []goIdent, error) {
	fset := token.NewFileSet()
	file := fset.AddFile("x.go", -1, len(src))
	var s scanner.Scanner
	var firstErr error
	s.Init(file, []byte(src), func(pos token.Position, msg string) {
		if firstErr == nil {
			firstErr = untok("go/scanner: line %d: %s", pos.Line, msg)
		}
	}, scanner.ScanComments)
	var segs []tokSeg
	var idents []goIdent
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		off := file.Offset(pos)
		// The scanner strips carriage returns from comments and raw
		// strings, so their ends are found in the source, not by length.
		switch tok {
		case token.COMMENT:
			end := lineEnd(src, off)
			if strings.HasPrefix(lit, "/*") {
				end = off + 2 + strings.Index(src[off+2:], "*/") + 2
			}
			segs = append(segs, tokSeg{Kind: segComment, Start: off, End: end, Block: strings.HasPrefix(lit, "/*")})
		case token.STRING, token.CHAR:
			end := off + len(lit)
			if strings.HasPrefix(lit, "`") {
				end = off + 1 + strings.IndexByte(src[off+1:], '`') + 1
			}
			segs = append(segs, tokSeg{Kind: segString, Start: off, End: end})
		case token.IDENT:
			idents = append(idents, goIdent{Start: off, Name: lit})
		default:
			// Operators, keywords and numbers hold no comment, string or
			// identifier.
		}
	}
	if firstErr != nil {
		return nil, nil, firstErr
	}
	return segs, idents, nil
}

// goTokens is a Go file's token stream without comments — what the
// compiler sees, automatic semicolons included.
func goTokens(src string) ([]string, error) {
	fset := token.NewFileSet()
	file := fset.AddFile("x.go", -1, len(src))
	var s scanner.Scanner
	var firstErr error
	s.Init(file, []byte(src), func(pos token.Position, msg string) {
		if firstErr == nil {
			firstErr = fmt.Errorf("line %d: %s", pos.Line, msg)
		}
	}, 0)
	var out []string
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		out = append(out, tok.String()+"\x00"+lit)
	}
	return out, firstErr
}

// lineEnd is the index of the newline ending the line at i, or len(src).
func lineEnd(src string, i int) int {
	if j := strings.IndexByte(src[i:], '\n'); j >= 0 {
		return i + j
	}
	return len(src)
}

// ---- shell ----------------------------------------------------------

type heredoc struct {
	delim     string
	stripTabs bool
}

func shellWordStart(src string, i int) bool {
	if i == 0 {
		return true
	}
	return strings.IndexByte(" \t\n;&|()<>", src[i-1]) >= 0
}

//nolint:gocyclo // a hand-written lexer: one case per token class
func tokenizeShell(src string) ([]tokSeg, error) {
	var out []tokSeg
	var pending []heredoc
	n := len(src)
	i := 0
	for i < n {
		c := src[i]
		switch {
		case c == '\n':
			i++
			for len(pending) > 0 {
				h := pending[0]
				pending = pending[1:]
				start := i
				for {
					if i >= n {
						return nil, untok("heredoc %q has no closing line", h.delim)
					}
					end := lineEnd(src, i)
					line := strings.TrimSuffix(src[i:end], "\r")
					if h.stripTabs {
						line = strings.TrimLeft(line, "\t")
					}
					i = min(end+1, n)
					if line == h.delim {
						break
					}
				}
				out = append(out, tokSeg{Kind: segString, Start: start, End: i})
			}
		case c == '\\':
			i += 2
		case c == '\'':
			j := strings.IndexByte(src[i+1:], '\'')
			if j < 0 {
				return nil, untok("unterminated single-quoted string")
			}
			out = append(out, tokSeg{Kind: segString, Start: i, End: i + j + 2})
			i += j + 2
		case c == '"':
			j, err := shellDQ(src, i+1)
			if err != nil {
				return nil, err
			}
			out = append(out, tokSeg{Kind: segString, Start: i, End: j})
			i = j
		case c == '`':
			j, err := shellEscaped(src, i+1, '`')
			if err != nil {
				return nil, err
			}
			out = append(out, tokSeg{Kind: segString, Start: i, End: j})
			i = j
		case c == '$' && i+1 < n && src[i+1] == '\'':
			j, err := shellEscaped(src, i+2, '\'')
			if err != nil {
				return nil, err
			}
			out = append(out, tokSeg{Kind: segString, Start: i, End: j})
			i = j
		case c == '$' && i+1 < n && src[i+1] == '{':
			j, err := shellBraces(src, i+2)
			if err != nil {
				return nil, err
			}
			i = j
		case c == '$':
			i += 2
		case c == '#' && shellWordStart(src, i):
			end := lineEnd(src, i)
			out = append(out, tokSeg{Kind: segComment, Start: i, End: end})
			i = end
		case strings.HasPrefix(src[i:], "<<<"):
			// A here-string: its word follows on the same line. Skipping
			// only its first < would read the rest as a heredoc (L10).
			i += 3
		case c == '<' && strings.HasPrefix(src[i:], "<<"):
			h, j, ok := shellHeredocOp(src, i+2)
			if ok {
				pending = append(pending, h)
			}
			i = j
		default:
			i++
		}
	}
	if len(pending) > 0 {
		return nil, untok("heredoc %q has no body", pending[0].delim)
	}
	return out, nil
}

// shellEscaped scans to the unescaped quote q; returns the index after it.
func shellEscaped(src string, i int, q byte) (int, error) {
	for i < len(src) {
		switch src[i] {
		case '\\':
			i += 2
			continue
		case q:
			return i + 1, nil
		}
		i++
	}
	return 0, untok("unterminated %c-quoted string", q)
}

// shellDQ scans a double-quoted string from just after its quote,
// following $( ) and ${ } inside it; returns the index after the quote.
func shellDQ(src string, i int) (int, error) {
	for i < len(src) {
		switch {
		case src[i] == '\\':
			i += 2
			continue
		case src[i] == '"':
			return i + 1, nil
		case strings.HasPrefix(src[i:], "$("):
			j, err := shellParens(src, i+2)
			if err != nil {
				return 0, err
			}
			i = j
			continue
		case strings.HasPrefix(src[i:], "${"):
			j, err := shellBraces(src, i+2)
			if err != nil {
				return 0, err
			}
			i = j
			continue
		case src[i] == '`':
			j, err := shellEscaped(src, i+1, '`')
			if err != nil {
				return 0, err
			}
			i = j
			continue
		}
		i++
	}
	return 0, untok("unterminated double-quoted string")
}

// shellParens scans a $( ) body (quotes nested) to just after its ).
func shellParens(src string, i int) (int, error) {
	depth := 1
	for i < len(src) {
		switch src[i] {
		case '\\':
			i += 2
			continue
		case '\'':
			j := strings.IndexByte(src[i+1:], '\'')
			if j < 0 {
				return 0, untok("unterminated single-quoted string")
			}
			i += j + 2
			continue
		case '"':
			j, err := shellDQ(src, i+1)
			if err != nil {
				return 0, err
			}
			i = j
			continue
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1, nil
			}
		}
		i++
	}
	return 0, untok("unterminated $( )")
}

// shellBraces scans a ${ } body to just after its }.
func shellBraces(src string, i int) (int, error) {
	depth := 1
	for i < len(src) {
		switch src[i] {
		case '\\':
			i += 2
			continue
		case '"':
			j, err := shellDQ(src, i+1)
			if err != nil {
				return 0, err
			}
			i = j
			continue
		case '\'':
			j := strings.IndexByte(src[i+1:], '\'')
			if j < 0 {
				return 0, untok("unterminated single-quoted string")
			}
			i += j + 2
			continue
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1, nil
			}
		case '\n':
			return 0, untok("unterminated ${ }")
		}
		i++
	}
	return 0, untok("unterminated ${ }")
}

// shellHeredocOp parses the word after << (i is just past it).
func shellHeredocOp(src string, i int) (heredoc, int, bool) {
	var h heredoc
	if i < len(src) && src[i] == '-' {
		h.stripTabs = true
		i++
	}
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	var word strings.Builder
	for i < len(src) {
		c := src[i]
		if c == '\'' || c == '"' {
			j := strings.IndexByte(src[i+1:], c)
			if j < 0 {
				return h, i, false
			}
			word.WriteString(src[i+1 : i+1+j])
			i += j + 2
			continue
		}
		if c == '\\' && i+1 < len(src) {
			word.WriteByte(src[i+1])
			i += 2
			continue
		}
		if strings.IndexByte(" \t\n;&|<>()", c) >= 0 {
			break
		}
		word.WriteByte(c)
		i++
	}
	h.delim = word.String()
	return h, i, h.delim != ""
}

// ---- python ---------------------------------------------------------

func isIdentByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

var pyPrefixes = map[string]bool{"r": true, "b": true, "u": true, "f": true, "rb": true, "br": true, "fr": true, "rf": true}

func tokenizePython(src string) ([]tokSeg, error) {
	var out []tokSeg
	n := len(src)
	i := 0
	for i < n {
		c := src[i]
		switch c {
		case '#':
			end := lineEnd(src, i)
			out = append(out, tokSeg{Kind: segComment, Start: i, End: end})
			i = end
		case '\'', '"':
			start := i
			// A string prefix (r, b, f, rb, …) is the identifier just before.
			k := i
			for k > 0 && isIdentByte(src[k-1]) {
				k--
			}
			if k < i && pyPrefixes[strings.ToLower(src[k:i])] {
				start = k
			}
			end, err := pyString(src, i)
			if err != nil {
				return nil, err
			}
			out = append(out, tokSeg{Kind: segString, Start: start, End: end})
			i = end
		default:
			i++
		}
	}
	return out, nil
}

func pyString(src string, i int) (int, error) {
	q := src[i]
	triple := strings.Repeat(string(q), 3)
	if strings.HasPrefix(src[i:], triple) {
		j := i + 3
		for j < len(src) {
			if src[j] == '\\' {
				j += 2
				continue
			}
			if strings.HasPrefix(src[j:], triple) {
				return j + 3, nil
			}
			j++
		}
		return 0, untok("unterminated triple-quoted string")
	}
	j := i + 1
	for j < len(src) {
		switch src[j] {
		case '\\':
			j += 2
			continue
		case q:
			return j + 1, nil
		case '\n':
			return 0, untok("unterminated string at a newline")
		}
		j++
	}
	return 0, untok("unterminated string")
}

// ---- JS / TS ----------------------------------------------------------

var jsRegexKeywords = map[string]bool{
	"return": true, "typeof": true, "instanceof": true, "in": true, "of": true, "new": true, "delete": true,
	"void": true, "throw": true, "case": true, "do": true, "else": true, "yield": true, "await": true,
}

//nolint:gocyclo // a hand-written lexer: one case per token class
func tokenizeJS(src string) ([]tokSeg, error) {
	var out []tokSeg
	n := len(src)
	// prev is the last significant token: "" at start, a punctuator, or
	// "id"/"num"/a keyword — what decides whether / starts a regex.
	prev := ""
	var tmpl []int // brace depth inside each open ${ } of a template
	i := 0
	if strings.HasPrefix(src, "#!") {
		i = lineEnd(src, 0)
	}
	for i < n {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case strings.HasPrefix(src[i:], "//"):
			end := lineEnd(src, i)
			out = append(out, tokSeg{Kind: segComment, Start: i, End: end})
			i = end
		case strings.HasPrefix(src[i:], "/*"):
			j := strings.Index(src[i+2:], "*/")
			if j < 0 {
				return nil, untok("unterminated block comment")
			}
			out = append(out, tokSeg{Kind: segComment, Start: i, End: i + j + blockDelims, Block: true})
			i += j + blockDelims
		case c == '/' && jsRegexAllowed(prev):
			j, err := jsRegex(src, i)
			if err != nil {
				return nil, err
			}
			out = append(out, tokSeg{Kind: segString, Start: i, End: j})
			i, prev = j, "num"
		case c == '\'' || c == '"':
			j, err := jsQuoted(src, i)
			if err != nil {
				return nil, err
			}
			out = append(out, tokSeg{Kind: segString, Start: i, End: j})
			i, prev = j, "num"
		case c == '`':
			j, interp, err := jsTemplate(src, i+1)
			if err != nil {
				return nil, err
			}
			out = append(out, tokSeg{Kind: segString, Start: i, End: j})
			i, prev = j, "num"
			if interp {
				tmpl = append(tmpl, 0)
				prev = "{"
			}
		case c == '{':
			if len(tmpl) > 0 {
				tmpl[len(tmpl)-1]++
			}
			i, prev = i+1, "{"
		case c == '}' && len(tmpl) > 0 && tmpl[len(tmpl)-1] == 0:
			// The } closing a ${ resumes its template.
			tmpl = tmpl[:len(tmpl)-1]
			j, interp, err := jsTemplate(src, i+1)
			if err != nil {
				return nil, err
			}
			out = append(out, tokSeg{Kind: segString, Start: i, End: j})
			i, prev = j, "num"
			if interp {
				tmpl = append(tmpl, 0)
				prev = "{"
			}
		case c == '}':
			if len(tmpl) > 0 {
				tmpl[len(tmpl)-1]--
			}
			i, prev = i+1, "}"
		case isIdentByte(c) || c == '$' || c >= 0x80:
			j := i
			for j < n && (isIdentByte(src[j]) || src[j] == '$' || src[j] >= 0x80 || (src[j] == '.' && c >= '0' && c <= '9')) {
				j++
			}
			word := src[i:j]
			switch {
			case jsRegexKeywords[word]:
				prev = word
			case c >= '0' && c <= '9':
				prev = "num"
			default:
				prev = "id"
			}
			i = j
		default:
			prev = string(c)
			i++
		}
	}
	if len(tmpl) > 0 {
		return nil, untok("unterminated template literal")
	}
	return out, nil
}

func jsRegexAllowed(prev string) bool {
	if prev == "" || jsRegexKeywords[prev] {
		return true
	}
	if prev == "id" || prev == "num" || prev == ")" || prev == "]" || prev == "}" {
		return false
	}
	return strings.Contains("(,=:[!&|?{};+-*%<>~^", prev) //nolint:gocritic // argOrder: the punctuator set is the haystack, prev the needle
}

func jsRegex(src string, i int) (int, error) {
	j := i + 1
	inClass := false
	for j < len(src) {
		switch src[j] {
		case '\\':
			j += 2
			continue
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				j++
				for j < len(src) && isIdentByte(src[j]) {
					j++
				}
				return j, nil
			}
		case '\n':
			return 0, untok("unterminated regular expression")
		}
		j++
	}
	return 0, untok("unterminated regular expression")
}

func jsQuoted(src string, i int) (int, error) {
	q := src[i]
	j := i + 1
	for j < len(src) {
		switch src[j] {
		case '\\':
			j += 2
			continue
		case q:
			return j + 1, nil
		case '\n':
			return 0, untok("unterminated string at a newline")
		}
		j++
	}
	return 0, untok("unterminated string")
}

// jsTemplate scans template text from i to its closing ` (interp false)
// or to just after a ${ (interp true).
func jsTemplate(src string, i int) (end int, interp bool, err error) {
	for i < len(src) {
		switch {
		case src[i] == '\\':
			i += 2
			continue
		case src[i] == '`':
			return i + 1, false, nil
		case strings.HasPrefix(src[i:], "${"):
			return i + 2, true, nil
		}
		i++
	}
	return 0, false, untok("unterminated template literal")
}

// ---- Dart -----------------------------------------------------------

type dartStr struct {
	quote  string // ' " ''' """
	braces int
}

func tokenizeDart(src string) ([]tokSeg, error) {
	var out []tokSeg
	n := len(src)
	var stack []dartStr // strings suspended at a ${
	i := 0
	for i < n {
		c := src[i]
		switch {
		case strings.HasPrefix(src[i:], "//"):
			end := lineEnd(src, i)
			out = append(out, tokSeg{Kind: segComment, Start: i, End: end})
			i = end
		case strings.HasPrefix(src[i:], "/*"):
			j, err := dartBlock(src, i)
			if err != nil {
				return nil, err
			}
			out = append(out, tokSeg{Kind: segComment, Start: i, End: j, Block: true})
			i = j
		case c == '\'' || c == '"':
			raw := i > 0 && src[i-1] == 'r' && (i < 2 || !isIdentByte(src[i-2]))
			start := i
			if raw {
				start = i - 1
			}
			q := string(c)
			if strings.HasPrefix(src[i:], strings.Repeat(q, 3)) {
				q = strings.Repeat(q, 3)
			}
			j, interp, err := dartString(src, i+len(q), q, raw)
			if err != nil {
				return nil, err
			}
			out = append(out, tokSeg{Kind: segString, Start: start, End: j})
			i = j
			if interp {
				stack = append(stack, dartStr{quote: q})
			}
		case c == '{':
			if len(stack) > 0 {
				stack[len(stack)-1].braces++
			}
			i++
		case c == '}' && len(stack) > 0 && stack[len(stack)-1].braces == 0:
			// The } closing a ${ resumes its string.
			q := stack[len(stack)-1].quote
			stack = stack[:len(stack)-1]
			j, interp, err := dartString(src, i+1, q, false)
			if err != nil {
				return nil, err
			}
			out = append(out, tokSeg{Kind: segString, Start: i, End: j})
			i = j
			if interp {
				stack = append(stack, dartStr{quote: q})
			}
		case c == '}':
			if len(stack) > 0 {
				stack[len(stack)-1].braces--
			}
			i++
		default:
			i++
		}
	}
	if len(stack) > 0 {
		return nil, untok("unterminated string interpolation")
	}
	return out, nil
}

// dartBlock scans a nested /* */ comment.
func dartBlock(src string, i int) (int, error) {
	depth := 0
	for i < len(src) {
		switch {
		case strings.HasPrefix(src[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(src[i:], "*/"):
			depth--
			i += 2
			if depth == 0 {
				return i, nil
			}
		default:
			i++
		}
	}
	return 0, untok("unterminated block comment")
}

// dartString scans string text to its closing quote, or to just after a ${.
func dartString(src string, i int, q string, raw bool) (end int, interp bool, err error) {
	for i < len(src) {
		switch {
		case !raw && src[i] == '\\':
			i += 2
			continue
		case strings.HasPrefix(src[i:], q):
			return i + len(q), false, nil
		case !raw && strings.HasPrefix(src[i:], "${"):
			return i + 2, true, nil
		case len(q) == 1 && src[i] == '\n':
			return 0, false, untok("unterminated string at a newline")
		}
		i++
	}
	return 0, false, untok("unterminated string")
}

// ---- C / C++ ----------------------------------------------------------

//nolint:gocyclo // a hand-written lexer: one case per token class
func tokenizeC(src string) ([]tokSeg, error) {
	var out []tokSeg
	n := len(src)
	i := 0
	for i < n {
		c := src[i]
		switch {
		case strings.HasPrefix(src[i:], "//"):
			end := lineEnd(src, i)
			if strings.HasSuffix(strings.TrimRight(src[i:end], "\r"), "\\") {
				return nil, untok("a // comment continued by a backslash")
			}
			out = append(out, tokSeg{Kind: segComment, Start: i, End: end})
			i = end
		case strings.HasPrefix(src[i:], "/*"):
			j := strings.Index(src[i+2:], "*/")
			if j < 0 {
				return nil, untok("unterminated block comment")
			}
			out = append(out, tokSeg{Kind: segComment, Start: i, End: i + j + blockDelims, Block: true})
			i += j + blockDelims
		case c >= '0' && c <= '9' || c == '.' && i+1 < n && src[i+1] >= '0' && src[i+1] <= '9':
			// A pp-number, digit separators (1'000) included.
			j := i + 1
			for j < n {
				d := src[j]
				if isIdentByte(d) || d == '.' || d == '\'' && j+1 < n && isIdentByte(src[j+1]) {
					j++
					continue
				}
				if (d == '+' || d == '-') && strings.IndexByte("eEpP", src[j-1]) >= 0 {
					j++
					continue
				}
				break
			}
			i = j
		case isIdentByte(c):
			j := i
			for j < n && isIdentByte(src[j]) {
				j++
			}
			word := src[i:j]
			if j < n && src[j] == '"' && (word == "R" || word == "LR" || word == "uR" || word == "UR" || word == "u8R") {
				end, err := cRaw(src, j+1)
				if err != nil {
					return nil, err
				}
				out = append(out, tokSeg{Kind: segString, Start: i, End: end})
				i = end
				continue
			}
			if j < n && (src[j] == '"' || src[j] == '\'') && (word == "L" || word == "u" || word == "U" || word == "u8") {
				end, err := jsQuoted(src, j)
				if err != nil {
					return nil, err
				}
				out = append(out, tokSeg{Kind: segString, Start: i, End: end})
				i = end
				continue
			}
			i = j
		case c == '"' || c == '\'':
			end, err := jsQuoted(src, i)
			if err != nil {
				return nil, err
			}
			out = append(out, tokSeg{Kind: segString, Start: i, End: end})
			i = end
		default:
			i++
		}
	}
	return out, nil
}

// cRaw scans a raw string R"delim( … )delim" from just after its quote.
func cRaw(src string, i int) (int, error) {
	open := strings.IndexByte(src[i:], '(')
	if open < 0 || open > 16 {
		return 0, untok("malformed raw string")
	}
	closing := ")" + src[i:i+open] + "\""
	j := strings.Index(src[i+open:], closing)
	if j < 0 {
		return 0, untok("unterminated raw string")
	}
	return i + open + j + len(closing), nil
}

// ---- YAML -------------------------------------------------------------

var yamlBlockIndicatorRe = regexp.MustCompile(`(?:^|[\s:\-])[|>][0-9+-]*\s*$`)

func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

func tokenizeYAML(src string) ([]tokSeg, error) {
	var out []tokSeg
	inBlock, blockIndent := false, 0
	var quote byte // an open ' or " flow scalar, across lines
	quoteStart := 0
	for off := 0; off < len(src); {
		end := lineEnd(src, off)
		line := src[off:end]
		next := min(end+1, len(src))
		if inBlock {
			if strings.TrimSpace(line) == "" || indentOf(line) > blockIndent {
				out = append(out, tokSeg{Kind: segString, Start: off, End: end})
				off = next
				continue
			}
			inBlock = false
		}
		codeEnd := len(line)
		for j := 0; j < len(line); j++ {
			c := line[j]
			if quote != 0 {
				switch {
				case quote == '"' && c == '\\':
					j++
				case quote == '\'' && c == '\'' && j+1 < len(line) && line[j+1] == '\'':
					j++
				case c == quote:
					out = append(out, tokSeg{Kind: segString, Start: quoteStart, End: off + j + 1})
					quote = 0
				}
				continue
			}
			if c == '#' && (j == 0 || line[j-1] == ' ' || line[j-1] == '\t') {
				out = append(out, tokSeg{Kind: segComment, Start: off + j, End: end})
				codeEnd = j
				break
			}
			if (c == '"' || c == '\'') && yamlQuoteStarts(line, j) {
				quote, quoteStart = c, off+j
			}
		}
		if quote == 0 && yamlBlockIndicatorRe.MatchString(line[:codeEnd]) {
			inBlock, blockIndent = true, indentOf(line)
		}
		off = next
	}
	if quote != 0 {
		return nil, untok("unterminated quoted scalar")
	}
	return out, nil
}

// yamlQuoteStarts reports whether a quote at j opens a quoted scalar
// rather than sitting inside a plain one (`it's`).
func yamlQuoteStarts(line string, j int) bool {
	k := j - 1
	for k >= 0 && (line[k] == ' ' || line[k] == '\t') {
		k--
	}
	return k < 0 || strings.IndexByte(":-,[{?", line[k]) >= 0
}

// ---- Makefile -----------------------------------------------------------

func tokenizeMake(src string) ([]tokSeg, error) {
	var out []tokSeg
	inDefine, recipeCont := false, false
	for off := 0; off < len(src); {
		end := lineEnd(src, off)
		line := strings.TrimSuffix(src[off:end], "\r")
		next := min(end+1, len(src))
		trimmed := strings.TrimSpace(line)
		continued := strings.HasSuffix(line, "\\")
		switch {
		case inDefine:
			if trimmed == "endef" || strings.HasPrefix(trimmed, "endef ") {
				inDefine = false
			}
		case strings.HasPrefix(line, "\t") || recipeCont:
			// A recipe line is the shell's, not make's: code.
			recipeCont = continued
		case strings.HasPrefix(trimmed, "define ") || trimmed == "define" || strings.Contains(trimmed, " define "):
			inDefine = true
		default:
			depth := 0
			for j := 0; j < len(line); j++ {
				c := line[j]
				switch {
				case c == '\\':
					j++
				case c == '$' && j+1 < len(line) && (line[j+1] == '(' || line[j+1] == '{'):
					depth++
					j++
				case (c == ')' || c == '}') && depth > 0:
					depth--
				case c == '#' && depth == 0:
					if continued {
						// make continues a comment onto the next line.
						return nil, untok("a # comment continued by a backslash")
					}
					out = append(out, tokSeg{Kind: segComment, Start: off + j, End: end})
					j = len(line)
				}
			}
		}
		off = next
	}
	return out, nil
}
