package apexv1

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// D623: the test runners whose own listeners are in-process.
func TestTestRunner(t *testing.T) {
	for argv, want := range map[string]string{
		"/tmp/go-build1/b001/ledger.test -test.v": "go",
		"/usr/bin/python3 -m pytest -q":           "python",
		"pytest tests/":                           "python",
		"node --test":                             "node",
		"node /p/node_modules/.bin/jest":          "node",
		"/p/target/debug/deps/svc-1a2b":           "cargo",
		"dart test":                               "dart",
		"/tmp/go-build1/b001/exe/ledger serve":    "",
		"python3 -m uvicorn app:app":              "",
		"go test ./...":                           "",
	} {
		require.Equal(t, want, testRunner(splitArgv(argv)), argv)
	}
	require.True(t, isBrowser([]string{"/home/u/.cache/ms-playwright/chromium-1140/chrome-linux/chrome", "--headless"}))
	require.False(t, isBrowser([]string{"/usr/bin/python3"}))
}

func splitArgv(s string) []string { return strings.Fields(s) }

// D623/D624/D626: a (full) check seen in-process is check.tag-mismatch, an
// informational notice (no action, no remedy); anything that needed a
// service, a long run, an unknown runner, too few samples, a requires:
// entry, an unrun or an untagged check is not.
func TestTagMismatchNotices(t *testing.T) {
	inProc := &Observed{Samples: 4, Runner: "go", InProcessListeners: 2}
	full := PlannedCheck{Ref: "T3", Kind: KindTask, Cmd: "go test ./internal/srv/ -run TestShutdown", Full: true}
	res := CheckResult{Task: "T3", Exit: 0, Secs: 0.8, Observed: inProc}

	got := tagMismatchNotices([]PlannedCheck{full}, []CheckResult{res}, nil, 60)
	require.Len(t, got, 1)
	require.Equal(t, CodeTagMismatch, got[0].Code)
	require.Equal(t, ActionNone, got[0].Action, "informational: no action a loop could branch on (D626)")
	require.Equal(t, SeverityNotice, got[0].Severity)
	require.Contains(t, got[0].Message, "T3's check is tagged (full) but ran in 0.8s in-process (2 port(s), all the go test runner's own")
	require.Equal(t, "informational: no action needed; ape ran it as tagged, and the tag is the planner's choice", got[0].Fix)

	none := func(name string, pc PlannedCheck, r CheckResult, req Requires) {
		t.Helper()
		require.Empty(t, tagMismatchNotices([]PlannedCheck{pc}, []CheckResult{r}, req, 60), name)
	}
	with := func(f func(*CheckResult)) CheckResult {
		r := res
		o := *inProc
		r.Observed = &o
		f(&r)
		return r
	}
	untagged := full
	untagged.Full = false
	none("untagged", untagged, res, nil)
	none("a service", full, with(func(r *CheckResult) { r.Observed.Services = []string{"4412 /tmp/go-build/exe/ledger serve :8080"} }), nil)
	none("a container", full, with(func(r *CheckResult) { r.Observed.Containers = 1 }), nil)
	none("a browser", full, with(func(r *CheckResult) { r.Observed.Browser = true }), nil)
	none("an outside connection", full, with(func(r *CheckResult) { r.Observed.Outside = []string{"127.0.0.1:5432"} }), nil)
	none("a remote one", full, with(func(r *CheckResult) { r.Observed.Remote = []string{"93.184.216.34:443"} }), nil)
	none("long", full, with(func(r *CheckResult) { r.Secs = 75 }), nil)
	none("an unknown runner", full, with(func(r *CheckResult) { r.Observed.Runner = "" }), nil)
	none("one sample", full, with(func(r *CheckResult) { r.Observed.Samples = 1 }), nil)
	none("not observed", full, with(func(r *CheckResult) { r.Observed = nil }), nil)
	none("unrun", full, with(func(r *CheckResult) { r.Exit, r.Reason = ExitUnrun, ReasonUnrun }), nil)
	none("timed out", full, with(func(r *CheckResult) { r.Exit, r.Reason = ExitTimeout, ReasonIdle }), nil)
	none("a probe binds it", full, res, Requires{"pgready": {Kind: RequireProbe, Probe: "true", When: "TestShutdown", when: regexp.MustCompile("TestShutdown")}})
	none("an env it uses", PlannedCheck{Ref: "T3", Cmd: "DSN=$PG_DSN go test ./...", Full: true}, res, Requires{"PG_DSN": {Kind: RequireEnv}})
	require.Empty(t, tagMismatchNotices([]PlannedCheck{full}, []CheckResult{with(func(r *CheckResult) { r.Secs = 30 })}, nil, 20), "full_min_secs 20")
}

func TestChecksConfig_FullMinSecs(t *testing.T) {
	var c ChecksConfig
	require.InDelta(t, 60, c.FullMinSecsAt(), 0)
	c.FullMinSecs = 120
	require.InDelta(t, 120, c.FullMinSecsAt(), 0)
}
