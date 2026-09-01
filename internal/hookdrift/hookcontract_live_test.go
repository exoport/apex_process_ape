package hookdrift

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLive_HookContract is hookdrift's own gate, and it exists because the
// package it tests could only ever speak about a corpus somebody else
// happened to leave behind.
//
// `ape doctor --only hooks.contract_drift` reads the hook-events.jsonl
// files ape wrote during past interactive runs. That makes the check
// observational: it is only as good as the project it is pointed at, and
// on a machine where nobody has run a pipeline in 30 days it reports
// "not verified" for ever. That is the documented behaviour and it is the
// honest one — but it means the gate guarding the completion gates had, on
// this repo's own developer machine, never once fired. A gate that cannot
// find evidence is indistinguishable from a gate that found none, and the
// whole reason hookdrift exists is that a protection believed present is
// worse than one known absent.
//
// So this test stops waiting for a corpus and produces one: it copies the
// testdata project to a temp dir, drives ONE real unattended Claude Code
// session through `ape prompt`, and judges the runlog that session wrote.
// It is reproducible on any developer machine with `claude` + auth, needs
// no pre-existing project, and is therefore something `make check-harness`
// can rely on rather than hope for.
//
// # The corpus has to be real
//
// The obvious shortcut — synthesise a hook-events.jsonl and assert the
// sweep reads it — is already covered by the hermetic tests in this
// package, and it cannot answer this question at all. It would test ape
// against ape's own idea of the payload shape, which is exactly the
// tautology the package was written to escape. The only corpus worth
// judging is one the installed Claude Code actually emitted, so this
// spawns the harness and reads what comes back.
//
// # Why the seed spawns a subagent
//
// The three gate-critical fields are not all reachable from a plain turn:
//
//	background_tasks  Stop         — any completed session emits this
//	tool_response     PostToolUse  — ONLY counted for the Agent tool
//	agent_id          SubagentStop — needs a subagent to have stopped
//
// A seed that merely answers a question produces one Stop and nothing
// else. Observation.OK() is true for a field seen zero times ("no evidence
// is not evidence of absence"), so such a run would report a clean green
// having verified one field out of three — the precise failure mode this
// test was added to close, reintroduced inside the fix. The seed therefore
// spawns exactly one subagent, which covers the Agent PostToolUse and the
// SubagentStop in a single cheap turn, and the assertions below treat a
// field with Seen == 0 as a BROKEN SEED rather than a pass.
//
// Not hermetic — needs `claude` on PATH, working auth, and network, and it
// costs one short Haiku session — so it is gated behind APE_CLAUDE_LIVE=1
// and never runs in `make test` or GitHub CI. Run it via `make check-hooks`.
func TestLive_HookContract(t *testing.T) {
	if os.Getenv("APE_CLAUDE_LIVE") != "1" {
		t.Skip("set APE_CLAUDE_LIVE=1 (needs claude on PATH + auth + network) to run the live hook contract gate")
	}
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude not on PATH")
	}
	wantVersion := claudeVersion(t, claudeBin)
	t.Logf("seeding a runlog with Claude Code at %s (%s)", claudeBin, wantVersion)

	apeBin := buildApe(t)

	// The seed is RETRIED while its corpus is inadequate, and that is not the
	// same as retrying a failing gate.
	//
	// Two of the three fields only exist if the session actually delegates:
	// `tool_response` is counted on Agent-tool PostToolUse and `agent_id` on
	// SubagentStop. Whether it delegates is the model's call, and a Haiku
	// session at effort `low` will sometimes just read the one file itself —
	// observed doing exactly that, 2 turns instead of 6, on a run that had
	// passed three times before it. Producing the corpus is the means here,
	// not the thing under test, so an attempt that fails to provoke the
	// events is a wasted setup, not a verdict.
	//
	// Drift is never retried. A field SEEN but absent fails on the first
	// attempt below, because retrying that would be re-rolling until the
	// answer is convenient — the precise thing this package exists to stop.
	var rep *Report
	for attempt := 1; attempt <= seedAttempts; attempt++ {
		project := seedProject(t)
		runSeed(t, apeBin, project)

		// Judge only what this run wrote: the window starts at the test, so
		// no stray corpus can contribute, and Judged must be the local harness.
		r, err := Observe(project, time.Now().Add(-1*time.Hour))
		require.NoError(t, err)
		require.True(t, r.Observed(), "the seeded session produced no hook events at all — "+
			"ape wrote no runlog, so the contract is NOT verified")
		require.Equal(t, 1, r.Scanned, "expected to judge exactly the one run just seeded")
		require.Equal(t, wantVersion, r.Judged,
			"the verdict must be about the Claude Code installed right now")
		t.Logf("attempt %d/%d verdict: %s", attempt, seedAttempts, r.Summary())

		rep = r
		if seedIsAdequate(r) {
			break
		}
		t.Logf("attempt %d did not delegate, so two fields have nothing to judge — reseeding", attempt)
	}

	// Two distinct failures, reported distinctly. Seen == 0 means the SEED
	// failed to provoke the event, so this run cannot speak for the field —
	// which Observation.OK() would otherwise wave through as a pass.
	for _, o := range rep.Observations {
		t.Run(o.Field, func(t *testing.T) {
			require.NotZerof(t, o.Seen,
				"no %s events after %d seeding attempt(s): the corpus cannot judge %q, so this "+
					"is a broken seed, NOT a passing contract. The session declined to delegate "+
					"every time, which past that many tries is a change in how Claude Code or the "+
					"model handles sub-agents — fix the seed prompt before trusting any green here.",
				o.Event, seedAttempts, o.Field)
			require.NotZerof(t, o.Present,
				"DRIFT: Claude Code %s emitted %d %s event(s), none carrying %q. The step-completion "+
					"gate reading it has gone silent — ape will report success on runs that did nothing.",
				rep.Judged, o.Seen, o.Event, o.Field)
		})
	}
}

// seedAttempts bounds the reseeding above. Three is enough for a step the
// model takes most of the time, and each attempt costs one short Haiku
// session — a gate that quietly spent ten of them would be its own problem.
const seedAttempts = 3

// seedIsAdequate reports whether a seeded run provoked every event the
// gate needs, i.e. whether its verdict can speak for all three fields.
func seedIsAdequate(r *Report) bool {
	for _, o := range r.Observations {
		if o.Seen == 0 {
			return false
		}
	}
	return true
}

// runSeed drives one unattended session in project.
//
// The prompt forbids doing the work directly, because that is the shortcut
// a cheap model takes: told to delegate a one-file read, it reads the file
// and answers, which is a perfectly good answer to the question asked and a
// useless corpus for this gate. Naming the refusal explicitly is what makes
// delegation the only way to comply.
func runSeed(t *testing.T, apeBin, project string) {
	t.Helper()
	const seed = "Delegate this task; do not do it yourself. Call the Task tool exactly once to " +
		"spawn one general-purpose subagent, and have THAT subagent read README.md in the current " +
		"directory and report back the text of its first heading. You must not read README.md " +
		"yourself — the delegation is the point of this exercise. When the subagent reports back, " +
		"tell me the heading and stop. Do not create, modify or delete any files."

	// Haiku keeps it to fractions of a cent; the timeouts are backstops for
	// a session that hangs rather than expected durations.
	cmd := exec.Command(apeBin, "prompt", seed,
		"--cwd", project,
		"--model", "haiku",
		"--effort", "low",
		"--idle-timeout", "5m",
		"--max-duration", "10m",
	)
	cmd.Dir = project
	out, err := cmd.CombinedOutput()
	t.Logf("ape prompt exited: %v\n%s", err, tail(string(out), 40))
	require.NoError(t, err, "the seeding session must complete via the Stop hook; "+
		"without a runlog there is nothing to judge")
}

// seedProject copies the testdata project into a temp dir. `ape prompt`
// refuses to run outside a project root (it needs _apex/config.yaml), and
// a copy keeps the session's blast radius off the real checkout.
func seedProject(t *testing.T) string {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "testdata", "apexproject"))
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(src, "_apex", "config.yaml"),
		"the seed needs a project root to run in")

	dst := t.TempDir()
	// CopyFS will not write into a non-empty dir, so seed a fresh subdir.
	dst = filepath.Join(dst, "project")
	require.NoError(t, os.CopyFS(dst, os.DirFS(src)))
	return dst
}

// buildApe compiles the binary under test once, so the seeding session
// runs the working tree's ape rather than whatever is installed on PATH.
func buildApe(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ape")
	build := exec.Command("go", "build", "-o", bin, "./cmd/ape")
	build.Dir = filepath.Join("..", "..")
	out, err := build.CombinedOutput()
	require.NoErrorf(t, err, "build ape: %s", out)
	return bin
}

// claudeVersionRe matches the version `claude --version` prints.
var claudeVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+`)

// claudeVersion returns the version in the form Report.Judged carries.
// runlog stamps harness.yaml with the whole `claude --version` line
// ("2.1.252 (Claude Code)"), and readClaudeVersion trims the suffix off
// again, so the comparable value is the bare semver.
func claudeVersion(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command(bin, "--version").Output()
	require.NoError(t, err)
	v := strings.TrimSpace(string(out))
	require.Regexpf(t, claudeVersionRe, v, "unexpected `claude --version` output %q", v)
	return strings.TrimSpace(strings.TrimSuffix(v, "(Claude Code)"))
}

// tail keeps test logs readable when a seeding session goes long.
func tail(s string, n int) string {
	ls := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(ls) <= n {
		return strings.Join(ls, "\n")
	}
	return "…\n" + strings.Join(ls[len(ls)-n:], "\n")
}
