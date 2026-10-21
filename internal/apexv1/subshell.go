package apexv1

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// R24b: cortex's regression command was one ( … ) block of seven lines,
// newline-separated without set -e, so only its last line's exit counted:
// go test failed seven tests after the lift, the block exited 0, and the
// lift called it green. A check, regression or gate command that is
// exactly one multi-line ( … ) block of simple commands runs as its lines
// joined by &&, so each line's exit counts, as under set -e. A line whose
// constructs (a for … done, an if … fi, a case … esac, a { … } group, a
// [[ … ]] or (( … ))) open and close on that line counts as one (D114:
// the lift wraps a fence in a block only when a line holds a construct).
// A block with a line that is not one runs as written: joined, a cd or an
// export would hold differently, and a construct left open would not
// parse.

var (
	// subshellStateRe is a command that changes the shell's own state for
	// the lines after it.
	subshellStateRe = regexp.MustCompile(`^(?:cd|pushd|popd|export|unset|set|source|\.|alias|unalias|trap|umask|ulimit|exec|shopt|eval|local|declare|readonly|typeset)$`)
	// assignWordRe is a NAME=value word.
	assignWordRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// splitSubshell returns the commands of cmd's ( … ) block when cmd,
// trimmed, is exactly one such block spanning several lines and every
// line in it is a simple command: blank lines and full-line comments
// dropped, a trailing comment cut, and a first line of set -e (the lift
// writes one, Q141) dropped too. For a block it does not split, refused
// says which line and why (( a ) ⏎ ( b ) is refused: its first ) leaves
// line 1 unbalanced); both are empty for a command that is no such block,
// or a block of a single command.
func splitSubshell(cmd string) (cmds []string, refused string) {
	lines := strings.Split(cmd, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			lines[i] = ""
		} else {
			lines[i] = stripShellComment(l)
		}
	}
	t := strings.TrimSpace(strings.Join(lines, "\n"))
	if len(t) < 2 || t[0] != '(' || t[len(t)-1] != ')' || strings.HasPrefix(t, "((") || !strings.Contains(t, "\n") {
		return nil, ""
	}
	for i, l := range strings.Split(t[1:len(t)-1], "\n") {
		if len(cmds) == 0 && strings.TrimSpace(l) == "set -e" {
			// The lift's first line (Q141): the && join already stops at
			// the first red line.
			continue
		}
		code, why := simpleLine(l)
		if why != "" {
			return nil, "line " + strconv.Itoa(i+1) + " " + why
		}
		if code != "" {
			cmds = append(cmds, code)
		}
	}
	if len(cmds) < 2 {
		return nil, ""
	}
	return cmds, ""
}

// simpleLine is one line of a ( … ) block as a command to join: "" for a
// blank line or a comment, else the line without its trailing comment. why
// says what keeps it from being a simple command.
func simpleLine(l string) (code, why string) {
	t := strings.TrimSpace(l)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", ""
	}
	t = strings.TrimSpace(stripShellComment(t))
	if strings.HasSuffix(t, `\`) {
		return "", `continues on the next line (a trailing \)`
	}
	toks, heredoc, balanced := shellTokens(t)
	switch {
	case !balanced:
		return "", whyUnbalanced
	case heredoc:
		return "", "opens a construct (a here-doc <<)"
	}
	if why, _ := lineConstructs(toks); why != "" {
		return "", why
	}
	return t, ""
}

// whyUnbalanced is the reason a line with unbalanced quotes or
// parentheses is no simple command.
const whyUnbalanced = "has unbalanced quotes or parentheses"

// shellTok is one token of a line: an operator (; ;; & && || | |& ( )) or
// a word, quotes, $( ), ${ }, `…` and (( )) inside it.
type shellTok struct {
	op, word string
}

// shellTokens splits one line into operators and words, and reports a
// here-doc (<<, not the here-string <<<) and whether its quotes and
// parentheses balance. A # that starts a token ends the line.
//
//nolint:gocyclo // a hand-written lexer: one case per token class
func shellTokens(t string) (toks []shellTok, heredoc, balanced bool) {
	for i := 0; i < len(t); {
		c := t[i]
		switch {
		case c == ' ' || c == '\t':
			i++
			continue
		case c == '#':
			return toks, heredoc, true
		case c == '(' && strings.HasPrefix(t[i:], "(("):
			end := closeParens(t, i)
			if end < 0 || t[end-1] != ')' {
				return nil, false, false
			}
			toks = append(toks, shellTok{word: t[i : end+1]})
			i = end + 1
			continue
		}
		if op := shellOp(t[i:]); op != "" {
			toks = append(toks, shellTok{op: op})
			i += len(op)
			continue
		}
		start := i
		for i < len(t) {
			c := t[i]
			if c == ' ' || c == '\t' || shellOp(t[i:]) != "" && (c != '&' || i <= start || t[i-1] != '>' && t[i-1] != '<') {
				break
			}
			switch {
			case c == '\\':
				i += 2
				continue
			case c == '\'':
				j := strings.IndexByte(t[i+1:], '\'')
				if j < 0 {
					return nil, false, false
				}
				i += j + 2
				continue
			case c == '"' || c == '`':
				j := closeQuote(t, i)
				if j < 0 {
					return nil, false, false
				}
				i = j + 1
				continue
			case (c == '$' || c == '<' || c == '>') && i+1 < len(t) && t[i+1] == '(':
				end := closeParens(t, i+1)
				if end < 0 {
					return nil, false, false
				}
				i = end + 1
				continue
			case c == '$' && i+1 < len(t) && t[i+1] == '{':
				j := strings.IndexByte(t[i:], '}')
				if j < 0 {
					return nil, false, false
				}
				i += j + 1
				continue
			case c == '<' && strings.HasPrefix(t[i:], "<<<"):
				i += 3
				continue
			case c == '<' && strings.HasPrefix(t[i:], "<<"):
				heredoc = true
			}
			i++
		}
		toks = append(toks, shellTok{word: t[start:min(i, len(t))]})
	}
	return toks, heredoc, true
}

// shellOp is the operator s starts with, or "". An & before > is a
// redirection (&>file), not one.
func shellOp(s string) string {
	for _, op := range []string{";;", "&&", "||", "|&", ";", "|", "(", ")"} {
		if strings.HasPrefix(s, op) {
			return op
		}
	}
	if strings.HasPrefix(s, "&") && !strings.HasPrefix(s, "&>") {
		return "&"
	}
	return ""
}

// closeQuote is the index of the quote closing the " or ` at t[i], or -1.
func closeQuote(t string, i int) int {
	q := t[i]
	for j := i + 1; j < len(t); j++ {
		switch t[j] {
		case '\\':
			j++
		case q:
			return j
		}
	}
	return -1
}

// closeParens is the index of the ) closing the ( at t[i], quotes inside
// skipped, or -1.
func closeParens(t string, i int) int {
	depth := 0
	for j := i; j < len(t); j++ {
		switch t[j] {
		case '\\':
			j++
		case '\'':
			k := strings.IndexByte(t[j+1:], '\'')
			if k < 0 {
				return -1
			}
			j += k + 1
		case '"', '`':
			if j = closeQuote(t, j); j < 0 {
				return -1
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// The case … esac a frame is in: its word, its patterns, a clause's body.
const (
	caseWord = iota + 1
	casePattern
	caseBody
)

// shellFrame is a construct open on a line: the word that closes it, the
// word that opened it, and for a case where it is.
type shellFrame struct {
	closer, opener string
	mode           int
}

// lineConstructs walks one line's tokens, words read as keywords only at a
// command's start (the line's, after an operator, after do, then, else,
// elif, if, while, until, {, !): every construct must open and close on
// the line, no command may change the shell's state (except inside a
// ( … ) subshell, where it changes the subshell's only), and the line may
// not end joined to the next. It returns why the line is no simple
// command, or "", and whether the line is a list at its top level (a ;, &
// or || outside any construct).
//
//nolint:gocyclo // one pass over a line's tokens: one case per construct keyword, each a few lines
func lineConstructs(toks []shellTok) (why string, list bool) {
	var stack []shellFrame
	top := func() *shellFrame {
		if len(stack) == 0 {
			return &shellFrame{}
		}
		return &stack[len(stack)-1]
	}
	inSubshell := func() bool {
		for _, f := range stack {
			if f.closer == ")" {
				return true
			}
		}
		return false
	}
	cmdPos := true
	for i := 0; i < len(toks); i++ {
		tk := toks[i]
		f := top()
		switch {
		case f.opener == "case" && f.mode == caseWord:
			if tk.word == "in" {
				f.mode = casePattern
			}
			continue
		case f.opener == "case" && f.mode == casePattern:
			switch {
			case tk.op == ")":
				f.mode, cmdPos = caseBody, true
			case tk.word == "esac":
				stack, cmdPos = stack[:len(stack)-1], false
			}
			continue
		case f.opener == "case" && f.mode == caseBody && tk.op == ";;":
			f.mode, cmdPos = casePattern, false
			continue
		}
		if tk.op != "" {
			switch tk.op {
			case "(":
				switch {
				case cmdPos:
					stack = append(stack, shellFrame{closer: ")", opener: "("})
				case i > 0 && toks[i-1].word != "" && i+1 < len(toks) && toks[i+1].op == ")":
					return "opens a construct (a function definition)", false
				default:
					return whyUnbalanced, false
				}
			case ")":
				if f.closer != ")" {
					return whyUnbalanced, false
				}
				stack, cmdPos = stack[:len(stack)-1], false
				continue
			case ";;":
				return "closes a construct it did not open (;;)", false
			case ";", "&", "||":
				list = list || len(stack) == 0
			}
			cmdPos = true
			continue
		}
		if !cmdPos {
			continue
		}
		w := tk.word
		switch w {
		case "if":
			stack = append(stack, shellFrame{closer: "fi", opener: w})
		case "while", "until":
			stack = append(stack, shellFrame{closer: "done", opener: w})
		case "for", "select":
			stack = append(stack, shellFrame{closer: "done", opener: w})
			cmdPos = false // its name and words
		case "case":
			stack = append(stack, shellFrame{closer: "esac", opener: w, mode: caseWord})
			cmdPos = false
		case "{":
			stack = append(stack, shellFrame{closer: "}", opener: w})
		case "then", "elif", "else":
			if f.closer != "fi" {
				return fmt.Sprintf("closes a construct it did not open (%s)", w), false
			}
		case "do":
			if f.closer != "done" {
				return fmt.Sprintf("closes a construct it did not open (%s)", w), false
			}
		case "fi", "done", "esac", "}":
			if f.closer != w || f.opener == "case" && f.mode != caseBody {
				return fmt.Sprintf("closes a construct it did not open (%s)", w), false
			}
			stack, cmdPos = stack[:len(stack)-1], false
		case "[[":
			j := i + 1
			for j < len(toks) && toks[j].word != "]]" {
				j++
			}
			if j == len(toks) {
				return "opens a construct ([[) that does not close on that line", false
			}
			i, cmdPos = j, false
		case "function":
			return "opens a construct (a function definition)", false
		case "!", "time":
		default:
			switch {
			case subshellStateRe.MatchString(w) && !inSubshell():
				return fmt.Sprintf("changes shell state (%s)", w), false
			case assignWordRe.MatchString(w):
				// NAME=value before a command sets its environment only;
				// with no command after it, the shell's.
				if (i+1 == len(toks) || toks[i+1].op != "") && !inSubshell() {
					return "changes shell state (a bare NAME=value)", false
				}
			default:
				cmdPos = false
			}
		}
	}
	if len(stack) > 0 {
		return fmt.Sprintf("opens a construct (%s) that does not close on that line", stack[len(stack)-1].opener), false
	}
	if n := len(toks); n > 0 && toks[n-1].op != "" && toks[n-1].op != ")" {
		return "is already joined to the next (it ends in &&, ||, ;, | or &)", false
	}
	return "", list
}

// joinLines joins a block's commands with &&. A command that is a list at
// its top level goes in { …; }, so the && cannot regroup it: x && a || b
// runs b when x fails, and its exit hides x's.
func joinLines(cmds []string) string {
	parts := make([]string, len(cmds))
	for i, c := range cmds {
		toks, _, _ := shellTokens(c)
		if _, list := lineConstructs(toks); list {
			c = "{ " + c + "; }"
		}
		parts[i] = c
	}
	return strings.Join(parts, " && ")
}

// checkScript is what a check command runs as: its ( … ) block's lines
// joined by && when it splits (R24b), else the command as written. note is
// the line its log opens with, "" when there is nothing to say.
func checkScript(cmd string) (script, note string) {
	cmds, refused := splitSubshell(cmd)
	switch {
	case len(cmds) > 0:
		script = joinLines(cmds)
		return script, fmt.Sprintf("[ape] split into %d commands: the ( … ) block runs as its lines joined by &&, so each line's exit counts, as under set -e: %s", len(cmds), script)
	case refused != "":
		return cmd, "[ape] the ( … ) block runs as written, not split into commands: its " + refused
	}
	return cmd, ""
}
