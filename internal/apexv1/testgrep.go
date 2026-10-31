package apexv1

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// CodeCheckGrepTestOutput: a notice — a check command that pipes a test
// runner's output into something else, in one of the shapes the v1.4 eval
// saw apex-change write (D620): the pipeline's exit is not the test's, a
// grep for go test's PASS line anchored with `$` that can never match, or a
// PASS-line grep whose test name is unanchored. Warn-only (D637 A2), like
// check.tag-mismatch: an error needs a measurement first.
const CodeCheckGrepTestOutput = "check.grep-test-output"

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeCheckGrepTestOutput, 0, ActionNone, "a notice, never a finding, and informational: its action is empty (change check's, start's and verify's notices; the exit is unchanged; v1.4.1, D620, D637): " +
			"one of the change's own checks (a task's, or a scenario of its delta) pipes a test runner's output on, in a shape that misjudges the tests — " +
			"the pipeline's exit is its last command's, so a failing test can pass the check (no `set -o pipefail`, no PIPESTATUS); " +
			"a grep for go test's PASS line ending in `$`, which never matches the `(0.00s)` go test prints after the name, so the check is never green; " +
			"a PASS-line grep whose test name is unanchored, which also matches a longer name and every subtest; " +
			"or (v1.4.1, FINDING-D2-1) `grep -q` or `head` after the runner under `set -o pipefail`, which exits 141 on a pass. " +
			"The fix is apex-change's, at its next re-plan of the change: run the test by name and use its exit (`go test -run '^TestX$' ./pkg`), or capture the output first"},
	)
}

// testGrepNotices are the check.grep-test-output notices for change c's
// own checks: its task checks and its delta's scenario checks.
func (p *Project) testGrepNotices(c *Change, file string) []Diagnostic {
	out := []Diagnostic{}
	seen := map[string]bool{} // a command shared by a task and a scenario: once
	for _, t := range c.CommandTasks() {
		if seen[t.Check] {
			continue
		}
		seen[t.Check] = true
		for _, why := range testGrepShapes(t.Check) {
			out = append(out, notice(Diagnostic{
				Code: CodeCheckGrepTestOutput, File: file, Line: t.Line,
				Message: t.ID + "'s check `" + t.Check + "` " + why,
			}))
		}
	}
	specs := p.EffectiveSpecChecks(c)
	for i := range specs {
		pc := &specs[i]
		if !pc.Delta || seen[pc.Cmd] {
			continue
		}
		seen[pc.Cmd] = true
		for _, why := range testGrepShapes(pc.Cmd) {
			out = append(out, notice(Diagnostic{
				Code: CodeCheckGrepTestOutput, File: file, Line: pc.Line,
				Message: "scenario " + pc.Ref + "'s check `" + pc.Cmd + "` " + why,
			}))
		}
	}
	return out
}

// testGrepShapes says what is wrong with a check command, one sentence per
// shape (empty: none). Each sentence ends with the fix.
func testGrepShapes(cmd string) []string {
	var out []string
	setPipefail := false
	for _, words := range shellCommands(cmd) {
		if len(words) > 1 && words[0] == "set" && strings.Contains(strings.Join(words[1:], " "), "pipefail") {
			setPipefail = true
		}
	}
	pipefail := setPipefail || strings.Contains(cmd, "PIPESTATUS")
	for _, pl := range shellPipelines(cmd) {
		runner := -1
		for i, words := range pl {
			if r := testCommand(words); r != "" && runner < 0 {
				runner = i
			}
		}
		if runner < 0 || runner == len(pl)-1 {
			continue
		}
		name := strings.Join(bareCommand(pl[runner]), " ")
		if !pipefail {
			out = append(out, fmt.Sprintf("pipes `%s` into `%s`, so the check's exit is the last command's, not the tests' — "+
				"a failing test can pass the check: run the tests by name and use their exit (`go test -run '^TestX$' ./pkg`), "+
				"or capture the output first (`out=$(go test -v ./pkg) && printf '%%s\\n' \"$out\" | grep -q …`)",
				name, filepath.Base(bareCommand(pl[len(pl)-1])[0])))
		}
		if setPipefail {
			if early := earlyExit(pl[runner+1:]); early != "" {
				out = append(out, fmt.Sprintf("pipes `%s` into `%s` under pipefail: `%s` stops reading at its first match, `%s` then dies of SIGPIPE, "+
					"and the check exits 141 although the tests passed — run the tests by name and use their exit (`go test -run '^TestX$' ./pkg`), "+
					"or capture the output first (`out=$(go test -v ./pkg) && printf '%%s\\n' \"$out\" | grep -q …`)",
					name, early, early, name))
			}
		}
		goTest := testCommand(pl[runner]) == "go test"
		for _, words := range pl[runner+1:] {
			pat, ok := grepPattern(words)
			if !ok {
				continue
			}
			switch {
			case goTest && passLineEndAnchored.MatchString(pat):
				out = append(out, "greps go test's PASS line with `"+pat+"`, which can never match: go test prints the line with the test's time after the name (`--- PASS: TestX (0.00s)`), so the check is never green — "+
					"run the test by name and use its exit (`go test -run '^TestX$' ./pkg`)")
			case passLineUnanchored.MatchString(pat):
				out = append(out, "greps `"+pat+"`, whose test name is not anchored: it also matches a longer name and every subtest (`TestX` matches `TestXY`, `TestX/case`) — "+
					"end the name with a space (`--- PASS: TestX `), or run the test by name and use its exit (`go test -run '^TestX$' ./pkg`)")
			}
		}
	}
	return out
}

var (
	// passLineEndAnchored: a PASS-line pattern that ends right after a name, anchored.
	passLineEndAnchored = regexp.MustCompile(`PASS:?\s+\^?[A-Za-z_][\w/.-]*\$$`)
	// passLineUnanchored: a PASS-line pattern that ends right after a name.
	passLineUnanchored = regexp.MustCompile(`PASS:?\s+[A-Za-z_][\w/.-]*$`)
)

// testCommand names the test runner a command runs ("go test", "pytest", …),
// or "" when it runs none.
func testCommand(words []string) string {
	w := bareCommand(words)
	if len(w) == 0 {
		return ""
	}
	base := filepath.Base(w[0])
	sub := ""
	if len(w) > 1 {
		sub = w[1]
	}
	switch base {
	case "go", "cargo", "flutter", "dart", "dotnet", "mvn", "mvnw", "gradle", "gradlew", "deno", "bun":
		if sub == "test" {
			return base + " test"
		}
	case "npm", "pnpm", "yarn":
		if sub == "test" || sub == "t" || (sub == "run" && len(w) > 2 && strings.HasPrefix(w[2], "test")) {
			return base + " test"
		}
	case "pytest", "gotestsum", "jest", "vitest", "rspec", "phpunit", "ctest":
		return base
	case "python", "python3":
		if sub == "-m" && len(w) > 2 && (w[2] == "pytest" || w[2] == "unittest") {
			return "python -m " + w[2]
		}
	}
	return ""
}

// bareCommand drops a command's leading VAR=value assignments and the
// wrappers that run what follows them (env, timeout, nice, …).
func bareCommand(words []string) []string {
	for len(words) > 0 {
		base := filepath.Base(words[0])
		switch {
		case assignRe.MatchString(words[0]):
			words = words[1:]
		case wrappers[base]:
			words = words[1:]
			for len(words) > 0 && strings.HasPrefix(words[0], "-") {
				if wrapperValueOpts[base][words[0]] && len(words) > 1 {
					words = words[1:]
				}
				words = words[1:]
			}
			if base == "timeout" && len(words) > 0 {
				words = words[1:]
			}
		default:
			return words
		}
	}
	return words
}

// grepOptsWithValue are grep's options that take the next word.
var grepOptsWithValue = map[string]bool{"-e": true, "-f": true, "-m": true, "-A": true, "-B": true, "-C": true, "--regexp": true, "--file": true, "--max-count": true}

// grepPattern is a grep (or rg) command's pattern, when it is one.
func grepPattern(words []string) (string, bool) {
	w := bareCommand(words)
	if len(w) == 0 {
		return "", false
	}
	switch filepath.Base(w[0]) {
	case "grep", "egrep", "fgrep", "rg":
	default:
		return "", false
	}
	for i := 1; i < len(w); i++ {
		a := w[i]
		switch {
		case a == "-e" || a == "--regexp":
			if i+1 < len(w) {
				return w[i+1], true
			}
			return "", false
		case strings.HasPrefix(a, "--regexp="):
			return strings.TrimPrefix(a, "--regexp="), true
		case a == "--":
			if i+1 < len(w) {
				return w[i+1], true
			}
			return "", false
		case grepOptsWithValue[a]:
			i++
		case strings.HasPrefix(a, "-") && len(a) > 1:
		default:
			return a, true
		}
	}
	return "", false
}

// earlyExit names the first command after a test runner that stops reading
// its input early — `grep -q` (or --quiet/--silent, -m), `head` — "" when
// none does. Under pipefail the runner then dies of SIGPIPE (FINDING-D2-1).
func earlyExit(after [][]string) string {
	for _, words := range after {
		w := bareCommand(words)
		if len(w) == 0 {
			continue
		}
		switch filepath.Base(w[0]) {
		case "head":
			return "head"
		case "grep", "egrep", "fgrep":
			for _, a := range w[1:] {
				switch {
				case a == "--":
					return ""
				case a == "--quiet" || a == "--silent" || strings.HasPrefix(a, "--max-count"):
					return "grep " + a
				case len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.ContainsAny(a[1:], "qsm"):
					if strings.ContainsAny(a[1:], "qm") {
						return "grep " + a
					}
				}
			}
		}
	}
	return ""
}
