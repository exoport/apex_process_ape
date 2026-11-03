//go:build !windows

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeClaudeAssessor is fakeClaude as a stub assessor session: it records
// its argv and APE_ASSESS, then, after the typed line, asks pre-tool (the
// copy ape injected, run by the ape under test) about one tool call per
// line of $FAKE_DIR/calls (<tool>\t<tool_input JSON>), writing each
// answer to $FAKE_DIR/decision.<n> (empty: allowed).
var fakeClaudeAssessor = strings.Replace(fakeClaude, `printf '%s' "$line" > "$FAKE_CLAUDE_LINE"`,
	`printf '%s' "$line" > "$FAKE_CLAUDE_LINE"
n=0
while IFS=$'\t' read -r tool input; do
  n=$((n+1))
  printf '{"session_id":"fake-1","hook_event_name":"PreToolUse","cwd":"%s","tool_name":"%s","tool_input":%s}' "$PWD" "$tool" "$input" | ape hook pre-tool --inline > "$FAKE_DIR/decision.$n" 2>&1
done < "$FAKE_DIR/calls"`, 1)

var fakeAssessRecorder = strings.Replace(fakeClaudeAssessor, `settings=""`,
	`printf 'args=%s\nassess=%s\ntoolchain=%s\n' "$*" "$APE_ASSESS" "$APE_TOOLCHAIN" > "$FAKE_DIR/launch"
settings=""`, 1)

// PLAN-V15-ASSESSOR step 4: `ape task apex-assess` in a batch folder (a v1
// project's main as files, with input.md) launches the confined session:
// --tools Read,Grep,Glob,Write, --setting-sources user, the skill as the
// plugin ape-assess built beside the folder (typed
// /ape-assess:apex-assess), pre-tool registered for every tool under
// APE_ASSESS. The stub session then tries each route: Bash, a Write
// outside notice.json, a Read of ~/.ssh, a Read through a path that
// resolves outside, a Glob pattern climbing out, an MCP tool — each denied
// and recorded beside the folder. The controls: a Read and a Grep inside
// the folder and the Write of notice.json are allowed, and the same Bash
// call outside an assessor session is not denied.
func TestTask_AssessorSessionIsConfined(t *testing.T) {
	for _, bin := range []string{"bash", "git", "tar"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	ape := buildApe(t)
	project, fake, env := launchProject(t, ape, true)
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(fakeAssessRecorder), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "skills", "apex-assess"), 0o755))
	require.NoError(t, writeFile(filepath.Join(project, ".claude", "skills", "apex-assess", "SKILL.md"), "---\nname: apex-assess\n---\n# assess\n"))
	gitIn(t, project, env, "add", "-A")
	gitIn(t, project, env, "commit", "-q", "-m", "the assessor skill")

	// The batch folder, as BuildAssessFolder makes it: main's files, no .git.
	batch := filepath.Join(t.TempDir(), "20261103T020000Z-1-notes")
	require.NoError(t, os.Mkdir(batch, 0o700))
	tarCmd := exec.Command("sh", "-c", "git -C \"$1\" archive --format=tar HEAD | tar -x -C \"$2\"", "sh", project, batch)
	out, err := tarCmd.CombinedOutput()
	require.NoError(t, err, string(out))
	require.NoError(t, writeFile(filepath.Join(batch, "input.md"), "# Assessor batch\n"))
	home, _ := os.UserHomeDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(batch, "out")), "a link planted after extraction")

	calls := []string{
		"Bash\t" + `{"command":"echo hi"}`,
		"Write\t" + `{"file_path":"` + filepath.Join(batch, "README.md") + `","content":"x"}`,
		"Read\t" + `{"file_path":"` + filepath.Join(home, ".ssh", "id_ed25519") + `"}`,
		"Read\t" + `{"file_path":"out/secret.txt"}`,
		"Glob\t" + `{"pattern":"../**/*.creds"}`,
		"mcp__ape__reply\t" + `{"content":"hi"}`,
		"Read\t" + `{"file_path":"input.md"}`,
		"Grep\t" + `{"pattern":"Assessor","path":"` + batch + `"}`,
		"Write\t" + `{"file_path":"notice.json","content":"{\"notices\":[]}"}`,
	}
	require.NoError(t, writeFile(filepath.Join(fake, "calls"), strings.Join(calls, "\n")+"\n"))

	_, code := runApe(t, ape, batch, env, "task", "apex-assess", "--idle-timeout", "30s", "--output-format", "json", "--cwd", batch)
	require.Equal(t, 0, code)

	launch, err := os.ReadFile(filepath.Join(fake, "launch"))
	require.NoError(t, err)
	args := string(launch)
	require.Contains(t, args, "--tools Read,Grep,Glob,Write")
	require.Contains(t, args, "--setting-sources user")
	require.Contains(t, args, "--plugin-dir "+batch+".plugin")
	require.Contains(t, args, "assess="+batch)
	require.Contains(t, args, "toolchain=local", "the hooks are this ape, never a pin in the snapshot")
	require.NotContains(t, args, `"matcher":"Edit|Write|MultiEdit|NotebookEdit|Bash|Monitor"`, "pre-tool sees every tool")
	require.Contains(t, args, `"deny":["Bash","Monitor","Edit"`)
	require.FileExists(t, filepath.Join(batch+".plugin", "skills", "apex-assess", "SKILL.md"))
	line, err := os.ReadFile(filepath.Join(fake, "line"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(line), "/ape-assess:apex-assess"), "typed: %s", line)

	for i, c := range calls {
		got, err := os.ReadFile(filepath.Join(fake, "decision."+itoa(i+1)))
		require.NoError(t, err)
		if i < 6 {
			require.Contains(t, string(got), `"permissionDecision":"deny"`, "call %d (%s)", i+1, c)
		} else {
			require.Empty(t, string(got), "call %d (%s): allowed, so pre-tool answers nothing", i+1, c)
		}
	}
	denials, err := os.ReadFile(batch + ".denied.jsonl")
	require.NoError(t, err)
	require.Equal(t, 6, strings.Count(string(denials), "\n"), "every denial recorded beside the folder:\n%s", denials)

	// The control: the same Bash call in an ordinary v1 session is not
	// denied by the assessor's rule.
	c := exec.Command(ape, "hook", "pre-tool", "--inline")
	c.Dir = project
	c.Env = append(slices.Clone(env), "APE_V1_HOOKS=inline")
	c.Stdin = strings.NewReader(`{"session_id":"x","hook_event_name":"PreToolUse","cwd":"` + project + `","tool_name":"Bash","tool_input":{"command":"echo hi"}}`)
	ctl, err := c.CombinedOutput()
	require.NoError(t, err)
	require.NotContains(t, string(ctl), "deny")
}

func gitIn(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = env
	out, err := c.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
