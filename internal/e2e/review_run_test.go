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

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// fakeReviewer is a stand-in for the claude session `ape change review
// --run` starts. Like Claude Code it reads the typed line and reports it
// through ape's injected hooks; like the apex-review skill it writes and
// commits review.md and records the verdict with `ape change review`
// (`ape` is the running binary, first on the session's PATH). It also
// proves where it ran: APE_REVIEW and APE_REVIEW_PARENT go to a file.
// FAKE_MODE=tamper also commits a code edit, which must void the verdict;
// FAKE_SLEEP delays the work.
const fakeReviewer = `#!/bin/bash
if [ "$1" = "--version" ]; then echo "2.1.300 (Claude Code)"; exit 0; fi
echo "args=$* effort=$CLAUDE_CODE_EFFORT_LEVEL" > "$FAKE_DIR/launch"
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
printf '{"session_id":"rev-1","hook_event_name":"UserPromptSubmit","cwd":"%s","prompt":"%s"}' "$PWD" "$line" | sh -c "$ups"
echo "line=$line review=$APE_REVIEW parent=$APE_REVIEW_PARENT session=$APE_SESSION" > "$FAKE_DIR/seen"
sleep "${FAKE_SLEEP:-0}"
id=${line##* }
printf 'no findings\n' > "development/changes/$id/review.md"
git add "development/changes/$id/review.md" && git commit -q -m "review: $id" -m "Change: $id"
if [ "$FAKE_MODE" = tamper ]; then echo "# touched" >> wave.sh; git commit -qam "fix: sneak a code change in"; fi
ape change review "$id" --verdict pass --findings "development/changes/$id/review.md" > "$FAKE_DIR/verdict.out" 2>&1
printf '{"session_id":"rev-1","hook_event_name":"Stop","cwd":"%s","background_tasks":[]}' "$PWD" | sh -c "$stop"
printf '\n❯ '
while IFS= read -r more; do :; done
`

// reviewProject is a v1 project with one verified change, add-wave, and
// the fake reviewer on PATH. It returns the ape binary, the project, the
// fake's directory and an environment to run ape in.
func reviewProject(t *testing.T) (ape, project, fake string, env []string) {
	t.Helper()
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	ape = buildApe(t)
	project = t.TempDir()
	fake = t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(fakeReviewer), 0o755))
	env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "APE_SESSION=", "APE_REVIEW=", "PATH="+fake+":"+os.Getenv("PATH"),
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
	require.NoError(t, writeFile(filepath.Join(project, "development", "project.md"),
		"# G\n## Vision\nv\n## Users\nu\n## Scope\ns\n## Out of scope\no\n## Constraints\nc\n"))
	sh(ape, "milestone", "new", "Skeleton", "--gate", "./wave.sh", "--goal", "waves")
	sh(ape, "milestone", "approve", "M1", "--by", "test")
	sh(ape, "change", "new", "add-wave", "--milestone", "M1", "--touches", "greeting")
	require.NoError(t, writeFile(filepath.Join(project, "development", "changes", "add-wave", "change.md"), v1Change))
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "skills", "apex-review"), 0o755))
	require.NoError(t, writeFile(filepath.Join(project, ".claude", "skills", "apex-review", "SKILL.md"), "# review\n"))
	sh("git", "add", "-A")
	sh("git", "commit", "-q", "-m", "plan")
	sh(ape, "change", "start", "add-wave")
	require.NoError(t, os.WriteFile(filepath.Join(project, "wave.sh"), []byte("#!/bin/sh\necho wave\n"), 0o755))
	sh("git", "add", "wave.sh")
	sh("git", "commit", "-q", "-m", "feat: wave")
	sh(ape, "change", "verify", "add-wave")
	return ape, project, fake, env
}

func runApe(t *testing.T, ape, project string, env []string, args ...string) (stdout string, code int) {
	t.Helper()
	c := exec.Command(ape, args...)
	c.Dir = project
	c.Env = env
	var out, errb strings.Builder
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	code = exitStatus(err)
	if code != 0 && code != 75 {
		t.Logf("ape %v exited %d; stderr:\n%s", args, code, tail(errb.String(), 20))
	}
	return out.String(), code
}

func changeState(t *testing.T, project string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(project, "development", "changes", "add-wave", "state.json"))
	require.NoError(t, err)
	var st map[string]any
	require.NoError(t, json.Unmarshal(data, &st))
	return st
}

// D18: from inside an ape session (APE_SESSION set — where ape task would
// refuse to nest), `ape change review --run` starts apex-review in its own
// detached session, which records a verdict; the caller gets it back.
func TestReviewRun_FreshSessionRecordsTheVerdict(t *testing.T) {
	ape, project, fake, env := reviewProject(t)
	env = append(slices.Clone(env), "APE_SESSION=task/outer-run")

	out, code := runApe(t, ape, project, env, "change", "review", "add-wave", "--run", "--wait", "90s", "--json")
	require.Equal(t, 0, code, "stdout: %s", out)
	var res struct {
		Outcome struct {
			Verdict string `json:"verdict"`
			Status  string `json:"status"`
			Voided  bool   `json:"voided"`
		} `json:"outcome"`
		DetachedID string `json:"detached_id"` //nolint:tagliatelle // ape's snake_case --json contract
	}
	require.NoError(t, json.Unmarshal([]byte(out), &res), out)
	require.Equal(t, "pass", res.Outcome.Verdict)
	require.Equal(t, "reviewed", res.Outcome.Status)
	require.False(t, res.Outcome.Voided)
	require.Equal(t, "reviewed", changeState(t, project)["status"])

	seen, err := os.ReadFile(filepath.Join(fake, "seen"))
	require.NoError(t, err)
	require.Contains(t, string(seen), "line=/apex-review --autonomous add-wave")
	require.Contains(t, string(seen), "review=add-wave", "the session is read-only: APE_REVIEW reaches its tools")
	require.Contains(t, string(seen), "parent=task/outer-run", "the requesting session is recorded")
	require.NotContains(t, string(seen), "session=task/outer-run", "the review is its own session, not the caller's")

	_, err = os.Stat(filepath.Join(project, "_apex", "state", "runs", "tasks", "apex-review", "latest", "manifest.yaml"))
	require.NoError(t, err, "the review session's cost is its own manifest")
	require.NoFileExists(t, filepath.Join(project, "_apex", "state", "logs", "add-wave", "review-run.json"), "settled and cleared")
	status, err := exec.Command("git", "-C", project, "status", "--porcelain").Output()
	require.NoError(t, err)
	require.Empty(t, strings.TrimSpace(string(status)))
}

// A session that changes more than its review.md has its verdict voided.
func TestReviewRun_TamperingVoidsTheVerdict(t *testing.T) {
	ape, project, _, env := reviewProject(t)
	env = append(slices.Clone(env), "FAKE_MODE=tamper")
	out, code := runApe(t, ape, project, env, "change", "review", "add-wave", "--run", "--wait", "90s", "--json")
	require.Equal(t, 1, code, "stdout: %s", out)
	require.Contains(t, out, "voided")
	require.Contains(t, out, "committed wave.sh")
	st := changeState(t, project)
	require.Equal(t, "in-progress", st["status"], "the code moved since the verify: back to in-progress")
	require.Nil(t, st["review"])
	voided, ok := st["review_voided"].([]any)
	require.True(t, ok)
	require.Len(t, voided, 1)
}

// --wait bounds one call (exit 75); the next call attaches to the running
// session rather than starting another.
func TestReviewRun_WaitAgainAttaches(t *testing.T) {
	ape, project, _, env := reviewProject(t)
	env = append(slices.Clone(env), "FAKE_SLEEP=4")
	_, code := runApe(t, ape, project, env, "change", "review", "add-wave", "--run", "--wait", "1s")
	require.Equal(t, 75, code)
	out, code := runApe(t, ape, project, env, "change", "review", "add-wave", "--run", "--wait", "90s")
	require.Equal(t, 0, code, out)
	handles, err := filepath.Glob(filepath.Join(project, "_apex", "state", "runs", "detached", "*.json"))
	require.NoError(t, err)
	require.Len(t, handles, 1, "one review session, not two")
}

// Depth 1: a review session cannot start another review.
func TestReviewRun_RefusedInsideAReviewSession(t *testing.T) {
	ape, project, _, env := reviewProject(t)
	env = append(slices.Clone(env), "APE_REVIEW=add-wave")
	out, code := runApe(t, ape, project, env, "change", "review", "add-wave", "--run", "--json")
	require.Equal(t, 2, code)
	require.Contains(t, out, "a review session cannot start another review")
}

// D22: the review session runs on the model and effort of the session
// that asked for it, unless _apex/config.yaml models.apex-review says
// otherwise; the session's manifest records which source decided.
func TestReviewRun_ModelAndEffort(t *testing.T) {
	launchOf := func(t *testing.T, project string) map[string]any {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(project, "_apex", "state", "runs", "tasks", "apex-review", "latest", "manifest.yaml"))
		require.NoError(t, err)
		var m pipeline.Manifest
		require.NoError(t, yaml.Unmarshal(data, &m))
		require.NotNil(t, m.ReviewLaunch, "manifest:\n%s", data)
		require.Equal(t, m.ReviewLaunch, m.Launch, "launch: and review_launch: say the same thing")
		l := m.ReviewLaunch
		return map[string]any{
			"model": l.Model, "model_source": l.ModelSource, "effort": l.Effort,
			"effort_source": l.EffortSource, "parent": l.Parent,
		}
	}

	t.Run("inherits the asking session", func(t *testing.T) {
		ape, project, fake, env := reviewProject(t)
		env = append(slices.Clone(env), "APE_SESSION=task/outer", "APE_SESSION_MODEL=claude-sonnet-5-5", "APE_SESSION_EFFORT=high")
		out, code := runApe(t, ape, project, env, "change", "review", "add-wave", "--run", "--wait", "90s", "--json")
		require.Equal(t, 0, code, out)
		launch, err := os.ReadFile(filepath.Join(fake, "launch"))
		require.NoError(t, err)
		require.Contains(t, string(launch), "--model claude-sonnet-5-5")
		// Backlog 29: a v1 session's effort is claude's --effort, never the
		// process-wide CLAUDE_CODE_EFFORT_LEVEL.
		require.Contains(t, string(launch), "--effort high")
		require.Contains(t, string(launch), " effort=\n")
		l := launchOf(t, project)
		require.Equal(t, "claude-sonnet-5-5", l["model"])
		require.Equal(t, "parent-session", l["model_source"])
		require.Equal(t, "high", l["effort"])
		require.Equal(t, "parent-session", l["effort_source"])
		require.Equal(t, "task/outer", l["parent"])
		require.Contains(t, out, `"model_source": "parent-session"`)
	})

	t.Run("config models.apex-review wins", func(t *testing.T) {
		ape, project, fake, env := reviewProject(t)
		cfg := filepath.Join(project, "_apex", "config.yaml")
		data, err := os.ReadFile(cfg)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(cfg, append(data, []byte("models:\n  apex-review: {model: claude-haiku-5-5, effort: low}\n")...), 0o644))
		commit := exec.Command("git", "-C", project, "commit", "-qam", "config: review on haiku")
		commit.Env = env
		cout, cerr := commit.CombinedOutput()
		require.NoError(t, cerr, "%s", cout)
		_, vcode := runApe(t, ape, project, env, "change", "verify", "add-wave")
		require.Equal(t, 0, vcode, "the config commit moved HEAD past the verify")
		env = append(slices.Clone(env), "APE_SESSION_MODEL=claude-sonnet-5-5", "APE_SESSION_EFFORT=high")
		out, code := runApe(t, ape, project, env, "change", "review", "add-wave", "--run", "--wait", "90s", "--json")
		require.Equal(t, 0, code, out)
		launch, err := os.ReadFile(filepath.Join(fake, "launch"))
		require.NoError(t, err)
		require.Contains(t, string(launch), "--model claude-haiku-5-5")
		require.Contains(t, string(launch), "--effort low")
		require.Contains(t, string(launch), " effort=\n")
		l := launchOf(t, project)
		require.Equal(t, "config", l["model_source"])
		require.Equal(t, "config", l["effort_source"])
	})

	t.Run("a plain shell: claude's default, said so", func(t *testing.T) {
		ape, project, fake, env := reviewProject(t)
		env = append(slices.Clone(env), "APE_SESSION_MODEL=", "APE_SESSION_EFFORT=")
		_, code := runApe(t, ape, project, env, "change", "review", "add-wave", "--run", "--wait", "90s")
		require.Equal(t, 0, code)
		launch, err := os.ReadFile(filepath.Join(fake, "launch"))
		require.NoError(t, err)
		require.NotContains(t, string(launch), "--model")
		require.NotContains(t, string(launch), "--effort")
		l := launchOf(t, project)
		require.Equal(t, "default", l["model_source"])
		require.Equal(t, "default", l["effort_source"])
	})
}

// The other half of D22: every ape session tells its tools the model and
// explicit effort it was launched with.
func TestTask_ExportsItsModelAndEffort(t *testing.T) {
	ape, project, fake, env := reviewProject(t)
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "skills", "apex-foo"), 0o755))
	require.NoError(t, writeFile(filepath.Join(project, ".claude", "skills", "apex-foo", "SKILL.md"), "# foo\n"))
	// The fake reviewer doubles as any skill's session; it records its env.
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(strings.Replace(fakeReviewer,
		`echo "args=$* effort=$CLAUDE_CODE_EFFORT_LEVEL" > "$FAKE_DIR/launch"`,
		`echo "model=$APE_SESSION_MODEL effort=$APE_SESSION_EFFORT" > "$FAKE_DIR/session-env"`, 1)), 0o755))
	_, code := runApe(t, ape, project, env, "task", "apex-foo", "--args", "add-wave", "--model", "claude-sonnet-5-5", "--effort", "high",
		"--idle-timeout", "20s", "--output-format", "json", "--cwd", project)
	require.Equal(t, 0, code)
	data, err := os.ReadFile(filepath.Join(fake, "session-env"))
	require.NoError(t, err)
	require.Equal(t, "model=claude-sonnet-5-5 effort=high\n", string(data))
}
