package apexv1

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// OQ-SKIP (v1.4, D499, D561 B5): a test harness that cannot set up what
// it needs often skips every test and reports success — exflowpay's
// TestMain over a broken migration, a Docker-less host, a VM run of a
// browser suite. The vacuous rule (H11) catches a runner that says it ran
// nothing; this catches one that ran only skips. From a check's output ape
// reads the passed, skipped and failed counts the runner's own summary
// gives (measured per stack in V14-PLAN-APE P1):
//
//	go test -v            --- PASS: / --- SKIP: / --- FAIL: lines (plain go
//	                      test prints no counts: see goFlagsV)
//	go test -json         "Action":"pass"|"skip"|"fail" with a "Test"
//	gotestsum             DONE N tests, S skipped, F failures
//	pytest                N passed, S skipped, F failed … in 0.1s
//	cargo test            test result: … N passed; F failed; S ignored (per binary, summed)
//	node --test           ℹ pass N / ℹ skipped S / ℹ fail F
//	jest                  Tests: F failed, S skipped, N passed, T total
//	vitest                Tests  F failed | S skipped | N passed (T)
//	mocha                 N passing / S pending / F failing
//	flutter test, dart    the last progress line, 00:01 +N ~S -F: …
//
// Then, on a zero exit:
//
//   - 0 passed and at least one skipped is unrun (reason all-skipped):
//     nothing was tested, so it is never green;
//   - a check that names its tests (go test -run, a pytest node id, flutter
//     --plain-name or --name) and skipped any of them is unrun (reason
//     named-skip): the test it names did not run.

// TestCounts is what a runner's summary says ran.
type TestCounts struct {
	Passed  int    `json:"passed"`
	Skipped int    `json:"skipped"`
	Failed  int    `json:"failed"`
	Parser  string `json:"parser"`
}

// Skip reasons (both record ExitUnrun).
const (
	ReasonAllSkipped = "all-skipped"
	ReasonNamedSkip  = "named-skip"
)

var (
	goVerboseRe = regexp.MustCompile(`(?m)^\s*--- (PASS|SKIP|FAIL): `)
	goJSONRe    = regexp.MustCompile(`"Action":"(pass|skip|fail)","Package":"[^"]*","Test":`)
	gotestsumRe = regexp.MustCompile(`(?m)^DONE (\d+) tests?(?:, (\d+) skipped)?(?:, (\d+) failures?)?`)
	pytestRe    = regexp.MustCompile(`(?m)^(?:=+ )?((?:\d+ (?:passed|failed|skipped|errors?|xfailed|xpassed|deselected|warnings?)(?:, )?)+) in [\d.]+s`)
	pytestTokRe = regexp.MustCompile(`(\d+) (passed|failed|skipped|errors?)`)
	cargoRe     = regexp.MustCompile(`(?m)^test result: \w+\. (\d+) passed; (\d+) failed; (\d+) ignored;`)
	nodePassRe  = regexp.MustCompile(`(?m)^ℹ pass (\d+)$`)
	nodeSkipRe  = regexp.MustCompile(`(?m)^ℹ skipped (\d+)$`)
	nodeFailRe  = regexp.MustCompile(`(?m)^ℹ fail (\d+)$`)
	jestRe      = regexp.MustCompile(`(?m)^Tests:\s+(.*\d+ total)\s*$`)
	vitestRe    = regexp.MustCompile(`(?m)^\s*Tests\s+(.+?)\s+\(\d+\)\s*$`)
	countTokRe  = regexp.MustCompile(`(\d+) (passed|failed|skipped|todo)`)
	mochaRe     = regexp.MustCompile(`(?m)^\s*(\d+) (passing|pending|failing)\b`)
	flutterRe   = regexp.MustCompile(`(?m)^\d+:\d+ \+(\d+)(?: ~(\d+))?(?: -(\d+))?: `)
)

// testCounts reads the counts of the first runner whose summary out has,
// in the order above; ok is false when none does.
func testCounts(out string) (TestCounts, bool) {
	out = strings.ReplaceAll(ansiRe.ReplaceAllString(out, ""), "\r", "\n")
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	var c TestCounts
	if ms := gotestsumRe.FindAllStringSubmatch(out, -1); ms != nil {
		m := ms[len(ms)-1]
		c = TestCounts{Skipped: atoi(m[2]), Failed: atoi(m[3]), Parser: "gotestsum"}
		c.Passed = atoi(m[1]) - c.Skipped - c.Failed
		return c, true
	}
	if ms := goJSONRe.FindAllStringSubmatch(out, -1); ms != nil {
		c.Parser = "go test -json"
		for _, m := range ms {
			c.add(map[string]string{"pass": "passed", "skip": "skipped", "fail": "failed"}[m[1]], 1)
		}
		return c, true
	}
	if ms := goVerboseRe.FindAllStringSubmatch(out, -1); ms != nil {
		c.Parser = "go test -v"
		for _, m := range ms {
			c.add(map[string]string{"PASS": "passed", "SKIP": "skipped", "FAIL": "failed"}[m[1]], 1)
		}
		return c, true
	}
	if ms := cargoRe.FindAllStringSubmatch(out, -1); ms != nil {
		c.Parser = "cargo test"
		for _, m := range ms {
			c.Passed += atoi(m[1])
			c.Failed += atoi(m[2])
			c.Skipped += atoi(m[3])
		}
		return c, true
	}
	if m := nodePassRe.FindStringSubmatch(out); m != nil {
		c = TestCounts{Passed: atoi(m[1]), Parser: "node --test"}
		if s := nodeSkipRe.FindStringSubmatch(out); s != nil {
			c.Skipped = atoi(s[1])
		}
		if f := nodeFailRe.FindStringSubmatch(out); f != nil {
			c.Failed = atoi(f[1])
		}
		return c, true
	}
	for _, j := range []struct {
		re     *regexp.Regexp
		parser string
	}{{jestRe, "jest"}, {vitestRe, "vitest"}} {
		if ms := j.re.FindAllStringSubmatch(out, -1); ms != nil {
			c.Parser = j.parser
			for _, t := range countTokRe.FindAllStringSubmatch(ms[len(ms)-1][1], -1) {
				c.add(t[2], atoi(t[1]))
			}
			return c, true
		}
	}
	if ms := pytestRe.FindAllStringSubmatch(out, -1); ms != nil {
		c.Parser = "pytest"
		for _, t := range pytestTokRe.FindAllStringSubmatch(ms[len(ms)-1][1], -1) {
			c.add(t[2], atoi(t[1]))
		}
		return c, true
	}
	if ms := mochaRe.FindAllStringSubmatch(out, -1); ms != nil {
		c.Parser = "mocha"
		for _, m := range ms {
			c.add(map[string]string{"passing": "passed", "pending": "skipped", "failing": "failed"}[m[2]], atoi(m[1]))
		}
		return c, true
	}
	if ms := flutterRe.FindAllStringSubmatch(out, -1); ms != nil {
		m := ms[len(ms)-1]
		return TestCounts{Passed: atoi(m[1]), Skipped: atoi(m[2]), Failed: atoi(m[3]), Parser: "flutter test"}, true
	}
	return TestCounts{}, false
}

func (c *TestCounts) add(kind string, n int) {
	switch kind {
	case "passed":
		c.Passed += n
	case "skipped", "todo":
		c.Skipped += n
	case "failed", "error", "errors":
		c.Failed += n
	}
}

// namesTests reports a check command that selects its tests by name: go
// test -run, a pytest node id (path::test), flutter/dart --plain-name or
// --name.
var namesTestsRe = regexp.MustCompile(`(?:^|\s)(?:-run[= ]|-test\.run[= ]|--plain-name[= ]|--name[= ]|-k[= ])|\S+\.py::\S+`)

// skipVerdict is the unrun reason and detail for a zero-exit output whose
// counts say nothing was tested, "" when they do not.
func skipVerdict(cmd string, c TestCounts) (reason, detail string) {
	switch {
	case c.Passed == 0 && c.Skipped > 0 && c.Failed == 0:
		return ReasonAllSkipped, "exit 0, but every test it ran was skipped (" + strconv.Itoa(c.Skipped) + " skipped, 0 passed, by " + c.Parser +
			"'s summary): nothing was tested — a harness that cannot set up what it needs should fail, not skip (APE_GATE=1 is set)"
	case c.Skipped > 0 && namesTestsRe.MatchString(cmd):
		return ReasonNamedSkip, "exit 0, but the check names its tests and " + strconv.Itoa(c.Skipped) + " of them was skipped (" + c.Parser +
			"'s summary): the test it names did not run"
	}
	return "", ""
}

// goFlagsV is the environment that makes a check's go test print its
// tests (D563, Q8): for a command that is one `go test` invocation — no
// pipeline, no chain — GOFLAGS with -v appended, never replaced; nil for
// any other command, or one -v already reaches. Plain go test prints only
// `ok pkg`, which hides a skip.
func goFlagsV(cmd string) []string {
	cmds := shellCommands(cmd)
	if len(cmds) != 1 || strings.ContainsAny(cmd, "|;&\n`") || strings.Contains(cmd, "$(") {
		return nil
	}
	words := cmds[0]
	for len(words) > 0 && assignRe.MatchString(words[0]) {
		if strings.HasPrefix(words[0], "GOFLAGS=") {
			return nil // the command sets its own
		}
		words = words[1:]
	}
	if len(words) < 2 || words[0] != "go" || words[1] != "test" {
		return nil
	}
	for _, w := range words[2:] {
		if w == "-v" || w == "-json" || strings.HasPrefix(w, "-v=") || strings.HasPrefix(w, "-test.v") {
			return nil
		}
	}
	cur := os.Getenv("GOFLAGS")
	for f := range strings.FieldsSeq(cur) {
		if f == "-v" || strings.HasPrefix(f, "-v=") {
			return nil
		}
	}
	return []string{"GOFLAGS=" + strings.TrimSpace(cur+" -v")}
}

// CodePassDrop: verify's notice for a green check that passed far fewer
// tests than at the change's last verify.
const CodePassDrop = "check.pass-drop"

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodePassDrop, 0, "apply", "a notice, never a finding (verify's notices; the exit is unchanged): a green check's runner passed fewer than half the tests it passed, green, at the change's last verify (at least 4 then; v1.4, OQ-SKIP) — tests skipped or no longer run read green; look at what stopped running before relying on the result"},
	)
}

// passDropMin is the fewest passed tests a drop is judged from.
const passDropMin = 4

// passDropNotices are check.pass-drop notices: each check of now that is
// green with fewer than half the tests passed it passed green in prev.
func passDropNotices(prev *Verify, now []CheckResult) []Diagnostic {
	if prev == nil {
		return nil
	}
	before := map[string]int{}
	for _, r := range prev.Results {
		if r.Exit == 0 && r.Tests != nil && r.Tests.Passed >= passDropMin {
			before[r.Task] = r.Tests.Passed
		}
	}
	var out []Diagnostic
	for _, r := range now {
		was, ok := before[r.Task]
		if !ok || r.Exit != 0 || r.Tests == nil || r.Tests.Passed*2 >= was {
			continue
		}
		out = append(out, notice(Diagnostic{Code: CodePassDrop, Message: r.Task + " is green but passed " + strconv.Itoa(r.Tests.Passed) +
			" test(s), against " + strconv.Itoa(was) + " at the last verify (" + r.Tests.Parser + ") — " +
			strconv.Itoa(r.Tests.Skipped) + " skipped now: find what stopped running before relying on it"}))
	}
	return out
}
