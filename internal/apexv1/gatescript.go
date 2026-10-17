package apexv1

import (
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// The gate's scripts (Q15, D51). An approval freezes what the gate runs,
// not what it tests: of each simple command of the gate it takes the
// executable word when that is a path (it has a slash: ./scripts/e2e.sh,
// scripts/x.sh), and the script an interpreter is given (bash
// scripts/gate.sh, python3 scripts/gate.py). Every other word is an
// argument — product code, a data file, CHANGELOG.md — and changes as the
// milestone's changes land, so hashing it would fail the gate its own
// work turns green.

// interpreter is how an interpreter that runs a script file is told
// otherwise: its short options that give it code instead of a file
// (noScript), and those that take the next word as their argument
// (withArg); likewise the long ones. plus: +o is an option too (a shell).
type interpreter struct {
	noScript, withArg         string
	noScriptLong, withArgLong []string
	plus                      bool
}

var shellInterpreter = interpreter{noScript: "cs", withArg: "oO", plus: true}

// interpreters are keyed by the command's base name without trailing
// version digits (python3.12 → python).
var interpreters = map[string]interpreter{
	"sh": shellInterpreter, "bash": shellInterpreter, "zsh": shellInterpreter, "dash": shellInterpreter,
	"python": {noScript: "cm", withArg: "WX"},
	"node":   {noScript: "ep", withArg: "r", noScriptLong: []string{"--eval", "--print"}, withArgLong: []string{"--require", "--import", "--loader", "--conditions"}},
	"ruby":   {noScript: "e", withArg: "IrCEF"},
	"perl":   {noScript: "eE"},
}

// commandPrefixes run the command after them: the word that follows is
// the executable word.
var commandPrefixes = map[string]bool{
	"!": true, "{": true, "if": true, "then": true, "else": true, "elif": true, "do": true,
	"while": true, "until": true, "time": true, "exec": true, "command": true, "env": true,
}

// gateScripts are the repo-relative, slash-separated paths gate's
// commands run as a script (see above), sorted, whether or not they exist.
// Absolute paths, paths leaving the root, and words with $ (an expansion),
// a glob character or a leading ~ are none.
func gateScripts(gate string) []string {
	set := map[string]bool{}
	for _, c := range gateCommands(gate) {
		i, env := 0, false
		for i < len(c) && (isAssignment(c[i]) || commandPrefixes[c[i]] || env && strings.HasPrefix(c[i], "-")) {
			env = env || c[i] == "env"
			i++
		}
		if i == len(c) {
			continue
		}
		exe := c[i]
		if strings.Contains(exe, "/") {
			if rel, ok := repoPath(exe); ok {
				set[rel] = true
			}
		}
		name := strings.TrimRight(path.Base(exe), "0123456789.")
		if in, ok := interpreters[name]; ok {
			if rel, ok := repoPath(in.script(c[i+1:])); ok {
				set[rel] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for rel := range set {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// script is the script file an interpreter is given by args (the words
// after it): the first word that is not an option or an option's
// argument; "" when an option gives it code instead (bash -c, python3 -m,
// node -e), or it reads standard input.
func (in interpreter) script(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		case a == "-":
			return ""
		case strings.HasPrefix(a, "--"):
			name, _, glued := strings.Cut(a, "=")
			if slices.Contains(in.noScriptLong, name) {
				return ""
			}
			if !glued && slices.Contains(in.withArgLong, name) {
				i++
			}
		case strings.HasPrefix(a, "-") || in.plus && strings.HasPrefix(a, "+"):
			for j, r := range a[1:] {
				if strings.ContainsRune(in.noScript, r) {
					return ""
				}
				if strings.ContainsRune(in.withArg, r) {
					if j == len(a)-2 { // its argument is the next word
						i++
					}
					break
				}
			}
		default:
			return a
		}
	}
	return ""
}

// isAssignment reports a leading NAME=value word of a simple command.
func isAssignment(w string) bool {
	name, _, ok := strings.Cut(w, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		if r != '_' && !unicode.IsLetter(r) && (i == 0 || !unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

// repoPath is w as a clean, slash-separated path under the project root,
// when it can name one.
func repoPath(w string) (string, bool) {
	if w == "" || strings.HasPrefix(w, "-") || strings.ContainsAny(w, "$*?[~") || filepath.IsAbs(w) || strings.HasPrefix(w, "/") {
		return "", false
	}
	rel := filepath.Clean(filepath.FromSlash(w))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// gateCommands splits gate into its simple commands, each its words with
// the quotes removed: at ; & | newlines and parentheses (so && and ||
// too), dropping redirections and their targets and # comments. A
// command substitution — $(…) or backquotes — and any other expansion
// stays inside its word as a $, unread: its output is an argument of the
// command around it, and what it runs is what the gate tests. It is a
// reading of the shell good enough for a gate line, not a shell parser.
func gateCommands(gate string) [][]string {
	l := &gateLexer{rs: []rune(gate)}
	for l.i = 0; l.i < len(l.rs); l.i++ {
		l.step()
	}
	l.endCmd()
	return l.cmds
}

// gateLexer is gateCommands' state: the commands so far, the current one's
// words, the word being read (inWord: one has started, even if empty, as
// ”), and target: that word is a redirection's target, dropped.
type gateLexer struct {
	rs             []rune
	i              int
	cmds           [][]string
	cur            []string
	w              strings.Builder
	inWord, target bool
}

func (l *gateLexer) endWord() {
	if l.inWord {
		if !l.target {
			l.cur = append(l.cur, l.w.String())
		}
		l.target = false
	}
	l.w.Reset()
	l.inWord = false
}

func (l *gateLexer) endCmd() {
	l.endWord()
	if len(l.cur) > 0 {
		l.cmds = append(l.cmds, l.cur)
	}
	l.cur, l.target = nil, false
}

func (l *gateLexer) add(r rune) {
	l.w.WriteRune(r)
	l.inWord = true
}

// step reads the rune at l.i, and whatever it starts.
func (l *gateLexer) step() {
	rs, r := l.rs, l.rs[l.i]
	switch {
	case r == '\\':
		if l.i+1 < len(rs) {
			l.i++
			if rs[l.i] != '\n' {
				l.add(rs[l.i])
			}
		}
	case r == '\'':
		j := l.i + 1
		for j < len(rs) && rs[j] != '\'' {
			j++
		}
		l.w.WriteString(string(rs[l.i+1 : min(j, len(rs))]))
		l.inWord, l.i = true, j
	case r == '"':
		l.doubleQuoted()
	case r == '$' || r == '`':
		l.i = skipExpansion(rs, l.i)
		l.add('$')
	case r == '#' && !l.inWord:
		for l.i+1 < len(rs) && rs[l.i+1] != '\n' {
			l.i++
		}
	case strings.ContainsRune("\n;|()", r), r == '&' && (l.i+1 >= len(rs) || rs[l.i+1] != '>'):
		l.endCmd()
	case r == '<' || r == '>' || r == '&':
		l.redirection()
	case unicode.IsSpace(r):
		l.endWord()
	default:
		l.add(r)
	}
}

// doubleQuoted reads a "…" string from its opening quote at l.i to its
// closing one.
func (l *gateLexer) doubleQuoted() {
	rs := l.rs
	l.inWord = true
	for l.i++; l.i < len(rs) && rs[l.i] != '"'; l.i++ {
		switch rs[l.i] {
		case '\\':
			if l.i+1 < len(rs) {
				l.i++
				l.w.WriteRune(rs[l.i])
			}
		case '$', '`':
			l.i = skipExpansion(rs, l.i)
			l.w.WriteByte('$')
		default:
			l.w.WriteRune(rs[l.i])
		}
	}
}

// redirection reads a redirection operator at l.i (<, >, >>, 2>&1, &>
// …): the descriptor before it is not a word, the word after it is its
// target.
func (l *gateLexer) redirection() {
	if s := l.w.String(); l.inWord && strings.Trim(s, "0123456789") == "" {
		l.w.Reset() // the descriptor of 2>
		l.inWord = false
	}
	l.endWord()
	for l.i+1 < len(l.rs) && strings.ContainsRune("<>&|", l.rs[l.i+1]) {
		l.i++
	}
	l.target = true
}

// skipExpansion returns the index of the last rune of the expansion
// starting at rs[i] — $name, ${…}, $(…), $((…)) or `…`.
func skipExpansion(rs []rune, i int) int {
	if rs[i] == '`' {
		for j := i + 1; j < len(rs); j++ {
			switch rs[j] {
			case '\\':
				j++
			case '`':
				return j
			}
		}
		return len(rs) - 1
	}
	if i+1 >= len(rs) {
		return i
	}
	switch open := rs[i+1]; open {
	case '(', '{':
		closer := map[rune]rune{'(': ')', '{': '}'}[open]
		depth := 0
		for j := i + 1; j < len(rs); j++ {
			switch rs[j] {
			case '\\':
				j++
			case '\'':
				j++
				for j < len(rs) && rs[j] != '\'' {
					j++
				}
			case open:
				depth++
			case closer:
				depth--
				if depth == 0 {
					return j
				}
			}
		}
		return len(rs) - 1
	default:
		j := i + 1
		switch r := rs[j]; {
		case unicode.IsDigit(r) || strings.ContainsRune("?@#*$!-", r):
			return j // $1, $?, $@ …
		case r != '_' && !unicode.IsLetter(r):
			return i // a lone $
		}
		for j+1 < len(rs) && (rs[j+1] == '_' || unicode.IsLetter(rs[j+1]) || unicode.IsDigit(rs[j+1])) {
			j++
		}
		return j
	}
}
