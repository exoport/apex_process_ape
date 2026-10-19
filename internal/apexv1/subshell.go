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
// joined by &&, so each line's exit counts, as under set -e. A block with
// a line that is not a simple command runs as written: joined, a cd or an
// export would hold differently, and a construct would not parse.

var (
	// subshellStateRe is a command that changes the shell's own state for
	// the lines after it.
	subshellStateRe = regexp.MustCompile(`^(?:cd|pushd|popd|export|unset|set|source|\.|alias|unalias|trap|umask|ulimit|exec|shopt|eval|local|declare|readonly|typeset)$`)
	// subshellKeywordRe is a word that opens or closes a shell construct.
	subshellKeywordRe = regexp.MustCompile(`^(?:if|then|else|elif|fi|for|while|until|do|done|case|esac|select|function|\{|\})$`)
	// funcDefRe is a function definition: name() …
	funcDefRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]*\s*\(\s*\)`)
	// assignWordRe is a NAME=value word.
	assignWordRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// splitSubshell returns the commands of cmd's ( … ) block when cmd,
// trimmed, is exactly one such block spanning several lines and every
// line in it is a simple command: blank lines and full-line comments
// dropped, a trailing comment cut. For a block it does not split, refused
// says which line and why; both are empty for a command that is no such
// block, or a block of a single command.
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
	if len(t) < 2 || t[0] != '(' || t[len(t)-1] != ')' || strings.HasPrefix(t, "((") || !strings.Contains(t, "\n") || !oneBlock(t) {
		return nil, ""
	}
	for i, l := range strings.Split(t[1:len(t)-1], "\n") {
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

// oneBlock reports whether the ( that opens t closes at its last byte:
// ( a ⏎ b ) is one block, ( a ) ⏎ ( b ) is two. t has no comments.
func oneBlock(t string) bool {
	var quote byte
	depth := 0
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote != 0:
			switch c {
			case '\\':
				i++
			case quote:
				quote = 0
			}
		case c == '\\':
			i++
		case c == '\'' || c == '"' || c == '`':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth--; depth == 0 {
				return i == len(t)-1
			}
		}
	}
	return false
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
	segs, heredoc, balanced := shellSegments(t)
	switch {
	case !balanced:
		return "", "has unbalanced quotes or parentheses"
	case heredoc:
		return "", "opens a construct (a here-doc <<)"
	}
	for _, s := range segs {
		if why := segmentConstruct(s); why != "" {
			return "", why
		}
	}
	if len(segs) > 1 && strings.TrimSpace(segs[len(segs)-1]) == "" {
		return "", "is already joined to the next (it ends in &&, ||, ;, | or &)"
	}
	return t, ""
}

// shellSegments splits one line on its top-level &&, ||, ;, | and &
// (outside quotes and parentheses; >&, <& and &> are redirections), and
// reports a here-doc (<<, not the here-string <<<) and whether its quotes
// and parentheses balance.
func shellSegments(t string) (segs []string, heredoc, balanced bool) {
	var quote byte
	depth, from := 0, 0
	balanced = true
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote != 0:
			switch c {
			case '\\':
				i++
			case quote:
				quote = 0
			}
		case c == '\\':
			i++
		case c == '\'' || c == '"' || c == '`':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth == 0 {
				balanced = false
			} else {
				depth--
			}
		case c == '<' && strings.HasPrefix(t[i:], "<<<"):
			i += 2
		case c == '<' && strings.HasPrefix(t[i:], "<<"):
			heredoc = true
			i++
		case depth == 0 && c == '&' && (i > 0 && (t[i-1] == '>' || t[i-1] == '<') || i+1 < len(t) && t[i+1] == '>'):
			// a redirection: 2>&1, <&3, &>file
		case depth == 0 && (c == ';' || c == '|' || c == '&'):
			segs = append(segs, t[from:i])
			if i+1 < len(t) && (t[i+1] == c || c == '|' && t[i+1] == '&') {
				i++
			}
			from = i + 1
		}
	}
	if quote != 0 || depth != 0 {
		balanced = false
	}
	return append(segs, t[from:]), heredoc, balanced
}

// segmentConstruct says what makes one command of a line no simple
// command — it opens or closes a construct, defines a function or changes
// the shell's state — or "".
func segmentConstruct(s string) string {
	s = strings.TrimSpace(s)
	if funcDefRe.MatchString(s) {
		return "opens a construct (a function definition)"
	}
	words := strings.Fields(s)
	if len(words) > 0 && words[0] == "!" {
		words = words[1:]
	}
	// NAME=value words before a command set its environment only; with no
	// command after them they set the shell's.
	rest := strings.TrimSpace(strings.TrimPrefix(s, "!"))
	for len(words) > 0 && assignWordRe.MatchString(words[0]) {
		end := wordEnd(rest)
		rest = strings.TrimSpace(rest[end:])
		words = strings.Fields(rest)
		if rest == "" {
			return "changes shell state (a bare NAME=value)"
		}
	}
	if len(words) == 0 {
		return ""
	}
	switch w := words[0]; {
	case subshellKeywordRe.MatchString(w):
		return fmt.Sprintf("opens a construct (%s)", w)
	case subshellStateRe.MatchString(w):
		return fmt.Sprintf("changes shell state (%s)", w)
	}
	return ""
}

// wordEnd is where s's first shell word ends: at unquoted whitespace.
func wordEnd(s string) int {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote != 0:
			switch c {
			case '\\':
				i++
			case quote:
				quote = 0
			}
		case c == '\\':
			i++
		case c == '\'' || c == '"' || c == '`':
			quote = c
		case c == ' ' || c == '\t':
			return i
		}
	}
	return len(s)
}

// checkScript is what a check command runs as: its ( … ) block's lines
// joined by && when it splits (R24b), else the command as written. note is
// the line its log opens with, "" when there is nothing to say.
func checkScript(cmd string) (script, note string) {
	cmds, refused := splitSubshell(cmd)
	switch {
	case len(cmds) > 0:
		script = strings.Join(cmds, " && ")
		return script, fmt.Sprintf("[ape] split into %d commands: the ( … ) block runs as its lines joined by &&, so each line's exit counts, as under set -e: %s", len(cmds), script)
	case refused != "":
		return cmd, "[ape] the ( … ) block runs as written, not split into commands: its " + refused
	}
	return cmd, ""
}
