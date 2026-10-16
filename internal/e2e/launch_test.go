//go:build !windows

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// fakeLaunchRecorder is fakeClaude that first writes how it was launched:
// its argv, the CLAUDE_CODE_EFFORT_LEVEL it inherited and the launch ape
// exported to its tools.
var fakeLaunchRecorder = strings.Replace(fakeClaude, `settings=""`,
	`printf 'args=%s\nenv_effort=%s\nsession=%s/%s\n' "$*" "$CLAUDE_CODE_EFFORT_LEVEL" "$APE_SESSION_MODEL" "$APE_SESSION_EFFORT" > "$FAKE_DIR/launch"
settings=""`, 1)

type launchSeen struct {
	args, envEffort, session string
}

func readLaunch(t *testing.T, fake string) launchSeen {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fake, "launch"))
	require.NoError(t, err)
	var s launchSeen
	for line := range strings.SplitSeq(string(data), "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "args":
			s.args = v
		case "env_effort":
			s.envEffort = v
		case "session":
			s.session = v
		}
	}
	return s
}

// launchProject is a git repo with skill apex-foo and the recording fake
// on PATH; v1 makes it an APEX v1 project. The environment carries a
// CLAUDE_CODE_EFFORT_LEVEL as if ape ran inside a Claude Code session at
// max: it must never reach the spawned session.
func launchProject(t *testing.T, ape string, v1 bool) (project, fake string, env []string) {
	t.Helper()
	project, fake = t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(fakeLaunchRecorder), 0o755))
	env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "APE_SESSION=", "APE_SESSION_MODEL=", "APE_SESSION_EFFORT=", "APE_REVIEW=",
		"PATH="+fake+":"+os.Getenv("PATH"), "APE_CLAUDE_PROBE=off", "FAKE_DIR="+fake,
		"FAKE_CLAUDE_LINE="+filepath.Join(fake, "line"), "CLAUDE_CODE_EFFORT_LEVEL=max")
	sh := func(name string, args ...string) {
		c := exec.Command(name, args...)
		c.Dir = project
		c.Env = env
		out, err := c.CombinedOutput()
		require.NoError(t, err, "%s %v: %s", name, args, out)
	}
	sh("git", "init", "-q")
	sh("git", "commit", "-q", "--allow-empty", "-m", "base")
	if v1 {
		sh(ape, "project", "init")
		require.NoError(t, writeFile(filepath.Join(project, "development", "project.md"),
			"# G\n## Vision\nv\n## Users\nu\n## Scope\ns\n## Out of scope\no\n## Constraints\nc\n"))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "skills", "apex-foo"), 0o755))
	require.NoError(t, writeFile(filepath.Join(project, ".claude", "skills", "apex-foo", "SKILL.md"), "# foo\n"))
	// A framework effort table: a v1 session must not use it.
	require.NoError(t, os.MkdirAll(filepath.Join(project, "_apex"), 0o755))
	require.NoError(t, writeFile(filepath.Join(project, "_apex", "effort-defaults.yaml"),
		"version: 1\ndefaults:\n  opus: medium\nfallback: medium\n"))
	sh("git", "add", "-A")
	sh("git", "commit", "-q", "-m", "setup")
	return project, fake, env
}

func taskManifest(t *testing.T, project string, v1 bool) pipeline.Manifest {
	t.Helper()
	dir := filepath.Join(project, "_output", "ape", "tasks", "apex-foo", "latest")
	if v1 {
		dir = filepath.Join(project, "development", ".apex", "runs", "tasks", "apex-foo", "latest")
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	require.NoError(t, err)
	var m pipeline.Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	return m
}

func setConfig(t *testing.T, project, extra string) {
	t.Helper()
	cfg := filepath.Join(project, "_apex", "config.yaml")
	data, _ := os.ReadFile(cfg)
	require.NoError(t, os.WriteFile(cfg, append(data, []byte(extra)...), 0o644))
}

// Backlog 29 end to end: in a v1 project ape task passes effort as
// claude's --effort, never CLAUDE_CODE_EFFORT_LEVEL (the inherited one
// included), uses no effort table and no legacy default, exports the
// resolved values, and records the decision as launch: in the manifest.
func TestTask_V1LaunchModelAndEffort(t *testing.T) {
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	ape := buildApe(t)
	run := func(t *testing.T, project string, env []string, flags ...string) {
		t.Helper()
		args := append([]string{"task", "apex-foo", "--idle-timeout", "30s", "--output-format", "json", "--cwd", project}, flags...)
		_, code := runApe(t, ape, project, env, args...)
		require.Equal(t, 0, code)
	}

	t.Run("config top-level effort", func(t *testing.T) {
		project, fake, env := launchProject(t, ape, true)
		setConfig(t, project, "effort: medium\n")
		run(t, project, env)
		seen := readLaunch(t, fake)
		require.Contains(t, seen.args, "--dangerously-skip-permissions --effort medium")
		require.NotContains(t, seen.args, "--model")
		require.NotContains(t, seen.args, "effortLevel", "no effort table in --settings")
		require.Empty(t, seen.envEffort, "CLAUDE_CODE_EFFORT_LEVEL is scrubbed and never set")
		require.Equal(t, "/medium", seen.session)
		m := taskManifest(t, project, true)
		require.Equal(t, &pipeline.LaunchRecord{ModelSource: "default", Effort: "medium", EffortSource: "config-default"}, m.Launch)
		require.Nil(t, m.ReviewLaunch)
	})

	t.Run("flags, then models.<skill>", func(t *testing.T) {
		project, fake, env := launchProject(t, ape, true)
		setConfig(t, project, "effort: medium\nmodels:\n  apex-foo: {model: claude-haiku-5-5, effort: low}\n")
		run(t, project, env, "--effort", "high")
		seen := readLaunch(t, fake)
		require.Contains(t, seen.args, "--model claude-haiku-5-5 --effort high")
		require.Empty(t, seen.envEffort)
		require.Equal(t, "claude-haiku-5-5/high", seen.session)
		m := taskManifest(t, project, true)
		require.Equal(t, &pipeline.LaunchRecord{Model: "claude-haiku-5-5", ModelSource: "config", Effort: "high", EffortSource: "flag"}, m.Launch)
	})

	t.Run("nothing decides: nothing passed", func(t *testing.T) {
		project, fake, env := launchProject(t, ape, true)
		run(t, project, env)
		seen := readLaunch(t, fake)
		require.NotContains(t, seen.args, "--effort")
		require.NotContains(t, seen.args, "effortLevel")
		require.Empty(t, seen.envEffort, "no silent xhigh")
		m := taskManifest(t, project, true)
		require.Equal(t, &pipeline.LaunchRecord{ModelSource: "default", EffortSource: "default"}, m.Launch)
	})

	// A project that is not v1 keeps today's rule exactly: an explicit
	// effort is CLAUDE_CODE_EFFORT_LEVEL; with none (and no framework
	// config, so no table) the legacy xhigh, process-wide; no --effort, no
	// launch: record.
	t.Run("legacy project unchanged", func(t *testing.T) {
		project, fake, env := launchProject(t, ape, false)
		run(t, project, env, "--effort", "high")
		seen := readLaunch(t, fake)
		require.NotContains(t, seen.args, "--effort")
		require.Equal(t, "high", seen.envEffort)
		require.Nil(t, taskManifest(t, project, false).Launch)

		run(t, project, env)
		seen = readLaunch(t, fake)
		require.NotContains(t, seen.args, "--effort")
		require.Equal(t, "xhigh", seen.envEffort, "the legacy default")
	})
}
