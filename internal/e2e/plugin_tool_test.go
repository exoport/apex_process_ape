//go:build !windows

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// pluginsConfig lists plugin apish, which the fake CLI reports installed
// at its ref from a fake install holding skill nats-expert.
const pluginsConfig = "marketplaces:\n  team-tools: {source: github, repo: org/claude-plugins}\n" +
	"plugins:\n  - {name: apish, marketplace: team-tools, ref: v1.4.0}\n"

// installPlugin makes the fake `claude plugin list --json` report apish
// installed at project scope in project, from an install dir carrying
// skills/nats-expert/SKILL.md.
func installPlugin(t *testing.T, project, fake string) {
	t.Helper()
	install := filepath.Join(fake, "plugins", "apish")
	require.NoError(t, os.MkdirAll(filepath.Join(install, "skills", "nats-expert"), 0o755))
	require.NoError(t, writeFile(filepath.Join(install, "skills", "nats-expert", "SKILL.md"),
		"---\nname: nats-expert\ndescription: NATS\n---\nBody.\n"))
	list, err := json.Marshal([]map[string]string{{
		"id": "apish@team-tools", "version": "1.4.0", "scope": "project", "projectPath": project, "installPath": install,
	}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(fake, "installed.json"), list, 0o644))
}

// F-E1-1: ape task starts a plugin skill its project lists — the
// preflight resolves it from the plugin's install, as `ape project check`
// does, the session is typed /<plugin>:<skill>, and the run is filed under
// a folder whose name spells the ':' as "__" while the manifest keeps the
// name. A plugin skill the project does not list, or that its listed
// plugin lacks, is refused with exit 2 before anything spawns.
func TestTask_V1PluginSkill(t *testing.T) {
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	ape := buildApe(t)
	project, fake, env := launchProject(t, ape, true)
	setConfig(t, project, pluginsConfig)
	installPlugin(t, project, fake)
	commit := exec.Command("git", "-C", project, "commit", "-qam", "plugins")
	commit.Env = env
	require.NoError(t, commit.Run())

	_, code := runApe(t, ape, project, env, "task", "apish:nats-expert", "--args", "--change x", "--idle-timeout", "30s", "--output-format", "json", "--cwd", project)
	require.Equal(t, 0, code)
	line, err := os.ReadFile(filepath.Join(fake, "line"))
	require.NoError(t, err)
	require.Equal(t, "/apish:nats-expert --autonomous --change x", string(line))
	seen := readLaunch(t, fake)
	require.Contains(t, seen.args, `"enabledPlugins":{"apish@team-tools":true}`)

	tasks := filepath.Join(project, "_apex", "state", "runs", "tasks")
	require.NoDirExists(t, filepath.Join(tasks, "apish:nats-expert"), "no ':' in a run folder's name")
	data, err := os.ReadFile(filepath.Join(tasks, "apish__nats-expert", "latest", "manifest.yaml"))
	require.NoError(t, err)
	var m pipeline.Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	require.Equal(t, "apish:nats-expert", m.Pipeline.Name, "the manifest keeps the skill's name")
	require.Equal(t, pipeline.StatusCompleted, m.Status)
	require.Equal(t, "apish:nats-expert", m.Stages[0].Steps[0].Skill)
	out, code := runApe(t, ape, project, env, "costs", "run", m.RunID, "--output-format", "json")
	require.Equal(t, 0, code)
	require.Contains(t, out, `"name": "apish:nats-expert"`, "a reader of the run folders gives the name back")

	for name, tc := range map[string]struct{ skill, want string }{
		"unlisted plugin": {"nope:x", "no plugin nope in plugins: — list the plugin nope in plugins: and run `ape plugins sync`, or fix the skill name"},
		"skill missing":   {"apish:nope", "plugin apish has no skill nope — fix the skill name, or install a version of apish that ships it"},
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, os.Remove(filepath.Join(fake, "launch")))
			c := exec.Command(ape, "task", tc.skill, "--idle-timeout", "30s", "--cwd", project)
			c.Env = env
			out, err := c.CombinedOutput()
			require.Equal(t, 2, exitStatus(err), "%s", out)
			require.Contains(t, string(out), tc.want)
			require.NoFileExists(t, filepath.Join(fake, "launch"), "no session was spawned")
			require.NoDirExists(t, filepath.Join(tasks, strings.ReplaceAll(tc.skill, ":", "__")))
			// Put a launch file back for the next case's Remove.
			require.NoError(t, writeFile(filepath.Join(fake, "launch"), ""))
		})
	}
}

const pluginReviewChange = `---
id: add-wave
milestone: M1
type: feat
touches: [greeting]
review_tools: [nats]
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

// F-E1-1 through the verb that measured it: `ape change tool` runs a
// tools.review entry whose skill is a plugin's. Its detached `ape task
// apish:nats-expert` passes the preflight, the session is typed the
// plugin's slash command, and the tool run is reported done.
func TestChangeTool_PluginReviewTool(t *testing.T) {
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	ape := buildApe(t)
	project, fake := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(strings.Replace(fakeDelegate, `settings=""`, fakePluginCLI+`settings=""`, 1)), 0o755))
	env := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
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
	installPlugin(t, project, fake)
	setConfig(t, project, pluginsConfig+"tools:\n  review:\n    - {name: nats, skill: \"apish:nats-expert\", when: \"NATS changes\"}\n")
	sh("git", "commit", "-qam", "config: a plugin review tool")
	sh(ape, "project", "accept-config", "--by", "test")
	require.NoError(t, writeFile(filepath.Join(project, "development", "project.md"),
		"# G\n## Vision\nv\n## Users\nu\n## Scope\ns\n## Out of scope\no\n## Constraints\nc\n"))
	require.NoError(t, writeWaveGate(project))
	sh(ape, "milestone", "new", "Skeleton", "--gate", "./gate.sh", "--goal", "waves")
	sh(ape, "milestone", "approve", "M1", "--by", "test")
	sh(ape, "change", "new", "add-wave", "--milestone", "M1", "--touches", "greeting")
	require.NoError(t, writeFile(filepath.Join(project, "development", "changes", "add-wave", "change.md"), pluginReviewChange))
	sh("git", "add", "-A")
	sh("git", "commit", "-q", "-m", "plan")
	sh(ape, "change", "start", "add-wave")

	// project check calls the tool installed; ape task agrees.
	out, _ := runApe(t, ape, project, env, "project", "check", "--json")
	require.NotContains(t, out, "tools.not-installed", out)

	out, code := runApe(t, ape, project, env, "change", "tool", "add-wave", "nats", "--wait", "90s", "--json")
	require.Equal(t, 0, code, out)
	var res struct {
		Slot     string `json:"slot"`
		Skill    string `json:"skill"`
		TaskExit *int   `json:"task_exit"` //nolint:tagliatelle // ape's snake_case --json contract
	}
	require.NoError(t, json.Unmarshal([]byte(out), &res), out)
	require.Equal(t, "review", res.Slot)
	require.Equal(t, "apish:nats-expert", res.Skill)
	require.NotNil(t, res.TaskExit)
	require.Equal(t, 0, *res.TaskExit)
	seen, err := os.ReadFile(filepath.Join(fake, "seen"))
	require.NoError(t, err)
	require.Contains(t, string(seen), "line=/apish:nats-expert --autonomous --change add-wave")
	require.FileExists(t, filepath.Join(project, "_apex", "state", "runs", "tasks", "apish__nats-expert", "latest", "manifest.yaml"))
	require.NoFileExists(t, filepath.Join(project, "_apex", "state", "logs", "add-wave", "tool-nats.json"), "the tool run is settled and cleared")
}
