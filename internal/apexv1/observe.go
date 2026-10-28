package apexv1

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// What a check did while it ran (v1.4, D623/D624, C6): which processes
// listened on a port, whether one of them was a service apart from the
// test runner, the containers it created, a browser, and connections to a
// listener outside the check or off the machine. Sampled from the check's
// scope (or process group) on Linux (observe_linux.go); nothing is observed
// elsewhere.
//
// It answers one question about a check tagged (full) — "needs a service,
// a network or a browser, or it is long" — that the tag's text cannot: did
// this one need any of that? A (full) check that ran in-process (httptest,
// a server on 127.0.0.1:0 in the test binary, a signal test) is verify's
// notice check.tag-mismatch. It is informational: ape runs the check as
// tagged and nothing has to change. An untagged check that started a
// service is recorded only, never a notice: projects decide that direction
// differently (a throwaway-database harness kept untagged by a project's
// own ruling).

// Observed is what ape saw a check do while it ran (CheckResult.Observed).
type Observed struct {
	// Samples is how many times the check was looked at.
	Samples int `json:"samples"`
	// Runner is the test runner seen ("go", "python", "node", "cargo",
	// "dart"), "" when none ape knows was.
	Runner string `json:"runner,omitempty"`
	// Services are listening processes other than the test runner: "<pid>
	// <command> :<port>".
	Services []string `json:"services,omitempty"`
	// InProcessListeners counts the ports the test runner itself listened
	// on (httptest, 127.0.0.1:0).
	InProcessListeners int `json:"in_process_listeners,omitempty"`
	// Containers counts the containers created while it ran.
	Containers int  `json:"containers,omitempty"`
	Browser    bool `json:"browser,omitempty"`
	// Outside are loopback ports it connected to whose listener was not
	// the check's (a running dev stack); Remote, addresses off the machine.
	Outside []string `json:"outside,omitempty"`
	Remote  []string `json:"remote,omitempty"`
}

// Needs reports whether the check was seen needing what (full) is for: a
// service, a container, a browser, or a connection to something outside it.
func (o *Observed) Needs() bool {
	return o != nil && (len(o.Services) > 0 || o.Containers > 0 || o.Browser || len(o.Outside) > 0 || len(o.Remote) > 0)
}

// testRunner is the stack of the test runner argv runs, "" when it is none
// ape knows: the stacks whose counts ape reads (testcounts.go).
func testRunner(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	base := filepath.Base(argv[0])
	args := strings.Join(argv[1:], " ")
	switch {
	case strings.HasSuffix(base, ".test"):
		return "go"
	case base == "pytest" || base == "py.test":
		return "python"
	case strings.HasPrefix(base, "python") && (strings.Contains(args, "pytest") || strings.Contains(args, "unittest")):
		return "python"
	case base == "jest" || base == "vitest" || base == "mocha":
		return "node"
	case (base == "node" || base == "nodejs") && (slices.Contains(argv[1:], "--test") ||
		strings.Contains(args, "jest") || strings.Contains(args, "vitest") || strings.Contains(args, "mocha")):
		return "node"
	case strings.Contains(argv[0], "/target/") && strings.Contains(argv[0], "/deps/"):
		return "cargo"
	case base == "flutter_tester" || (base == "dart" || base == "flutter") && strings.Contains(args, "test"):
		return "dart"
	}
	return ""
}

// browserWords are the executables of a browser a check drives.
var browserWords = []string{"chrome", "chromium", "headless_shell", "firefox", "webkit", "msedge"}

// isBrowser reports a browser, or a browser driver (Playwright, Selenium).
func isBrowser(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	base := strings.ToLower(filepath.Base(argv[0]))
	for _, w := range browserWords {
		if strings.Contains(base, w) {
			return true
		}
	}
	all := strings.ToLower(strings.Join(argv, " "))
	return strings.Contains(all, "ms-playwright") || strings.Contains(all, "playwright/driver") || strings.Contains(base, "geckodriver") || strings.Contains(base, "chromedriver")
}

// CodeTagMismatch: verify's notice for a (full) check seen running
// in-process.
const CodeTagMismatch = "check.tag-mismatch"

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeTagMismatch, 0, ActionPerson, "a notice, never a finding, and informational (verify's notices; the exit is unchanged; v1.4, D623): a check tagged (full) ran in-process — under checks.full_min_secs (default 60 s), its runner recognised, and no service apart from the test runner, no container, no browser and no connection to a listener outside the check or off the machine seen, nor a requires: entry naming what it needs. ape runs the check as tagged and nothing has to change: no session acts on it. It tells a person the tag may be wider than the check; an untagged check that started a service is recorded in the result's observed, never a notice"},
	)
}

// tagMismatchNotices are check.tag-mismatch notices for checks (results,
// in the same order) that are tagged (full) and were seen running
// in-process for less than minSecs.
func tagMismatchNotices(checks []PlannedCheck, results []CheckResult, requires Requires, minSecs float64) []Diagnostic {
	var out []Diagnostic
	for i := range checks {
		if i >= len(results) || !checks[i].Full {
			continue
		}
		pc, r := &checks[i], &results[i]
		o := r.Observed
		if o == nil || o.Runner == "" || o.Samples < 2 || o.Needs() || r.Reason != "" ||
			r.Exit == exitNotFound || r.Exit == exitInterrupted || r.Secs >= minSecs || declaredNeed(pc.Cmd, requires) {
			continue
		}
		seen := "no port"
		if o.InProcessListeners > 0 {
			seen = strconv.Itoa(o.InProcessListeners) + " port(s), all the " + o.Runner + " test runner's own"
		}
		out = append(out, notice(Diagnostic{Code: CodeTagMismatch, Message: pc.Ref + "'s check is tagged (full) but ran in " +
			strconv.FormatFloat(r.Secs, 'f', 1, 64) + "s in-process (" + seen + "; no service, container, browser or outside connection seen)" +
			" — informational: ape ran it as tagged and nothing has to change; the tag may be wider than the check needs"}))
	}
	return out
}

// declaredNeed reports a requires: entry that applies to cmd — a probe
// that binds it, an environment variable it uses, a command it runs: what
// it needs is declared, seen or not.
func declaredNeed(cmd string, requires Requires) bool {
	for name, req := range requires {
		switch req.Kind {
		case RequireProbe:
			bound := containsWord(cmd, name)
			if req.when != nil {
				bound = req.when.MatchString(cmd)
			}
			if bound {
				return true
			}
		case RequireEnv:
			if mentionsVar(cmd, name) {
				return true
			}
		case RequireCmd:
			if invokes(cmd, name) {
				return true
			}
		}
	}
	return false
}
