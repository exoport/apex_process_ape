package apexv1

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// RN-1 (D312): radix's block opens `source .bingo/variables.env`; a block
// line that is exactly `source <file>` is written in its POSIX form, and
// the rewrite is listed. A source with more words, or inside a line, is
// left as written.
func TestExtractGates_SourceLineIsWrittenForSh(t *testing.T) {
	doc := "# Project Context\n\n## Code Validation\n\n```bash\n" +
		"source .bingo/variables.env\n" +
		"\"${SHELLCHECK}\" tools/ci/*.sh\n" +
		"for g in tools/ci/*.sh; do \"$g\" || exit 1; done\n" +
		"source ./env.sh extra\n" +
		"```\n\n```sh\nsource env.sh # the tools\nmake test\n```\n\n## Next\n"
	g := extractGates("development/project-context.md", doc)
	require.Equal(t, []string{
		"( set -e\n. .bingo/variables.env\n\"${SHELLCHECK}\" tools/ci/*.sh\nfor g in tools/ci/*.sh; do \"$g\" || exit 1; done\nsource ./env.sh extra\n)",
		". env.sh",
		"make test",
	}, g.Commands)
	require.Equal(t, []string{"`source .bingo/variables.env` → `. .bingo/variables.env`", "`source env.sh` → `. env.sh`"}, g.Rewrites)
}

// RN-1 (b): a 127 whose output says a bash/zsh builtin was not found gets
// a note naming it; a missing tool, or another exit, gets none.
func TestShellBuiltinNote(t *testing.T) {
	require.Equal(t, "source", shellBuiltinNotFound("sh: 2: source: not found\n"))
	require.Equal(t, "[[", shellBuiltinNotFound("x\nsh: 1: [[: not found\n"))
	require.Equal(t, "shopt", shellBuiltinNotFound("bash: line 3: shopt: command not found\n"))
	require.Empty(t, shellBuiltinNotFound("sh: 1: golangci-lint: not found\n"), "a missing tool is not a builtin")
	require.Contains(t, shellBuiltinNote(127, "sh: 2: source: not found"), "`source` is a bash/zsh builtin that /bin/sh here (dash) does not have")
	require.Contains(t, shellBuiltinNote(127, "sh: 2: source: not found"), "use `.`, its POSIX form")
	require.Contains(t, shellBuiltinNote(127, "sh: 1: [[: not found"), "write that line in POSIX sh")
	require.Empty(t, shellBuiltinNote(1, "sh: 2: source: not found"), "only a 127")
	require.Empty(t, shellBuiltinNote(127, "sh: 1: nope: not found"))
}

// shIsDash skips a test that needs a /bin/sh without source.
func shIsDash(t *testing.T) {
	t.Helper()
	if exec.Command("sh", "-c", "source /dev/null").Run() == nil {
		t.Skip("sh has source here: the RN-1 case needs a dash-like /bin/sh")
	}
}

// RN-1 (b): a check run by ape (sh -c) that uses source records the note.
func TestRunCheck_ShellBuiltinNote(t *testing.T) {
	shIsDash(t)
	dir := t.TempDir()
	r := RunCheck(ctx(), dir, "source /dev/null && echo ran", filepath.Join(dir, "c.log"), time.Minute)
	require.Equal(t, 127, r.Exit)
	require.Contains(t, r.ShellNote, "`source` is a bash/zsh builtin")
	data, err := os.ReadFile(filepath.Join(dir, "c.log"))
	require.NoError(t, err)
	require.Contains(t, string(data), "[ape] note: `source` is a bash/zsh builtin")
	r = RunCheck(ctx(), dir, "no-such-tool-xyz", filepath.Join(dir, "d.log"), time.Minute)
	require.Equal(t, 127, r.Exit)
	require.Empty(t, r.ShellNote)
}

// RN-1, radix's shape on a dash host: a v0 block that sources its tool
// env and runs guards in a loop. rc.7m lifted it as written: `source`
// exited 127 at the tag and after, cannot-run, and hid the guard the lift
// broke. Written as `. tools/env.sh` it runs: green at the tag, red after
// the lift (the guard reads the v0 team memory the lift moves).
func TestLift_SourceBlockRunsUnderSh(t *testing.T) {
	shIsDash(t)
	root := liftFixture(t, false)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "tools", "ci"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "tools", "env.sh"), []byte("GUARD_FILE=development/team-memory.md\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "tools", "ci", "guard.sh"), []byte("#!/bin/sh\ntest -f \"$GUARD_FILE\" || { echo \"guard: FAIL — $GUARD_FILE missing\"; exit 1; }\n"), 0o755))
	run(t, root, "add", "-A")
	root = liftWithGatesAt(t, root, "source tools/env.sh\nexport GUARD_FILE\nfor g in tools/ci/*.sh; do \"$g\" || exit 1; done")
	res, err := Lift(ctx(), root, LiftOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"`source tools/env.sh` → `. tools/env.sh`"}, res.Gates.Rewrites)
	require.Contains(t, res.Gates.Regression, "\n. tools/env.sh\n")
	require.Equal(t, 0, res.RegressionAtTag.Exit, "green at the tag: the block runs")
	require.Equal(t, 1, res.Regression.Exit, "red after the lift: the guard")
	require.Equal(t, LiftRegressionRedAfterLift, res.RegressionVerdict)
	var rep struct {
		Gates LiftGates `json:"gates"`
	}
	data, err := os.ReadFile(filepath.Join(root, "_apex", "state", LiftReport))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &rep))
	require.Equal(t, res.Gates.Rewrites, rep.Gates.Rewrites, "gates.rewrites in the report")
	disp, err := os.ReadFile(filepath.Join(root, "development", LiftDisposition))
	require.NoError(t, err)
	require.Contains(t, string(disp), "- written for /bin/sh (RN-1): `source tools/env.sh` → `. tools/env.sh` — ape runs checks with sh")
}
