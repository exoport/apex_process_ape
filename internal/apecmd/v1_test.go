package apecmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

// v1Gate is v1Repo's M1 gate: red until done.flag exists.
const v1Gate = "#!/bin/sh\ntest -f done.flag\n"

// v1Repo is a git repo with `ape project init` run, a filled project.md
// and an approved M1.
func v1Repo(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the v1 fixtures are POSIX shell scripts run by path (./x.sh), not yet run on Windows; checks themselves run under sh -c on every OS")
	}
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	// A person at a terminal (person-only commands), whatever runs the tests.
	t.Setenv("APE_SESSION", "")
	t.Setenv("CLAUDECODE", "")
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
	// The gate exists and is red until the milestone's work is done:
	// approve runs it once (D70 B).
	require.NoError(t, os.WriteFile(filepath.Join(root, "gate.sh"), []byte(v1Gate), 0o755))
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
type: feat
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
	require.Equal(t, "change/add-wave", started.Branch, "D38: start checks out the change's branch")
	base := started.BaseBranch
	require.NotEmpty(t, base)

	// A new change is planned on the base branch: refused on a change's.
	_, stderr, err = v1Exec(t, newChangeCmd, "new", "add-wave-2", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	require.Contains(t, stderr, "git switch "+base)
	v1Git(t, root, "switch", "-q", base)

	// conflict → 3: a second change MODIFYing... here ADDing the same requirement
	require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.FromSlash("development/changes/add-wave-2")), 0o755))
	_, _, err = v1Exec(t, newChangeCmd, "new", "add-wave-2", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err), "the folder already exists")
	require.NoError(t, os.RemoveAll(filepath.Join(root, filepath.FromSlash("development/changes/add-wave-2"))))
	_, _, err = v1Exec(t, newChangeCmd, "new", "add-wave-2", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash("development/changes/add-wave-2/change.md")),
		[]byte(strings.Replace(v1ChangeMD, "id: add-wave", "id: add-wave-2", 1)), 0o644))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "plan: add-wave-2")
	_, stderr, err = v1Exec(t, newChangeCmd, "start", "add-wave-2", "--cwd", root)
	require.Equal(t, exitV1Conflict, exitCodeOf(t, err))
	require.Contains(t, stderr, "conflict: add-wave:")

	// Off its branch, verify refuses; start resumes the change there.
	_, stderr, err = v1Exec(t, newChangeCmd, "verify", "add-wave", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	require.Contains(t, stderr, "git switch change/add-wave")
	stdout, _, err = v1Exec(t, newChangeCmd, "start", "add-wave", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "resumed on change/add-wave")

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
	require.Equal(t, "feat: Add wave", v1Git(t, root, "log", "-1", "--format=%s"), "D38: one squash commit, titled by the change; D193: {type}: {title} by default")
	require.Equal(t, base, v1Git(t, root, "branch", "--show-current"))

	// milestone gate red → 1; close after the gate goes green → 0
	_, _, err = v1Exec(t, newMilestoneCmd, "gate", "M1", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	require.NoError(t, os.WriteFile(filepath.Join(root, "done.flag"), []byte("done\n"), 0o644))
	v1Git(t, root, "add", "done.flag")
	v1Git(t, root, "commit", "-q", "-m", "feat: done")
	_, _, err = v1Exec(t, newChangeCmd, "abandon", "add-wave-2", "--reason", "duplicate", "--cwd", root)
	require.NoError(t, err)
	// gate.sh was frozen at the approval: a changed one is refused until a
	// person approves it again (H2), and a green gate cannot be (D70 B).
	require.NoError(t, os.WriteFile(filepath.Join(root, "gate.sh"), []byte("#!/bin/sh\ntrue\n"), 0o755))
	stdout, _, err = v1Exec(t, newMilestoneCmd, "close", "M1", "--json", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	require.Contains(t, stdout, `"code": "milestone.gate-changed"`)
	require.Contains(t, stdout, `"action": "person"`)
	stdout, _, err = v1Exec(t, newMilestoneCmd, "approve", "M1", "--by", "eval", "--json", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	require.Contains(t, stdout, `"code": "milestone.gate-green"`)
	require.Contains(t, stdout, `"action": "replan"`)
	// A long run since OI-12: the self-check's progress goes to the run's
	// log (detached), its output to the approve log.
	require.FileExists(t, filepath.Join(root, "_apex", "state", "logs", "gate-M1-approve.log"))
	require.NoError(t, os.WriteFile(filepath.Join(root, "gate.sh"), []byte(v1Gate), 0o755))
	stdout, _, err = v1Exec(t, newMilestoneCmd, "close", "M1", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "M1 status: done")
	require.Empty(t, v1Git(t, root, "status", "--porcelain"),
		"D11: after the whole lifecycle, every v1 command has committed what it wrote")

	// Q162 (D350): a done milestone's gate is refused, exit 1, approve's
	// code and action; nothing is committed.
	head := v1Git(t, root, "rev-parse", "HEAD")
	stdout, _, err = v1Exec(t, newMilestoneCmd, "gate", "M1", "--json", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	require.Contains(t, stdout, `"code": "milestone.done"`)
	require.Contains(t, stdout, `"action": "stop"`)
	require.Equal(t, head, v1Git(t, root, "rev-parse", "HEAD"))
	require.Empty(t, v1Git(t, root, "status", "--porcelain"))
	// And --spec-only (W2) refuses it the same way.
	stdout, _, err = v1Exec(t, newMilestoneCmd, "gate", "M1", "--spec-only", "--json", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	require.Contains(t, stdout, `"code": "milestone.done"`)
	require.Contains(t, stdout, `"action": "stop"`)
	require.NoFileExists(t, filepath.Join(root, "_apex", "state", "logs", "gate-M1-spec.json"))
	require.Equal(t, head, v1Git(t, root, "rev-parse", "HEAD"))
	require.Empty(t, v1Git(t, root, "status", "--porcelain"))

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

// RK-3a (D297): under zsh the session-start context ends with the zsh
// paragraph, as valid JSON; under bash it is the brief alone.
func TestHookSessionStart_ZshParagraph(t *testing.T) {
	root := v1Repo(t)
	in := `{"session_id":"s","hook_event_name":"SessionStart","cwd":` + strconv.Quote(root) + `}`
	ctxOf := func(shell string) string {
		t.Setenv("CLAUDE_CODE_SHELL", "")
		t.Setenv("SHELL", shell)
		stdout, _, err := v1ExecIn(t, newHookCmd, in, "session-start")
		require.NoError(t, err)
		var got struct {
			HookSpecificOutput struct {
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		require.NoError(t, json.Unmarshal([]byte(stdout), &got), stdout)
		return got.HookSpecificOutput.AdditionalContext
	}
	bash, zsh := ctxOf("/bin/bash"), ctxOf("/usr/bin/zsh")
	require.Contains(t, bash, "Milestone M1")
	require.NotContains(t, bash, "run in zsh")
	require.Equal(t, strings.TrimRight(bash, "\n")+"\n\n"+apexv1.ZshNote+"\n", zsh)
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

	// Item 10: a Bash call that writes a tag is denied with PreToolUse
	// JSON; any other Bash call is allowed with no output; the session's
	// APE_ALLOW_TAG=1 lets it through.
	t.Setenv("APE_ALLOW_TAG", "")
	bash := func(command string) string {
		t.Helper()
		out, _, err := v1ExecIn(t, newHookCmd, payload(map[string]any{
			"session_id": "s", "hook_event_name": "PreToolUse", "tool_name": "Bash",
			"tool_input": map[string]any{"command": command},
		}), "pre-tool")
		require.NoError(t, err)
		return out
	}
	stdout = bash("go test ./... && git -C . tag -a v1 -m release")
	require.Contains(t, stdout, `"permissionDecision":"deny"`)
	require.Contains(t, stdout, "only a person tags or releases")
	require.Empty(t, bash("git tag --list && git log -1"))
	t.Setenv("APE_ALLOW_TAG", "1")
	require.Empty(t, bash("git tag v1"))
	// D45/D46: every git push and pkill/killall are denied too, unless the
	// session was launched with APE_ALLOW_PUSH=1 / APE_ALLOW_KILL=1.
	t.Setenv("APE_ALLOW_PUSH", "")
	t.Setenv("APE_ALLOW_KILL", "")
	stdout = bash("git push origin main")
	require.Contains(t, stdout, `"permissionDecision":"deny"`)
	require.Contains(t, stdout, "only a person pushes")
	stdout = bash("APE_ALLOW_KILL=1 pkill -f server")
	require.Contains(t, stdout, "stop the process you started by its PID (kill ")
	require.Empty(t, bash("kill 4242"))
	t.Setenv("APE_ALLOW_PUSH", "1")
	t.Setenv("APE_ALLOW_KILL", "1")
	require.Empty(t, bash("git push origin main && pkill -f server"))
	t.Setenv("APE_ALLOW_PUSH", "")
	t.Setenv("APE_ALLOW_KILL", "")

	// v1.3: a Monitor call's command (Claude Code 2.1.310+) is judged as a
	// Bash command is; a ws-only Monitor runs nothing and is allowed.
	t.Setenv("APE_ALLOW_TAG", "")
	monitor := func(input map[string]any) string {
		t.Helper()
		out, _, err := v1ExecIn(t, newHookCmd, payload(map[string]any{
			"session_id": "s", "hook_event_name": "PreToolUse", "tool_name": "Monitor", "tool_input": input,
		}), "pre-tool")
		require.NoError(t, err)
		return out
	}
	for cmd, reason := range map[string]string{
		"git push origin main":                  "only a person pushes",
		"git tag v1":                            "only a person tags or releases",
		"pkill -f server":                       "stop the process you started by its PID",
		`git commit --allow-empty -m "apex: x"`: "Only ape writes one",
	} {
		stdout = monitor(map[string]any{"command": cmd, "timeout_ms": 60000, "persistent": true})
		require.Contains(t, stdout, `"permissionDecision":"deny"`, cmd)
		require.Contains(t, stdout, reason, cmd)
	}
	require.Empty(t, monitor(map[string]any{"command": "until test -f done.txt; do sleep 2; done", "timeout_ms": 60000}))
	require.Empty(t, monitor(map[string]any{"ws": map[string]any{"url": "ws://127.0.0.1:9/events"}, "timeout_ms": 60000}))

	// Backlog 31: in a session ape task injected the hooks into, the
	// settings.json copy (no --inline) stands down; the inline one acts.
	t.Setenv("APE_ALLOW_TAG", "")
	t.Setenv("APE_V1_HOOKS", "inline")
	require.Empty(t, bash("git tag v1"), "the settings.json copy does nothing")
	stdout, _, err = v1ExecIn(t, newHookCmd, payload(map[string]any{
		"session_id": "s", "hook_event_name": "PreToolUse", "tool_name": "Bash",
		"tool_input": map[string]any{"command": "git tag v1"},
	}), "pre-tool", "--inline")
	require.NoError(t, err)
	require.Contains(t, stdout, `"permissionDecision":"deny"`)
	t.Setenv("APE_V1_HOOKS", "")

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

	t.Setenv("APE_SESSION", "task/test") // after v1Repo, which runs as a person: the hook blocks only in sessions ape launched (H13)
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

// Item 13: a --json failure carries diagnostics whose first code is the
// failure's own and comes with the exit the published list gives it.
func TestV1Commands_DiagnosticEnvelope(t *testing.T) {
	root := v1Repo(t)
	exitOf := map[string]int{}
	for _, c := range apexv1.DiagnosticCodes {
		exitOf[c.Code] = c.Exit
	}
	type envelope struct {
		OK          bool                `json:"ok"`
		Exit        int                 `json:"exit"`
		Diagnostics []apexv1.Diagnostic `json:"diagnostics"`
	}
	cases := []struct {
		build func() *cobra.Command
		args  []string
		code  string
	}{
		{newChangeCmd, []string{"new", "BAD", "--milestone", "M1", "--touches", "greeting"}, "usage.change-id"},
		{newChangeCmd, []string{"verify", "nope-nope"}, "change.not-found"},
		{newMilestoneCmd, []string{"gate", "M1"}, "gate.red"},
		{newMilestoneCmd, []string{"approve", "M7", "--by", "x"}, "milestone.not-found"},
	}
	for _, tc := range cases {
		stdout, _, err := v1Exec(t, tc.build, append(tc.args, "--json", "--cwd", root)...)
		var env envelope
		require.NoError(t, json.Unmarshal([]byte(stdout), &env), stdout)
		require.False(t, env.OK)
		require.NotEmpty(t, env.Diagnostics, stdout)
		d := env.Diagnostics[0]
		require.Equal(t, tc.code, d.Code, stdout)
		require.Equal(t, "error", d.Severity)
		require.NotEmpty(t, d.Message)
		require.Equal(t, exitOf[d.Code], env.Exit, "the published exit for %s", d.Code)
		require.Equal(t, env.Exit, exitCodeOf(t, err))
	}

	// change check: the failure, then one diagnostic per finding.
	_, _, err := v1Exec(t, newChangeCmd, "new", "add-wave", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err)
	stdout, _, _ := v1Exec(t, newChangeCmd, "check", "add-wave", "--json", "--cwd", root)
	var env envelope
	require.NoError(t, json.Unmarshal([]byte(stdout), &env))
	require.Equal(t, "change.check-failed", env.Diagnostics[0].Code)
	codes := make([]string, 0, len(env.Diagnostics))
	for _, d := range env.Diagnostics[1:] {
		require.NotEqual(t, apexv1.UnclassifiedFinding, d.Code, d.Message)
		codes = append(codes, d.Code)
	}
	require.Contains(t, codes, "change.empty-section")
	require.Contains(t, codes, "change.empty-delta")
	require.Contains(t, codes, "change.no-tasks")

	stdout, _, err = v1Exec(t, newProjectCmd, "codes", "--json")
	require.NoError(t, err)
	var list []apexv1.PublishedCode
	require.NoError(t, json.Unmarshal([]byte(stdout), &list))
	require.Len(t, list, len(apexv1.DiagnosticCodes)+len(apexv1.CodeAliases), "a row per code, and one per former name")
	byCode := map[string]apexv1.PublishedCode{}
	for _, c := range list {
		byCode[c.Code] = c
	}
	for _, a := range apexv1.CodeAliases {
		require.Equal(t, []string{a.Alias}, byCode[a.Code].Aliases, a.Code)
		require.Equal(t, a.Code, byCode[a.Alias].RenamedTo, a.Alias)
		require.Equal(t, byCode[a.Code].Exit, byCode[a.Alias].Exit, a.Alias)
		require.Equal(t, byCode[a.Code].Action, byCode[a.Alias].Action, a.Alias)
	}
	stdout, _, err = v1Exec(t, newProjectCmd, "codes")
	require.NoError(t, err)
	require.Regexp(t, `(?m)^session\.loop-running +3  retry `, stdout)
	require.Regexp(t, "(?m)^session\\.orchestrator-running +3  retry +renamed to `session\\.loop-running`; kept as an alias until v1\\.5", stdout)
	require.Regexp(t, "(?m)^models\\.operator-row +0  person +renamed to `models\\.orchestrator-row`; kept as an alias until v1\\.5", stdout)
}

// Backlog 32, 37 through the CLI: a regression command red at baseline is
// recorded by start, reported (not failed) by verify, and listed by status.
func TestV1Commands_RegressionRedAtBaseline(t *testing.T) {
	root := v1Repo(t)
	cfg := filepath.Join(root, "_apex", "config.yaml")
	data, err := os.ReadFile(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg, append(data, []byte("regression: ./suite.sh\n")...), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "suite.sh"), []byte("#!/bin/sh\necho flaky; exit 1\n"), 0o755))
	_, _, err = v1Exec(t, newChangeCmd, "new", "add-wave", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash("development/changes/add-wave/change.md")), []byte(v1ChangeMD), 0o644))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "plan, and a red suite")
	// regression: is guarded (config.changed): a person accepts it.
	_, _, err = v1Exec(t, newProjectCmd, "accept-config", "--by", "t", "--cwd", root)
	require.NoError(t, err)

	stdout, _, err := v1Exec(t, newChangeCmd, "start", "add-wave", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "regression red (exit 1) — recorded")

	require.NoError(t, os.WriteFile(filepath.Join(root, "wave.sh"), []byte("#!/bin/sh\necho wave\n"), 0o755))
	v1Git(t, root, "add", "wave.sh")
	v1Git(t, root, "commit", "-q", "-m", "feat: wave")
	stdout, _, err = v1Exec(t, newChangeCmd, "verify", "add-wave", "--json", "--cwd", root)
	require.NoError(t, err, stdout)
	var v struct {
		Green      bool           `json:"green"`
		Regression map[string]any `json:"regression"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &v))
	require.True(t, v.Green)
	// secs is omitempty and rounds to 0.1 s: the suite's echo is present
	// as "secs" only when the machine took 50 ms to run it. Its value is
	// the clock's, not this test's subject (W8b).
	if secs, ok := v.Regression["secs"]; ok {
		require.IsType(t, 0.0, secs)
		delete(v.Regression, "secs")
	}
	require.Equal(t, map[string]any{
		"cmd": "./suite.sh", "exit": 1.0, "baseline_exit": 1.0, "pre_existing_red": true, "failed": false,
		"output_sha256": v.Regression["output_sha256"], "log": "_apex/state/logs/add-wave/regression.log",
		"baseline_sha": v.Regression["baseline_sha"],
	}, v.Regression)

	stdout, _, err = v1Exec(t, newStatusCmd, "--json", "--cwd", root)
	require.NoError(t, err)
	var st struct {
		PreExistingRed []apexv1.PreExistingRed `json:"pre_existing_red"` //nolint:tagliatelle // the v1 --json payloads are snake_case by contract
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &st))
	require.Len(t, st.PreExistingRed, 1)
	require.Equal(t, apexv1.PreExistingRed{
		Change: "add-wave", Kind: "regression", Ref: "regression", Cmd: "./suite.sh", Exit: 1,
		SHA: st.PreExistingRed[0].SHA,
	}, st.PreExistingRed[0])
	stdout, _, err = v1Exec(t, newStatusCmd, "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "Red at baseline, not any change's own (roadmap it): regression `./suite.sh`")
	require.Empty(t, v1Git(t, root, "status", "--porcelain"))
}

// initGitRepo is an empty git repo, with a person at the terminal.
func initGitRepo(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("git fixture")
	}
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("APE_SESSION", "")
	t.Setenv("CLAUDECODE", "")
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "t"}, {"GIT_AUTHOR_EMAIL", "t@t"}, {"GIT_COMMITTER_NAME", "t"}, {"GIT_COMMITTER_EMAIL", "t@t"}} {
		t.Setenv(kv[0], kv[1])
	}
	v1Git(t, root, "init", "-q")
	return root
}

// D82: init says which existing file it amended, and how.
func TestV1_ProjectInitAmendsAgentsMD(t *testing.T) {
	root := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Map\n"), 0o644))
	stdout, _, err := v1Exec(t, newProjectCmd, "init", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "amended  AGENTS.md (added the project-context line)\n")
	stdout, _, err = v1Exec(t, newProjectCmd, "init", "--cwd", root)
	require.NoError(t, err)
	require.NotContains(t, stdout, "amended")
}

// D86: an untracked config without models: is amended and said to be left
// uncommitted; a models: block missing skills is a warning on stderr
// (human) or under "warnings" (JSON).
func TestV1_ProjectInitModelDefaults(t *testing.T) {
	root := initGitRepo(t)
	cfgPath := filepath.Join(root, "_apex", "config.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(cfgPath), 0o755))
	require.NoError(t, os.WriteFile(cfgPath, []byte("project_name: g\n"), 0o644))
	stdout, stderr, err := v1Exec(t, newProjectCmd, "init", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "amended  _apex/config.yaml (appended the models: defaults; not committed: it was untracked or had uncommitted changes — commit it yourself)\n")
	require.NotContains(t, stderr, "warning:")

	root = initGitRepo(t)
	cfgPath = filepath.Join(root, "_apex", "config.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(cfgPath), 0o755))
	require.NoError(t, os.WriteFile(cfgPath, []byte("project_name: g\nmodels:\n  apex-apply: opus\n"), 0o644))
	stdout, stderr, err = v1Exec(t, newProjectCmd, "init", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stderr, "warning: _apex/config.yaml models: has no apex-accept entry — apex-accept runs on claude's default model")
	require.Equal(t, 6, strings.Count(stderr, "warning: "))
	require.NotContains(t, stdout, "warning")

	stdout, stderr, err = v1Exec(t, newProjectCmd, "init", "--cwd", root, "--json")
	require.NoError(t, err)
	require.Empty(t, stderr)
	var out struct {
		Warnings []string `json:"warnings"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &out))
	require.Len(t, out.Warnings, 6)
	require.Contains(t, out.Warnings[5], "apex-release")
}
