package apecmd

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// F1c (D456, D419): a gate's output cut by tail still shows the verdict,
// the log and how to read the result again — the orchestrator that ran
// `ape milestone gate M5 --json | tail -c 1500` lost the verdict and ran an
// hour-long gate again to read it.

const gateLogM1 = "_apex/state/logs/gate-M1.log"

// lastLine is the last line of out (its trailing newline dropped).
func lastLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	return lines[len(lines)-1]
}

// tailC is `tail -c n` of out.
func tailC(out string, n int) string {
	if len(out) <= n {
		return out
	}
	return out[len(out)-n:]
}

// requireFramedJSON checks a gate's --json as bytes: exit and log the
// first keys, verdict the last, and the tail naming the log and the
// re-read route; it returns the object decoded.
func requireFramedJSON(t *testing.T, stdout string, exit int, verdictPrefix string) map[string]any {
	t.Helper()
	require.True(t, strings.HasPrefix(stdout, "{\n  \"exit\": "+strconv.Itoa(exit)+",\n  \"log\": \""+gateLogM1+"\",\n"), stdout)
	last := lastLine(strings.TrimSuffix(strings.TrimRight(stdout, "\n"), "}"))
	require.True(t, strings.HasPrefix(last, "  \"verdict\": \""+verdictPrefix), last)
	require.True(t, strings.HasSuffix(stdout, "\"\n}\n"), "verdict is the last key")
	tail := tailC(stdout, 300)
	require.Contains(t, tail, "log "+gateLogM1)
	require.Contains(t, tail, "re-read it with `ape status --json`")
	var obj map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &obj), stdout)
	require.InDelta(t, float64(exit), obj["exit"], 0)
	return obj
}

// requireVerdictLine checks a human gate output's last stdout line.
func requireVerdictLine(t *testing.T, stdout, prefix string) {
	t.Helper()
	last := lastLine(stdout)
	require.True(t, strings.HasPrefix(last, prefix), stdout)
	require.Contains(t, last, "— log "+gateLogM1+"; this result is recorded: re-read it with `ape status --json` (milestone.gate_last) or the log, not by running the gate again")
	tail := tailC(stdout, 300)
	require.Contains(t, tail, "log "+gateLogM1)
	require.Contains(t, tail, "not by running the gate again")
}

func TestGateVerdict_RedJSON(t *testing.T) {
	root, _, _ := gateRecallRepo(t)
	stdout, _, err := v1Exec(t, newMilestoneCmd, "gate", "M1", "--json", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	obj := requireFramedJSON(t, stdout, 1, "gate M1: red (exit 1) at ")
	// Every key the red result had is still there, with its value.
	for _, k := range []string{"ok", "error", "diagnostics", "result"} {
		require.Contains(t, obj, k)
	}
	require.Equal(t, false, obj["ok"])
	var red struct {
		Result struct {
			Run struct {
				Log string `json:"log"`
			} `json:"run"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &red))
	require.Equal(t, gateLogM1, red.Result.Run.Log)
	require.Len(t, obj, 7, "exit, log, verdict and the four it had")
}

func TestGateVerdict_GreenJSON(t *testing.T) {
	root, _, _ := gateRecallRepo(t)
	gateGreen(t, root, true)
	stdout, _, err := v1Exec(t, newMilestoneCmd, "gate", "M1", "--json", "--cwd", root)
	require.NoError(t, err)
	obj := requireFramedJSON(t, stdout, 0, "gate M1: green (exit 0) at ")
	for _, k := range []string{"milestone", "run", "status", "result_changed"} {
		require.Contains(t, obj, k)
	}
}

func TestGateVerdict_HumanGreenRed(t *testing.T) {
	root, _, _ := gateRecallRepo(t)
	gateGreen(t, root, true)
	stdout, _, err := v1Exec(t, newMilestoneCmd, "gate", "M1", "--cwd", root)
	require.NoError(t, err)
	requireVerdictLine(t, stdout, "gate M1: green (exit 0) at ")

	gateGreen(t, root, false)
	stdout, stderr, err := v1Exec(t, newMilestoneCmd, "gate", "M1", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	requireVerdictLine(t, stdout, "gate M1: red (exit 1) at ")
	require.Contains(t, stderr, "Error: M1 gate is red", "the error stays on stderr")
}

// A recalled result (F1) ends the same way: the human line last, and
// under --json "recorded" before the verdict, which stays last.
func TestGateVerdict_Recalled(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	_, _, err := v1Exec(t, newMilestoneCmd, "gate", "M1", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	unprinted(t, root)
	stdout, stderr, err := v1Exec(t, newMilestoneCmd, "gate", "M1", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	require.Contains(t, stderr, "not a new run")
	requireVerdictLine(t, stdout, "gate M1: red (exit 1) at ")

	_, _, err = v1Exec(t, newMilestoneCmd, "gate", "M1", "--json", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	unprinted(t, root)
	stdout, _, err = v1Exec(t, newMilestoneCmd, "gate", "M1", "--json", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	obj := requireFramedJSON(t, stdout, 1, "gate M1: red (exit 1) at ")
	require.Equal(t, true, obj["recorded"])
	require.Contains(t, stdout, "\n  \"recorded\": true,\n  \"verdict\": ")
	require.Equal(t, 3, gateRuns(), "approve, then the two calls that ran it")
}

func TestGateVerdict_CloseRedAndGreen(t *testing.T) {
	root, _, _ := gateRecallRepo(t)
	stdout, _, err := v1Exec(t, newMilestoneCmd, "close", "M1", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	last := lastLine(stdout)
	require.True(t, strings.HasPrefix(last, "close M1: gate red, not closed (exit 1) at "), stdout)
	require.Contains(t, last, "— log "+gateLogM1+"; this result is recorded: re-read it with `ape status --json` (milestones[].last_gate) or the log, not by running the close again")

	gateGreen(t, root, true)
	stdout, _, err = v1Exec(t, newMilestoneCmd, "close", "M1", "--json", "--cwd", root)
	require.NoError(t, err, stdout)
	require.True(t, strings.HasPrefix(stdout, "{\n  \"exit\": 0,\n  \"log\": \""+gateLogM1+"\",\n"), stdout)
	require.Contains(t, lastLine(strings.TrimSuffix(strings.TrimRight(stdout, "\n"), "}")), "  \"verdict\": \"close M1: done, gate green (exit 0) at ")
}

// lines splits a JSON text into its lines: key order is what these tests
// compare, which JSONEq ignores.
func lines(s string) []string { return strings.Split(s, "\n") }

// withRecorded keeps a trailing verdict last.
func TestWithRecorded_BeforeVerdict(t *testing.T) {
	require.Equal(t, []string{"{", `  "a": 1,`, `  "recorded": true,`, `  "verdict": "x"`, "}", ""},
		lines(withRecorded("{\n  \"a\": 1,\n  \"verdict\": \"x\"\n}\n")))
	// A nested "verdict" is no trailing key.
	require.Equal(t, []string{"{", `  "a": {`, `    "verdict": "x"`, "  },", `  "recorded": true`, "}", ""},
		lines(withRecorded("{\n  \"a\": {\n    \"verdict\": \"x\"\n  }\n}\n")))
}

// encodeFramed moves a map's framed keys, keeps the rest as v1Emit
// orders them, and nests values at the right indent.
func TestEncodeFramed(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, encodeFramed(&b, map[string]any{"b": 2, "exit": 1, "a": []int{1}},
		&jsonFrame{head: []jsonKV{{"exit", 7}}, tail: []jsonKV{{"z", map[string]int{"k": 1}}}}))
	require.Equal(t, []string{"{", `  "exit": 7,`, `  "a": [`, "    1", "  ],", `  "b": 2,`, `  "z": {`, `    "k": 1`, "  }", "}", ""}, lines(b.String()))
	b.Reset()
	require.NoError(t, encodeFramed(&b, map[string]any{"x": "<&>"}, nil))
	var c bytes.Buffer
	require.NoError(t, v1Emit(&c, &v1Flags{json: true}, map[string]any{"x": "<&>"}, nil))
	require.Equal(t, c.String(), b.String(), "unframed is v1Emit's encoding")
}
