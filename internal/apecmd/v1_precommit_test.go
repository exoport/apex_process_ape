package apecmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// apeOnPath puts this test binary first on PATH as `ape`, as git's hooks
// resolve it, and lets the pre-commit hook scan (TestMain sets
// APE_SCANNED for every other test).
func apeOnPath(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	bin := t.TempDir()
	script := "#!/bin/sh\n" + asApeBinary + "=1 exec '" + exe + "' \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ape"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(apexv1.EnvScanned, "")
}

// gitCommit runs git commit as a model's Bash would: hooks on.
func gitCommit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	c := exec.Command("git", append([]string{"commit", "-q"}, args...)...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	return string(out), err
}

// Q89: init installs git's pre-commit hook; a model-style `git commit` of
// a planted token is refused by it (commit.secret, redacted, HEAD where it
// was, the files still staged), `git commit -a` too (git's temporary
// index), and a lone AWS key id (Q88); a clean commit passes silently.
func TestHookPreCommit_RefusesASessionsOwnCommit(t *testing.T) {
	root := v1Repo(t)
	apeOnPath(t)
	require.FileExists(t, filepath.Join(root, ".git", "hooks", "pre-commit"))

	require.NoError(t, os.WriteFile(filepath.Join(root, "app.sh"), []byte("#!/bin/sh\necho hi\n"), 0o644))
	v1Git(t, root, "add", "app.sh")
	head := v1Git(t, root, "rev-parse", "HEAD")
	out, err := gitCommit(t, root, "-m", "feat: app")
	require.NoError(t, err, out)
	require.Empty(t, out, "a clean commit: the hook prints nothing")
	require.NotEqual(t, head, v1Git(t, root, "rev-parse", "HEAD"))

	token := "gh" + "p_" + "aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5"
	require.NoError(t, os.WriteFile(filepath.Join(root, "leak.sh"), []byte("export GITHUB_TOKEN="+token+"\n"), 0o644))
	v1Git(t, root, "add", "leak.sh")
	head = v1Git(t, root, "rev-parse", "HEAD")
	out, err = gitCommit(t, root, "-m", "feat: leak")
	require.Error(t, err, out)
	require.Contains(t, out, "Error: nothing was committed: this commit adds a secret: leak.sh:1 rule github-pat: ghp_")
	require.Contains(t, out, "replace each with a placeholder (`<token>`, `$TOKEN`)")
	require.Contains(t, out, "its files stay staged")
	require.NotContains(t, out, token)
	require.Equal(t, head, v1Git(t, root, "rev-parse", "HEAD"), "HEAD did not move")
	require.Equal(t, "leak.sh", v1Git(t, root, "diff", "--cached", "--name-only"))

	v1Git(t, root, "reset", "-q")
	require.NoError(t, os.Remove(filepath.Join(root, "leak.sh")))
	require.NoError(t, os.WriteFile(filepath.Join(root, "app.sh"), []byte("#!/bin/sh\necho hi\nKEY="+"AKIA"+"MRLV32HHEU7JCMSQ"+"\n"), 0o644))
	out, err = gitCommit(t, root, "-a", "-m", "feat: key")
	require.Error(t, err, out)
	require.Contains(t, out, "app.sh:3 rule aws-access-key-id: AKIA")
	require.Equal(t, head, v1Git(t, root, "rev-parse", "HEAD"))
}

// Q89: outside a v1 project, and on a clean index, the hook says nothing
// and exits 0.
func TestHookPreCommit_SilentOutsideV1(t *testing.T) {
	root := v1Repo(t)
	t.Setenv(apexv1.EnvScanned, "")
	stdout, stderr, err := v1Exec(t, newHookCmd, "pre-commit", "--cwd", root)
	require.NoError(t, err)
	require.Empty(t, stdout+stderr)

	plain := t.TempDir()
	v1Git(t, plain, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(plain, "leak.sh"), []byte("export GITHUB_TOKEN="+"gh"+"p_"+"aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5\n"), 0o644))
	v1Git(t, plain, "add", "leak.sh")
	stdout, stderr, err = v1Exec(t, newHookCmd, "pre-commit", "--cwd", plain)
	require.NoError(t, err)
	require.Empty(t, stdout+stderr)
}

// Q89: init says what it did with the hook — installed, already there, or
// another tool's kept (with the line to add) — in text and JSON.
func TestProjectInit_SaysWhatItDidWithThePreCommitHook(t *testing.T) {
	root := v1Repo(t)
	stdout, _, err := v1Exec(t, newProjectCmd, "init", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "git hook .git/hooks/pre-commit (ape's, already installed)\n")

	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	require.NoError(t, os.Remove(hook))
	stdout, _, err = v1Exec(t, newProjectCmd, "init", "--json", "--cwd", root)
	require.NoError(t, err)
	var res struct {
		PreCommit *apexv1.PreCommitHook `json:"pre_commit"` //nolint:tagliatelle // the v1 --json payloads are snake_case by contract
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	require.Equal(t, &apexv1.PreCommitHook{Path: ".git/hooks/pre-commit", Status: "installed"}, res.PreCommit)

	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\nmake lint\n"), 0o755))
	stdout, _, err = v1Exec(t, newProjectCmd, "init", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "git hook .git/hooks/pre-commit (kept existing: not ape's, so commits ape does not make are not scanned — add the line `ape hook pre-commit || exit 1` to it; ape project check lists hooks.pre-commit-absent)\n")
	data, err := os.ReadFile(hook)
	require.NoError(t, err)
	require.Equal(t, "#!/bin/sh\nmake lint\n", string(data), "never touched")
}

// Q89: a missing or foreign hook is project check's notice (exit 0) and
// ape doctor's WARN row; with ape's hook, neither.
func TestProjectCheckAndDoctor_PreCommitHookAbsent(t *testing.T) {
	root := v1Repo(t)
	row := func() CheckResult {
		t.Helper()
		for _, c := range doctorProjectRows(t, root).Checks {
			if c.Name == "hooks.pre-commit" {
				return c
			}
		}
		t.Fatal("no hooks.pre-commit row")
		return CheckResult{}
	}
	require.Equal(t, StatusOK, row().Status)

	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	require.NoError(t, os.Remove(hook))
	_, stderr, err := v1Exec(t, newProjectCmd, "check", "--cwd", root)
	require.NoError(t, err, "a notice never fails the check")
	require.Contains(t, stderr, "notice: .git/hooks/pre-commit: .git/hooks/pre-commit is absent (git does not version it, so a fresh clone has none)")
	require.Contains(t, stderr, " — `ape project init` installs ape's\n")
	r := row()
	require.Equal(t, StatusWarn, r.Status)
	require.True(t, strings.HasPrefix(r.Message, "hooks.pre-commit-absent: .git/hooks/pre-commit is absent"), r.Message)
	require.Equal(t, "ape project init --cwd "+root, r.FixCommand)

	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\nmake lint\n"), 0o755))
	stdout, _, err := v1Exec(t, newProjectCmd, "check", "--json", "--cwd", root)
	require.NoError(t, err)
	var res struct {
		Notices []apexv1.Diagnostic `json:"notices"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	var n *apexv1.Diagnostic
	for i := range res.Notices {
		if res.Notices[i].Code == apexv1.CodeHooksPreCommitAbsent {
			n = &res.Notices[i]
		}
	}
	require.NotNil(t, n, stdout)
	require.Equal(t, apexv1.SeverityNotice, n.Severity)
	require.Equal(t, apexv1.ActionPerson, n.Action)
	require.Equal(t, "add the line `ape hook pre-commit || exit 1` to it (ape never edits a hook it did not write)", n.Fix)
	r = row()
	require.Equal(t, StatusWarn, r.Status)
	require.Empty(t, r.FixCommand, "init would not touch it")
}
