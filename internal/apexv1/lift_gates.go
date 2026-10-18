package apexv1

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

// The gate commands a v0 project declared in project-context.md's
// `## Code Validation` become AGENTS.md's command list and the regression
// command (`regression:` in _apex/config.yaml, backlog 32). The extraction
// is mechanical and says what it could not take.

// LiftGates is what the lift took from `## Code Validation`.
type LiftGates struct {
	// Source is the file and line of the section, "" when there is none.
	Source   string   `json:"source,omitempty"`
	Commands []string `json:"commands,omitempty"`
	// Regression is the runnable commands joined with &&.
	Regression string `json:"regression,omitempty"`
	// NotRunnable are commands with a placeholder (<board>, {name}, a lone
	// …): listed in AGENTS.md, left out of the regression command.
	NotRunnable []string `json:"not_runnable,omitempty"`
}

var (
	codeSpanRe = regexp.MustCompile("`([^`]+)`")
	listItemRe = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(.*)$`)
	tableRowRe = regexp.MustCompile(`^\s*\|.*\|\s*$`)
	// A placeholder is <…>, {…} (not a shell ${…}) or a lone … — never the
	// ... of a Go package pattern like ./... (L3).
	placeholderRe  = regexp.MustCompile(`<[^<>\s][^<>]*>|\{\{?[A-Za-z_][^{}]*\}?\}|(?:^|\s)(?:…|\.{3})(?:\s|$)`)
	commandSpanRe  = regexp.MustCompile(`^(?:\./|[A-Za-z0-9_][A-Za-z0-9_.+-]*\s+\S)`)
	compoundLineRe = regexp.MustCompile(`^\s*(?:for|while|until|if|case|do|done|then|else|elif|fi|esac|\{|\})\b`)
	fileSpanRe     = regexp.MustCompile(`^[\w./-]+\.(?:md|ya?ml|json|txt|go|sh|py)$`)
)

// extractGates reads the `## Code Validation` section of doc (any heading
// level, case-insensitive) up to the next heading of its level or above.
// A fenced block is one unit: its lines joined, or kept as a block when
// they hold a shell compound (for … done); outside fences, a list item's
// or a table row's code spans that read as commands (two words, or a
// ./path) are taken. Duplicates are dropped, order kept.
func extractGates(file, doc string) LiftGates {
	var g LiftGates
	lines := splitLines(doc)
	start, level := -1, 0
	for i, l := range lines {
		if lv, t := headingLevel(l); lv > 0 && strings.EqualFold(strings.TrimSpace(t), "Code Validation") {
			start, level = i, lv
			g.Source = file + ":" + strconv.Itoa(l.N)
			break
		}
	}
	if start < 0 {
		return g
	}
	seen := map[string]bool{}
	add := func(c string) {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			return
		}
		seen[c] = true
		g.Commands = append(g.Commands, c)
		if hasPlaceholder(c) {
			g.NotRunnable = append(g.NotRunnable, c)
		}
	}
	var fence []string
	inFence := false
	flush := func() {
		var cmds []string
		compound := false
		cont := ""
		for _, l := range fence {
			t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "$ "))
			if !strings.HasPrefix(t, "#") {
				t = stripShellComment(t)
			}
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			if compoundLineRe.MatchString(t) {
				compound = true
			}
			if head, ok := strings.CutSuffix(t, "\\"); ok {
				cont += head + " "
				continue
			}
			cmds = append(cmds, cont+t)
			cont = ""
		}
		if compound {
			add("( " + strings.Join(cmds, "\n") + "\n)")
		} else {
			for _, c := range cmds {
				add(c)
			}
		}
		fence = nil
	}
	for _, l := range lines[start+1:] {
		t := strings.TrimSpace(l.Text)
		isFence := strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
		if isFence {
			if inFence {
				flush()
			}
			inFence = !inFence
			continue
		}
		if inFence {
			fence = append(fence, l.Text)
			continue
		}
		if lv, _ := headingLevel(l); lv > 0 && lv <= level {
			break
		}
		if !listItemRe.MatchString(l.Text) && !tableRowRe.MatchString(l.Text) {
			continue
		}
		for _, m := range codeSpanRe.FindAllStringSubmatch(l.Text, -1) {
			c := stripShellComment(strings.TrimSpace(m[1]))
			if commandSpanRe.MatchString(c) && !fileSpanRe.MatchString(c) {
				add(c)
			}
		}
	}
	var run []string
	for _, c := range g.Commands {
		if !hasPlaceholder(c) {
			run = append(run, c)
		}
	}
	g.Regression = strings.Join(run, " && ")
	return g
}

// hasPlaceholder reports a command that names something to fill in. A
// {…} right after a $ is the shell's parameter expansion, not one.
func hasPlaceholder(c string) bool {
	for _, m := range placeholderRe.FindAllStringIndex(c, -1) {
		if c[m[0]] == '{' && m[0] > 0 && c[m[0]-1] == '$' {
			continue
		}
		return true
	}
	return false
}

// stripShellComment drops a trailing ` # …` comment from one command
// line: a # that starts a word outside quotes. Joined with &&, a kept
// comment would swallow every command after it (L3: atomix's regression
// ran `make smoke` alone).
func stripShellComment(line string) string {
	var quote byte
	blank := false // the previous byte was unescaped, unquoted whitespace
	for i := 0; i < len(line); i++ {
		c := line[i]
		wasBlank := blank
		blank = false
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '\\':
			i++
		case c == '\'' || c == '"':
			quote = c
		case c == ' ' || c == '\t':
			blank = true
		case c == '#' && wasBlank:
			return strings.TrimRight(line[:i], " \t")
		}
	}
	return line
}

// withCommands puts the commands under AGENTS.md's `## Commands` heading
// (after a comment line right below it), or appends the section.
func withCommands(doc string, g LiftGates) string {
	if len(g.Commands) == 0 {
		return doc
	}
	var items []string
	for _, c := range g.Commands {
		if strings.Contains(c, "\n") {
			items = append(items, "- ```\n"+indent(c, "  ")+"\n  ```")
			continue
		}
		items = append(items, "- `"+c+"`")
	}
	block := strings.Join(items, "\n") + "\n"
	note := "<!-- lifted from " + g.Source + " (## Code Validation) at " + LiftTag + " -->\n"
	lines := strings.SplitAfter(doc, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "## Commands" {
			continue
		}
		at := i + 1
		if at < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[at]), "<!--") && strings.HasSuffix(strings.TrimSpace(lines[at]), "-->") {
			at++
		}
		out := strings.Join(lines[:at], "") + note + block + strings.Join(lines[at:], "")
		return out
	}
	if !strings.HasSuffix(doc, "\n") && doc != "" {
		doc += "\n"
	}
	return doc + "\n## Commands\n" + note + block
}

func indent(s, pre string) string {
	return pre + strings.ReplaceAll(s, "\n", "\n"+pre)
}

// Both runs red says nothing about whose red it is (R4): atomix's gotest
// was red at the tag for its environment (22 failures) and red after the
// lift with 25, three of them tests reading the deleted
// sprint-status.yaml, and the lift called all of it pre-existing. So the
// failing tests are compared by name (failingTests), for the runners
// whose report names them; otherwise the outputs' hashes are.

// liftNewFailuresShown is how many new failing names the result and the
// note carry; the count says the rest.
const liftNewFailuresShown = 50

// newFailures compares two red runs' outputs. With failing names read
// from the run after the lift, it returns those the tag's run did not
// have (named true); without, named is false and the caller compares the
// hashes.
func newFailures(beforeOut, afterOut string) (fresh []string, named bool) {
	after := failingTests(afterOut)
	if len(after) == 0 {
		return nil, false
	}
	return namesNotIn(after, failingTests(beforeOut)), true
}

// R17: spirometer's Flutter targets run `fvm flutter`, and fvm is on
// PATH only in an interactive shell: each target printed "fvm: command
// not found" and make's "*** [...] Error 127", make exited 2, and the lift
// called it red at the tag, which hid that nothing ran. For the lift's
// two comparison runs, a run whose output says a command was not found
// and that it exited 127 cannot run either, as a direct exit 126 or 127
// cannot. A change's verify and a milestone's gate keep their own rule.
var (
	// notFoundRe is a shell's or make's "not found": bash's and zsh's
	// "command not found", make's own exec "No such file or directory",
	// and dash's (/bin/sh on Debian and Ubuntu) "sh: 1: fvm: not found".
	notFoundRe = regexp.MustCompile(`(?m)command not found|No such file or directory|^\S*sh: \d+: [^:\n]+: not found\r?$`)
	// error127Re is make's report of a recipe line that exited 127 and
	// stopped it: "make: *** [Makefile:88: t] Error 127", not one a -
	// prefix let it go past ("Error 127 (ignored)").
	error127Re = regexp.MustCompile(`(?m)\bError 127\r?$`)
)

// liftCannotRun reports a comparison run that could not run here: exit
// 126 or 127, or red with out saying a command was not found and that
// make got 127 from it (R17). why says which, for the note. An output
// that names failing tests ran them (make -k went on past the 127): it is
// a red, judged by those names.
func liftCannotRun(r *CheckResult, out string) (cannot bool, why string) {
	switch {
	case r == nil || r.Exit == 0:
		return false, ""
	case r.Exit == 126 || r.Exit == 127:
		return true, ""
	}
	out = ansiRe.ReplaceAllString(out, "")
	if notFoundRe.MatchString(out) && error127Re.MatchString(out) && len(failingTests(out)) == 0 {
		return true, ", where its output says a command was not found and make reported Error 127"
	}
	return false, ""
}

// R13: bingo prints "(re)installing …/golangci-lint-v2.13.2" on a tool's
// first run, which is the run at the tag, and a duration differs on every
// run; spirometer's two logs differed only in such a line, and the lift
// said "the output changed". The lift's comparison hash drops both; the
// logs themselves, and a change's or milestone's regression hash, are
// untouched.
var (
	reinstallingRe = regexp.MustCompile(`^\(re\)installing `)
	// durationRe is a Go-style duration token: 0.123s, 12ms, 4m19s,
	// 1h2m3s, 250µs — a whole word, so TestG6s or v2.13.2 stay.
	durationRe = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h)(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m))*\b`)
)

// liftComparisonHash is the sha256 of a regression run's output with
// tool-install lines dropped and durations blanked (R13): what the lift
// compares two red runs by when no failing test names could be read.
func liftComparisonHash(out string) string {
	var b strings.Builder
	for l := range strings.SplitSeq(ansiRe.ReplaceAllString(out, ""), "\n") {
		l = strings.TrimRight(l, "\r")
		if reinstallingRe.MatchString(l) {
			continue
		}
		b.WriteString(durationRe.ReplaceAllString(l, ""))
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// R12: tenax's `regression:` is `make lint && make test && …`; lint was
// red at the tag, so make test ran at neither point and the two tests the
// lift broke were never seen. For its two comparison runs the lift runs
// each command of a top-level && chain on its own.

// splitTopLevelAnd splits cmd on the && outside quotes, ( ), $( ), { },
// ${ }, backticks and escapes; ;, || and pipes are left alone (a part keeps
// them). Unbalanced nesting or quotes (a case pattern's ")") split
// nothing. A part that changes the shell's own state (cd, export, source,
// an assignment, …) holds for every command after it, so the chain from
// it on stays one part. A command with no top-level && — or one whose
// split would leave an empty part — is the one part.
func splitTopLevelAnd(cmd string) []string {
	var parts []string
	depth := 0
	var quote byte
	from := 0
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote == '"' || quote == '`':
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
		case c == '(' || c == '{':
			depth++
		case c == ')' || c == '}':
			// A ) with nothing open (a case pattern) leaves the nesting
			// unknown: no split.
			if depth == 0 {
				return []string{cmd}
			}
			depth--
		case c == '&' && depth == 0 && i+1 < len(cmd) && cmd[i+1] == '&':
			parts = append(parts, strings.TrimSpace(cmd[from:i]))
			i++
			from = i + 1
		}
	}
	if len(parts) == 0 || depth != 0 || quote != 0 {
		return []string{cmd}
	}
	parts = append(parts, strings.TrimSpace(cmd[from:]))
	for i, p := range parts {
		if p == "" {
			return []string{cmd}
		}
		if changesShellState(p) {
			return append(parts[:i:i], strings.Join(parts[i:], " && "))
		}
	}
	return parts
}

var (
	shellStateRe = regexp.MustCompile(`^(?:cd|pushd|popd|export|source|\.|set|shopt|ulimit|umask|alias|unset|unalias|trap|eval|exec)(?:\s|$)`)
	assignmentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=\S*$`)
)

// changesShellState reports a command that changes what the commands
// after it in the same shell see: a builtin like cd or export, or a bare
// NAME=value.
func changesShellState(part string) bool {
	return shellStateRe.MatchString(part) || assignmentRe.MatchString(part)
}
