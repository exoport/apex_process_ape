package apecmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// v1Repo is a git repo with `ape project init` run, a filled project.md
// and an approved M1.
func v1Repo(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("v1 check commands run with sh -c")
	}
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "t"}, {"GIT_AUTHOR_EMAIL", "t@t"}, {"GIT_COMMITTER_NAME", "t"}, {"GIT_COMMITTER_EMAIL", "t@t"}} {
		t.Setenv(kv[0], kv[1])
	}
	v1Git(t, root, "init", "-q")
	_, _, err := v1Exec(t, newProjectCmd, "init", "--cwd", root)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash("development/project.md")),
		[]byte("# G\n## Vision\nv\n## Users\nu\n## Scope\ns\n## Out of scope\no\n## Constraints\nc\n"), 0o644))
	_, _, err = v1Exec(t, newMilestoneCmd, "new", "Skeleton", "--gate", "./gate.sh", "--goal", "greets", "--cwd", root)
	require.NoError(t, err)
	_, _, err = v1Exec(t, newMilestoneCmd, "approve", "M1", "--by", "eval", "--cwd", root)
	require.NoError(t, err)
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "init")
	return root
}

func v1Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func v1Exec(t *testing.T, build func() *cobra.Command, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return v1ExecIn(t, build, "", args...)
}

func v1ExecIn(t *testing.T, build func() *cobra.Command, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := build()
	// As under the real root, which silences both for the whole tree.
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errb.String(), err
}

const v1ChangeMD = `---
id: add-wave
milestone: M1
touches: [greeting]
---
# Add wave
## Why
To wave.
## Out of scope
- farewells
## Delta
### ADDED [greeting] Requirement: Wave
The system SHALL wave.
#### Scenario: wave
- check: ./wave.sh
## Tasks
- T1 wave — check: ./wave.sh
`

func TestV1Commands_ExitCodes(t *testing.T) {
	root := v1Repo(t)

	_, _, err := v1Exec(t, newProjectCmd, "check", "--cwd", root)
	require.NoError(t, err)

	// usage: bad id → 2
	_, stderr, err := v1Exec(t, newChangeCmd, "new", "BAD", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.Contains(t, stderr, "Error: change id")

	_, _, err = v1Exec(t, newChangeCmd, "new", "add-wave", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err)

	// check failed → 1, findings listed on stderr
	_, stderr, err = v1Exec(t, newChangeCmd, "check", "add-wave", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	require.Contains(t, stderr, "## Delta is empty")

	// --json: the failure is a JSON object on stdout
	stdout, _, err := v1Exec(t, newChangeCmd, "check", "add-wave", "--json", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	var fail struct {
		OK       bool             `json:"ok"`
		Exit     int              `json:"exit"`
		Findings []apexv1.Finding `json:"findings"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &fail))
	require.False(t, fail.OK)
	require.Equal(t, 1, fail.Exit)
	require.NotEmpty(t, fail.Findings)

	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash("development/changes/add-wave/change.md")), []byte(v1ChangeMD), 0o644))
	_, _, err = v1Exec(t, newChangeCmd, "check", "add-wave", "--cwd", root)
	require.NoError(t, err)
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "plan")

	stdout, _, err = v1Exec(t, newChangeCmd, "start", "add-wave", "--json", "--cwd", root)
	require.NoError(t, err)
	var started apexv1.StartResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &started))
	require.Equal(t, apexv1.StatusInProgress, started.Status)

	// conflict → 3: a second change MODIFYing... here ADDing the same requirement
	require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.FromSlash("development/changes/add-wave-2")), 0o755))
	_, _, err = v1Exec(t, newChangeCmd, "new", "add-wave-2", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err), "the folder already exists")
	require.NoError(t, os.RemoveAll(filepath.Join(root, filepath.FromSlash("development/changes/add-wave-2"))))
	_, _, err = v1Exec(t, newChangeCmd, "new", "add-wave-2", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash("development/changes/add-wave-2/change.md")),
		[]byte(strings.Replace(v1ChangeMD, "id: add-wave", "id: add-wave-2", 1)), 0o644))
	_, stderr, err = v1Exec(t, newChangeCmd, "start", "add-wave-2", "--cwd", root)
	require.Equal(t, exitV1Conflict, exitCodeOf(t, err))
	require.Contains(t, stderr, "conflict: add-wave:")

	// verify red → 1, the failing tail on stdout
	stdout, _, err = v1Exec(t, newChangeCmd, "verify", "add-wave", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	require.Contains(t, stdout, "RED (exit 127)")

	require.NoError(t, os.WriteFile(filepath.Join(root, "wave.sh"), []byte("#!/bin/sh\necho wave\n"), 0o755))
	v1Git(t, root, "add", "wave.sh")
	v1Git(t, root, "commit", "-q", "-m", "feat: wave", "-m", "Change: add-wave")
	_, _, err = v1Exec(t, newChangeCmd, "verify", "add-wave", "--cwd", root)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash("development/changes/add-wave/review.md")), []byte("ok\n"), 0o644))
	_, _, err = v1Exec(t, newChangeCmd, "review", "add-wave", "--verdict", "pass", "--findings", "development/changes/add-wave/review.md", "--cwd", root)
	require.NoError(t, err)
	stdout, _, err = v1Exec(t, newChangeCmd, "archive", "add-wave", "--json", "--cwd", root)
	require.NoError(t, err)
	var arch apexv1.ArchiveResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &arch))
	require.Len(t, arch.Specs, 1)
	require.True(t, arch.Specs[0].Created)
	require.Equal(t, "archive: add-wave", v1Git(t, root, "log", "-1", "--format=%s"))

	// milestone gate red → 1; close after the gate goes green → 0
	_, _, err = v1Exec(t, newMilestoneCmd, "gate", "M1", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	require.NoError(t, os.WriteFile(filepath.Join(root, "gate.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	v1Git(t, root, "add", "gate.sh")
	v1Git(t, root, "commit", "-q", "-m", "feat: gate")
	_, _, err = v1Exec(t, newChangeCmd, "abandon", "add-wave-2", "--reason", "duplicate", "--cwd", root)
	require.NoError(t, err)
	stdout, _, err = v1Exec(t, newMilestoneCmd, "close", "M1", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "M1 status: done")
	require.Empty(t, v1Git(t, root, "status", "--porcelain"),
		"D11: after the whole lifecycle, every v1 command has committed what it wrote")

	stdout, _, err = v1Exec(t, newStatusCmd, "--brief", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "All milestones done.")
	require.Contains(t, stdout, "(2 archived)")

	_, _, err = v1Exec(t, newDecisionCmd, "new", "use-sh", "--cwd", root)
	require.NoError(t, err)
	_, _, err = v1Exec(t, newProjectCmd, "check", "--cwd", root)
	require.NoError(t, err, "a decision skeleton is a valid ADR")
}

func TestV1Commands_RefuseOutsideAProject(t *testing.T) {
	dir := t.TempDir()
	_, _, err := v1Exec(t, newChangeCmd, "verify", "x-y-z", "--cwd", dir)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	stdout, _, err := v1Exec(t, newStatusCmd, "--cwd", dir)
	require.NoError(t, err)
	require.Contains(t, stdout, "run `ape project init`")
}

func TestHookCommands(t *testing.T) {
	root := v1Repo(t)
	payload := func(m map[string]any) string {
		m["cwd"] = root
		b, err := json.Marshal(m)
		require.NoError(t, err)
		return string(b)
	}

	stdout, _, err := v1ExecIn(t, newHookCmd, payload(map[string]any{"session_id": "s", "hook_event_name": "SessionStart"}), "session-start")
	require.NoError(t, err)
	require.Contains(t, stdout, `"additionalContext"`)
	require.Contains(t, stdout, "Milestone M1")

	stdout, _, err = v1ExecIn(t, newHookCmd, payload(map[string]any{
		"session_id": "s", "hook_event_name": "PreToolUse", "tool_name": "Write",
		"tool_input": map[string]any{"file_path": filepath.Join(root, filepath.FromSlash("development/specs/x/spec.md"))},
	}), "pre-tool")
	require.NoError(t, err)
	require.Contains(t, stdout, `"permissionDecision":"deny"`)

	stdout, _, err = v1ExecIn(t, newHookCmd, payload(map[string]any{
		"session_id": "s", "hook_event_name": "PreToolUse", "tool_name": "Write",
		"tool_input": map[string]any{"file_path": filepath.Join(root, "main.go")},
	}), "pre-tool")
	require.NoError(t, err)
	require.Empty(t, stdout)

	stdout, _, err = v1ExecIn(t, newHookCmd, payload(map[string]any{"session_id": "s", "hook_event_name": "Stop"}), "stop")
	require.NoError(t, err)
	require.Empty(t, stdout, "nothing in progress: allow")

	// A payload that is not JSON never fails the session. (From the
	// project dir: with no payload cwd the hook falls back to its own.)
	t.Chdir(root)
	stdout, stderr, err := v1ExecIn(t, newHookCmd, "garbage", "stop")
	require.NoError(t, err)
	require.Empty(t, stdout)
	require.Contains(t, stderr, "allowing")
}

// The `ape task` half of D1, end to end on disk: the recorder consults
// the hook's verdict, counts a block as a deferral, and writes the
// verdicts into the run manifest.
func TestStopGateRecorder(t *testing.T) {
	root := v1Repo(t)
	_, _, err := v1Exec(t, newChangeCmd, "new", "add-wave", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash("development/changes/add-wave/change.md")), []byte(v1ChangeMD), 0o644))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "plan")
	_, _, err = v1Exec(t, newChangeCmd, "start", "add-wave", "--cwd", root)
	require.NoError(t, err)

	r := newStopGateRecorder(root)
	stopAt := time.Now()
	stdout, _, err := v1ExecIn(t, newHookCmd, `{"session_id":"s1","hook_event_name":"Stop","cwd":"`+root+`"}`, "stop")
	require.NoError(t, err)
	require.Contains(t, stdout, `"decision":"block"`)
	blocked, ok := r.gate(t.Context(), "s1", stopAt)
	require.True(t, ok)
	require.True(t, blocked)

	// The same record is not read twice: the next Stop waits for its own.
	require.NoError(t, os.WriteFile(filepath.Join(root, "wave.sh"), []byte("#!/bin/sh\necho wave\n"), 0o755))
	stopAt = time.Now()
	_, _, err = v1ExecIn(t, newHookCmd, `{"session_id":"s1","hook_event_name":"Stop","stop_hook_active":true,"cwd":"`+root+`"}`, "stop")
	require.NoError(t, err)
	blocked, ok = r.gate(t.Context(), "s1", stopAt)
	require.True(t, ok)
	require.False(t, blocked)

	runDir := filepath.Join(runlog.TasksRoot(root), "apex-apply", "20261015-120000-abcd")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "manifest.yaml"), []byte("schema_version: 2\nrun_id: 20261015-120000-abcd\n"), 0o644))
	require.NoError(t, os.Symlink(filepath.Base(runDir), filepath.Join(filepath.Dir(runDir), "latest")))
	recordStopGate(root, "apex-apply", runlog.TasksRoot(root), r)
	data, err := os.ReadFile(filepath.Join(runDir, "manifest.yaml"))
	require.NoError(t, err)
	var m pipeline.Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	require.NotNil(t, m.StopGate)
	require.Equal(t, 1, m.StopGate.Deferrals)
	require.Equal(t, 1, m.StopGate.Allowed)
	require.Equal(t, "add-wave", m.StopGate.Events[0].Change)
}

// No hook registered: the gate has no verdict to wait for and answers at
// once. Registered but the hook never ran for this Stop: no verdict after
// the appear window, and the Stop completes as before.
func TestStopGateRecorder_WithoutAVerdict(t *testing.T) {
	root := v1Repo(t)
	r := newStopGateRecorder(root)
	r.appear = 300 * time.Millisecond
	_, ok := r.gate(t.Context(), "s1", time.Now())
	require.False(t, ok, "settings register the hook but it never ran")
	require.Nil(t, r.record())

	require.NoError(t, os.Remove(filepath.Join(root, ".claude", "settings.json")))
	start := time.Now()
	_, ok = r.gate(t.Context(), "s1", time.Now())
	require.False(t, ok)
	require.Less(t, time.Since(start), 200*time.Millisecond, "no wait when no hook is registered")
}
