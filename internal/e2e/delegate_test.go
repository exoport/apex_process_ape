//go:build !windows

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeDelegate stands in for the claude session `ape change accept
// --delegate` (apex-accept) and `ape change tool` start. Like Claude Code
// it reads the typed line and reports it through ape's injected hooks; it
// records the line and its markers in $FAKE_DIR/seen. As apex-accept
// (a line starting /apex-accept) it writes an evidence note and records
// its verdict with `ape change accept <id> <T> --verdict …` — FAKE_MODE
// pass (default), fail, return (a fail naming wave.sh, in the change's
// diff, with --defect: D188), none (no verdict) or tamper (also commits a
// code edit). FAKE_SLEEP delays the work.
const fakeDelegate = `#!/bin/bash
if [ "$1" = "--version" ]; then echo "2.1.300 (Claude Code)"; exit 0; fi
settings=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--settings" ]; then settings="$2"; shift; fi
  shift
done
hook() { printf '%s' "$settings" | grep -o "env APE_BRIDGE_PORT=[0-9]* [^\"]* notify --event $1" | head -1; }
ups=$(hook UserPromptSubmit)
stop=$(hook Stop)
printf '\n  bypass permissions on (shift+tab to cycle)\n❯ '
IFS= read -r line
line=${line%$'\r'}
printf '{"session_id":"del-1","hook_event_name":"UserPromptSubmit","cwd":"%s","prompt":"%s"}' "$PWD" "$line" | sh -c "$ups"
echo "line=$line delegate=$APE_DELEGATE parent=$APE_DELEGATE_PARENT session=$APE_SESSION" >> "$FAKE_DIR/seen"
sleep "${FAKE_SLEEP:-0}"
case "$line" in
/apex-accept*)
  task=$(printf '%s' "$line" | sed -n 's/.*--task \([^ ]*\).*/\1/p')
  id=$(printf '%s' "$line" | sed -n 's/.*--change \([^ ]*\).*/\1/p')
  note="development/changes/$id/assets/accept-$task.md"
  mkdir -p "development/changes/$id/assets" && echo "looked at $task" > "$note"
  case "${FAKE_MODE:-pass}" in
  pass) ape change accept "$id" "$task" --verdict pass --reason "the copy reads well" --evidence "$note" > "$FAKE_DIR/verdict.out" 2>&1 ;;
  fail) ape change accept "$id" "$task" --verdict fail --reason "the copy is truncated" > "$FAKE_DIR/verdict.out" 2>&1 ;;
  return) ape change accept "$id" "$task" --verdict fail --reason "wave.sh waves in lower case" --defect wave.sh > "$FAKE_DIR/verdict.out" 2>&1 ;;
  tamper) echo "# touched" >> wave.sh; git commit -qam "fix: sneak a code change in"
          ape change accept "$id" "$task" --verdict pass --reason "fine" > "$FAKE_DIR/verdict.out" 2>&1 ;;
  esac ;;
esac
printf '{"session_id":"del-1","hook_event_name":"Stop","cwd":"%s","background_tasks":[]}' "$PWD" | sh -c "$stop"
printf '\n❯ '
while IFS= read -r more; do :; done
`

const delegateChange = `---
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
- T2 the wave reads well — tool: ui-mockup — check: human-review
`

// delegateProject is a v1 project whose config delegates human review
// (accepted by a person) and declares a design tool, with one reviewed
// change, add-wave, whose T2 awaits a person; the fake delegate is on PATH.
func delegateProject(t *testing.T) (ape, project, fake string, env []string) {
	t.Helper()
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	ape = buildApe(t)
	project = t.TempDir()
	fake = t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(fakeDelegate), 0o755))
	env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "APE_SESSION=", "CLAUDECODE=", "APE_REVIEW=", "APE_DELEGATE=", "PATH="+fake+":"+os.Getenv("PATH"),
		"APE_CLAUDE_PROBE=off", "FAKE_DIR="+fake)
	sh := func(name string, args ...string) {
		c := exec.Command(name, args...)
		c.Dir = project
		c.Env = env
		out, err := c.CombinedOutput()
		require.NoError(t, err, "%s %v: %s", name, args, out)
	}
	sh("git", "init", "-q")
	sh("git", "commit", "-q", "--allow-empty", "-m", "base")
	sh(ape, "project", "init")
	cfg := filepath.Join(project, "_apex", "config.yaml")
	data, err := os.ReadFile(cfg)
	require.NoError(t, err)
	require.NoError(t, writeFile(cfg, string(data)+"human_review: {}\ntools:\n  design:\n"+
		"    - {name: ui-mockup, skill: mock-skill, when: \"a screen changes\", args: \"{change} --out {assets}/mock-{task}.png\"}\n"))
	sh("git", "commit", "-qam", "config: delegate human review")
	sh(ape, "project", "accept-config", "--by", "test")
	require.NoError(t, writeFile(filepath.Join(project, "development", "project.md"),
		"# G\n## Vision\nv\n## Users\nu\n## Scope\ns\n## Out of scope\no\n## Constraints\nc\n"))
	require.NoError(t, writeWaveGate(project))
	sh(ape, "milestone", "new", "Skeleton", "--gate", "./gate.sh", "--goal", "waves")
	sh(ape, "milestone", "approve", "M1", "--by", "test")
	sh(ape, "change", "new", "add-wave", "--milestone", "M1", "--touches", "greeting")
	require.NoError(t, writeFile(filepath.Join(project, "development", "changes", "add-wave", "change.md"), delegateChange))
	for _, skill := range []string{"apex-accept", "mock-skill"} {
		require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "skills", skill), 0o755))
		require.NoError(t, writeFile(filepath.Join(project, ".claude", "skills", skill, "SKILL.md"), "# "+skill+"\n"))
	}
	sh("git", "add", "-A")
	sh("git", "commit", "-q", "-m", "plan")
	sh(ape, "change", "start", "add-wave")
	require.NoError(t, os.WriteFile(filepath.Join(project, "wave.sh"), []byte("#!/bin/sh\necho wave\n"), 0o755))
	sh("git", "add", "wave.sh")
	sh("git", "commit", "-q", "-m", "feat: wave")
	sh(ape, "change", "verify", "add-wave")
	require.NoError(t, writeFile(filepath.Join(project, "development", "changes", "add-wave", "review.md"), "fine\n"))
	sh("git", "add", "-A")
	sh("git", "commit", "-q", "-m", "review: add-wave")
	sh(ape, "change", "review", "add-wave", "--verdict", "pass", "--findings", "development/changes/add-wave/review.md")
	return ape, project, fake, env
}

type delegateResult struct {
	Running    bool   `json:"running"`
	DetachedID string `json:"detached_id"` //nolint:tagliatelle // ape's snake_case --json contract
	Skill      string `json:"skill"`
	Args       string `json:"args"`
	Outcome    struct {
		Verdict  string `json:"verdict"`
		Reason   string `json:"reason"`
		Voided   bool   `json:"voided"`
		Accepted *struct {
			By  string `json:"by"`
			Run string `json:"run"`
		} `json:"accepted"`
	} `json:"outcome"`
}

func statusChange(t *testing.T, ape, project string, env []string) map[string]any {
	t.Helper()
	out, code := runApe(t, ape, project, env, "status", "--json")
	require.Equal(t, 0, code)
	var r struct {
		Changes []map[string]any `json:"changes"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &r), out)
	for _, c := range r.Changes {
		if c["id"] == "add-wave" {
			return c
		}
	}
	return nil
}

// D75/D76: from the orchestrator's session, --delegate runs apex-accept in
// its own session; its pass is recorded by ape as delegate:apex-accept
// with the run id, shown by ape status, and named in the archive squash.
func TestDelegate_PassIsRecordedByApe(t *testing.T) {
	ape, project, fake, env := delegateProject(t)
	asOrchestrator(t, project, "orchestrator-run")
	env = append(slices.Clone(env), "APE_SESSION=task/orchestrator-run", "APE_DEPTH=1")
	out, code := runApe(t, ape, project, env, "change", "accept", "add-wave", "T2", "--delegate", "--wait", "90s", "--json")
	require.Equal(t, 0, code, "stdout: %s", out)
	var res delegateResult
	require.NoError(t, json.Unmarshal([]byte(out), &res), out)
	require.Equal(t, "apex-accept", res.Skill)
	require.Equal(t, "--task T2 --change add-wave", res.Args)
	require.Equal(t, "pass", res.Outcome.Verdict)
	require.NotNil(t, res.Outcome.Accepted)
	require.Equal(t, "delegate:apex-accept", res.Outcome.Accepted.By)
	require.Equal(t, res.DetachedID, res.Outcome.Accepted.Run)

	seen, err := os.ReadFile(filepath.Join(fake, "seen"))
	require.NoError(t, err)
	require.Contains(t, string(seen), "line=/apex-accept --autonomous --task T2 --change add-wave")
	require.Contains(t, string(seen), "delegate=add-wave/T2")
	require.Contains(t, string(seen), "parent=task/orchestrator-run")
	require.NotContains(t, string(seen), "session=task/orchestrator-run", "its own session, not the caller's")

	c := statusChange(t, ape, project, env)
	require.Empty(t, c["awaiting_human"])
	accepted, ok := c["accepted"].([]any)
	require.True(t, ok, "%v", c)
	a, _ := accepted[0].(map[string]any)
	require.Equal(t, "delegate:apex-accept", a["by"])
	require.Equal(t, res.DetachedID, a["run"])
	status, err := exec.Command("git", "-C", project, "status", "--porcelain").Output()
	require.NoError(t, err)
	require.Empty(t, strings.TrimSpace(string(status)), "the note is committed with the acceptance")

	_, code = runApe(t, ape, project, env, "change", "archive", "add-wave")
	require.Equal(t, 0, code)
	msg, err := exec.Command("git", "-C", project, "log", "-1", "--format=%B").Output()
	require.NoError(t, err)
	require.Contains(t, string(msg), "Accepted-By: delegate:apex-accept T2 run "+res.DetachedID)
}

// A fail accepts nothing: T2 stays awaiting a person, ape status shows the
// verdict, and the delegate is not run again on the same tip (D78).
func TestDelegate_FailLeavesItToAPerson(t *testing.T) {
	ape, project, _, env := delegateProject(t)
	env = append(slices.Clone(env), "FAKE_MODE=fail")
	out, code := runApe(t, ape, project, env, "change", "accept", "add-wave", "T2", "--delegate", "--wait", "90s", "--json")
	require.Equal(t, 1, code, "stdout: %s", out)
	require.Contains(t, out, `"code": "delegate.fail"`)
	require.Contains(t, out, `"action": "person"`)
	c := statusChange(t, ape, project, env)
	require.Equal(t, []any{"T2"}, c["awaiting_human"])
	d, _ := c["delegate"].(map[string]any)["T2"].(map[string]any)
	require.Equal(t, "fail", d["verdict"])
	require.Equal(t, "the copy is truncated", d["reason"])

	out, code = runApe(t, ape, project, env, "change", "accept", "add-wave", "T2", "--delegate", "--json")
	require.Equal(t, 1, code)
	require.Contains(t, out, `"code": "delegate.already-failed"`)
}

// D188: a fail naming a defect in the change's own diff goes back to the
// change (delegate.returned, action apply), once: after verify and review
// again, the next fail is a person's.
func TestDelegate_FailOnAnInDiffDefectReturnsTheChange(t *testing.T) {
	ape, project, _, env := delegateProject(t)
	env = append(slices.Clone(env), "FAKE_MODE=return")
	out, code := runApe(t, ape, project, env, "change", "accept", "add-wave", "T2", "--delegate", "--wait", "90s", "--json")
	require.Equal(t, 1, code, "stdout: %s", out)
	require.Contains(t, out, `"code": "delegate.returned"`)
	require.Contains(t, out, `"action": "apply"`)
	c := statusChange(t, ape, project, env)
	require.Equal(t, "in-progress", c["status"])
	returns, _ := c["delegate_returns"].(map[string]any)["T2"].([]any)
	require.Len(t, returns, 1, "%v", c)
	ret, _ := returns[0].(map[string]any)
	require.Equal(t, "wave.sh waves in lower case", ret["reason"])
	require.Equal(t, []any{"wave.sh"}, ret["defects"])
	msg, err := exec.Command("git", "-C", project, "log", "-1", "--format=%s").Output()
	require.NoError(t, err)
	require.Equal(t, "apex: add-wave returned by delegate", strings.TrimSpace(string(msg)))

	_, code = runApe(t, ape, project, env, "change", "verify", "add-wave")
	require.Equal(t, 0, code)
	_, code = runApe(t, ape, project, env, "change", "review", "add-wave", "--verdict", "pass", "--findings", "development/changes/add-wave/review.md")
	require.Equal(t, 0, code)
	out, code = runApe(t, ape, project, env, "change", "accept", "add-wave", "T2", "--delegate", "--wait", "90s", "--json")
	require.Equal(t, 1, code, "stdout: %s", out)
	require.Contains(t, out, `"code": "delegate.fail"`)
	require.Contains(t, out, "its second fail")
	require.Contains(t, out, `"to_person": "second-fail"`)
	require.Equal(t, []any{"T2"}, statusChange(t, ape, project, env)["awaiting_human"])
}

// D78: a session that records no verdict failed the task.
func TestDelegate_NoVerdictIsAFail(t *testing.T) {
	ape, project, _, env := delegateProject(t)
	env = append(slices.Clone(env), "FAKE_MODE=none")
	out, code := runApe(t, ape, project, env, "change", "accept", "add-wave", "T2", "--delegate", "--wait", "90s", "--json")
	require.Equal(t, 1, code, "stdout: %s", out)
	require.Contains(t, out, `"code": "delegate.fail"`)
	require.Contains(t, out, "no verdict")
}

// Only the delegate session records a verdict.
func TestDelegate_VerdictRefusedOutsideTheSession(t *testing.T) {
	ape, project, _, env := delegateProject(t)
	out, code := runApe(t, ape, project, env, "change", "accept", "add-wave", "T2", "--verdict", "pass", "--reason", "trust me", "--json")
	require.Equal(t, 2, code, out)
	require.Contains(t, out, `"code": "delegate.not-in-session"`)
	env = append(slices.Clone(env), "APE_DELEGATE=add-wave/T2")
	out, code = runApe(t, ape, project, env, "change", "accept", "add-wave", "T2", "--verdict", "pass", "--reason", "trust me", "--json")
	require.Equal(t, 2, code, out)
	require.Contains(t, out, "no delegate run of add-wave T2 is in progress")
}

// A delegate that commits has its verdict voided.
func TestDelegate_TamperingVoidsTheVerdict(t *testing.T) {
	ape, project, _, env := delegateProject(t)
	env = append(slices.Clone(env), "FAKE_MODE=tamper")
	out, code := runApe(t, ape, project, env, "change", "accept", "add-wave", "T2", "--delegate", "--wait", "90s", "--json")
	require.Equal(t, 1, code, "stdout: %s", out)
	require.Contains(t, out, `"code": "delegate.voided"`)
	require.Contains(t, out, "fix: sneak a code change in")
	require.Equal(t, []any{"T2"}, statusChange(t, ape, project, env)["awaiting_human"])
}

// --wait bounds one call (exit 75); the next attaches to the same session.
func TestDelegate_WaitAgainAttaches(t *testing.T) {
	ape, project, _, env := delegateProject(t)
	env = append(slices.Clone(env), "FAKE_SLEEP=4")
	out, code := runApe(t, ape, project, env, "change", "accept", "add-wave", "T2", "--delegate", "--wait", "1s", "--json")
	require.Equal(t, 75, code)
	var res delegateResult
	require.NoError(t, json.Unmarshal([]byte(out), &res), out)
	require.True(t, res.Running)
	out, code = runApe(t, ape, project, env, "change", "accept", "add-wave", "T2", "--delegate", "--wait", "90s", "--json")
	require.Equal(t, 0, code, out)
	var res2 delegateResult
	require.NoError(t, json.Unmarshal([]byte(out), &res2), out)
	require.Equal(t, res.DetachedID, res2.DetachedID, "the same session")
	handles, err := filepath.Glob(filepath.Join(project, "_apex", "state", "runs", "detached", "*.json"))
	require.NoError(t, err)
	require.Len(t, handles, 1)
}

// D74, D77: ape change tool fills the entry's args and runs its skill in
// its own session; --wait and 75 as for every long verb.
func TestChangeTool_FillsArgsAndAttaches(t *testing.T) {
	ape, project, fake, env := delegateProject(t)
	env = append(slices.Clone(env), "FAKE_SLEEP=3")
	out, code := runApe(t, ape, project, env, "change", "tool", "add-wave", "ui-mockup", "--task", "T2", "--wait", "1s", "--json")
	require.Equal(t, 75, code, out)
	out, code = runApe(t, ape, project, env, "change", "tool", "add-wave", "ui-mockup", "--task", "T2", "--wait", "90s", "--json")
	require.Equal(t, 0, code, out)
	var res struct {
		Slot     string `json:"slot"`
		Skill    string `json:"skill"`
		Args     string `json:"args"`
		TaskExit *int   `json:"task_exit"` //nolint:tagliatelle // ape's snake_case --json contract
	}
	require.NoError(t, json.Unmarshal([]byte(out), &res), out)
	require.Equal(t, "design", res.Slot)
	require.Equal(t, "mock-skill", res.Skill)
	require.Equal(t, "development/changes/add-wave/change.md --out development/changes/add-wave/assets/mock-T2.png", res.Args)
	require.NotNil(t, res.TaskExit)
	require.Equal(t, 0, *res.TaskExit)
	seen, err := os.ReadFile(filepath.Join(fake, "seen"))
	require.NoError(t, err)
	require.Contains(t, string(seen), "line=/mock-skill --autonomous development/changes/add-wave/change.md --out development/changes/add-wave/assets/mock-T2.png")
	require.Equal(t, 1, strings.Count(string(seen), "line="), "one session, attached to")

	out, code = runApe(t, ape, project, env, "change", "tool", "add-wave", "nope", "--json")
	require.Equal(t, 1, code)
	require.Contains(t, out, `"code": "tool.unknown"`)
	require.Contains(t, out, `"action": "replan"`)
}
