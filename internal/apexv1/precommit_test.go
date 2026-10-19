package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// initRepo is a git repo with one commit and Init run in it.
func initRepo(t *testing.T) (*Project, *InitResult) {
	t.Helper()
	needSh(t)
	gitEnv(t)
	asPerson(t)
	root := t.TempDir()
	run(t, root, "init", "-q", "-b", "main")
	run(t, root, "commit", "-q", "--allow-empty", "-m", "root")
	res, err := Init(ctx(), root, false)
	require.NoError(t, err)
	p, err := Open(root)
	require.NoError(t, err)
	return p, res
}

func hookNotices(p *Project) []Diagnostic {
	var out []Diagnostic
	for _, n := range p.Notices() {
		if n.Code == CodeHooksPreCommitAbsent {
			out = append(out, n)
		}
	}
	return out
}

// Q89: init installs ape's pre-commit hook where git looks for it, outside
// its commit (.git/hooks is never versioned); a second init leaves it; an
// init on a checkout without it (a fresh clone) installs it again; with it
// in place project check has no hook notice.
func TestInit_InstallsThePreCommitHook(t *testing.T) {
	p, res := initRepo(t)
	require.Equal(t, &PreCommitHook{Path: ".git/hooks/pre-commit", Status: PreCommitInstalled}, res.PreCommit)
	path := filepath.Join(p.Root, ".git", "hooks", "pre-commit")
	require.Equal(t, preCommitScript, read(t, p, ".git/hooks/pre-commit"))
	st, err := os.Stat(path)
	require.NoError(t, err)
	require.NotZero(t, st.Mode()&0o100, "executable")
	require.True(t, strings.HasPrefix(preCommitScript, "#!/bin/sh\n"+preCommitMarker))
	require.Contains(t, preCommitScript, `[ "$APE_SCANNED" = 1 ] && exit 0`)
	require.Contains(t, preCommitScript, "\nexec ape hook pre-commit\n")
	clean(t, p, "project init")
	require.Empty(t, hookNotices(p))

	res, err = Init(ctx(), p.Root, false)
	require.NoError(t, err)
	require.Equal(t, PreCommitPresent, res.PreCommit.Status)

	require.NoError(t, os.Remove(path))
	ns := hookNotices(p)
	require.Len(t, ns, 1)
	require.Equal(t, Diagnostic{
		Severity: SeverityNotice, Code: CodeHooksPreCommitAbsent, Action: ActionPerson, File: ".git/hooks/pre-commit",
		Message: ".git/hooks/pre-commit is absent (git does not version it, so a fresh clone has none): a commit ape does not make (a session's own git commit, a person's) is not scanned for secrets until the archive",
		Fix:     "`ape project init` installs ape's",
	}, ns[0])
	res, err = Init(ctx(), p.Root, false)
	require.NoError(t, err)
	require.Equal(t, PreCommitInstalled, res.PreCommit.Status, "re-running init installs a missing hook")
	require.FileExists(t, path)
	require.Empty(t, hookNotices(p))
	require.NotContains(t, findingMessages(p.Check()), "pre-commit", "a notice is not a finding")
}

// Q89: a pre-commit hook that is not ape's is never touched; project check
// names the line to add, and stops once the hook runs it.
func TestInit_KeepsAForeignPreCommitHook(t *testing.T) {
	needSh(t)
	gitEnv(t)
	asPerson(t)
	root := t.TempDir()
	run(t, root, "init", "-q", "-b", "main")
	foreign := "#!/bin/sh\nmake lint\n"
	path := filepath.Join(root, ".git", "hooks", "pre-commit")
	require.NoError(t, os.WriteFile(path, []byte(foreign), 0o755))
	res, err := Init(ctx(), root, false)
	require.NoError(t, err)
	require.Equal(t, &PreCommitHook{Path: ".git/hooks/pre-commit", Status: PreCommitKept}, res.PreCommit)
	p, err := Open(root)
	require.NoError(t, err)
	require.Equal(t, foreign, read(t, p, ".git/hooks/pre-commit"))
	ns := hookNotices(p)
	require.Len(t, ns, 1)
	require.Equal(t, ".git/hooks/pre-commit is another tool's hook and does not run ape's scan: a commit ape does not make (a session's own git commit, a person's) is not scanned for secrets until the archive", ns[0].Message)
	require.Equal(t, "add the line `ape hook pre-commit || exit 1` to it (ape never edits a hook it did not write)", ns[0].Fix)
	require.Equal(t, ActionPerson, ns[0].Action)

	// A comment naming it is not running it.
	require.NoError(t, os.WriteFile(path, []byte(foreign+"# ape hook pre-commit\n"), 0o755))
	require.Len(t, hookNotices(p), 1)
	require.NoError(t, os.WriteFile(path, []byte(foreign+"ape hook pre-commit || exit 1\n"), 0o755))
	require.Empty(t, hookNotices(p))
	res, err = Init(ctx(), root, false)
	require.NoError(t, err)
	require.Equal(t, &PreCommitHook{Path: ".git/hooks/pre-commit", Status: PreCommitKept, RunsApe: true}, res.PreCommit)

	// Not executable: git skips it.
	require.NoError(t, os.Chmod(path, 0o644))
	ns = hookNotices(p)
	require.Len(t, ns, 1)
	require.Equal(t, "`chmod +x .git/hooks/pre-commit`", ns[0].Fix)
}

// Q89: core.hooksPath is where git looks, so it is where init installs;
// a hooks folder inside the project is versioned, and the hook joins the
// init commit.
func TestInit_PreCommitHookHonoursCoreHooksPath(t *testing.T) {
	needSh(t)
	gitEnv(t)
	asPerson(t)
	root := t.TempDir()
	run(t, root, "init", "-q", "-b", "main")
	run(t, root, "config", "core.hooksPath", ".githooks")
	res, err := Init(ctx(), root, false)
	require.NoError(t, err)
	require.Equal(t, &PreCommitHook{Path: ".githooks/pre-commit", Status: PreCommitInstalled}, res.PreCommit)
	require.NoFileExists(t, filepath.Join(root, ".git", "hooks", "pre-commit"))
	p, err := Open(root)
	require.NoError(t, err)
	require.Contains(t, run(t, root, "show", "--name-only", "--format=", "HEAD"), ".githooks/pre-commit")
	clean(t, p, "project init")
}

// Q89: no git work tree, no hook and no notice.
func TestInit_NoPreCommitHookOutsideGit(t *testing.T) {
	needSh(t)
	asPerson(t)
	root := t.TempDir()
	res, err := Init(ctx(), root, false)
	require.NoError(t, err)
	require.Nil(t, res.PreCommit)
	p, err := Open(root)
	require.NoError(t, err)
	require.Empty(t, hookNotices(p))
}

// Q89: `ape hook pre-commit` is the commit scan of what is staged — the
// same rules and refusal — and passes a clean commit, a project that is
// not v1, and a commit ape scanned already (APE_SCANNED=1).
func TestPreCommitScan(t *testing.T) {
	p, _ := initRepo(t)
	t.Setenv(EnvScanned, "")
	write(t, p, "app.sh", "#!/bin/sh\necho hi\n")
	run(t, p.Root, "add", "app.sh")
	require.NoError(t, PreCommitScan(ctx(), p.Root))

	write(t, p, "app.sh", "#!/bin/sh\necho hi\nexport GITHUB_TOKEN="+fakeToken+"\n")
	run(t, p.Root, "add", "app.sh")
	fe := secretFail(t, PreCommitScan(ctx(), p.Root))
	require.Contains(t, fe.Msg, "nothing was committed: this commit adds a secret: app.sh:3 rule github-pat: ghp_")
	require.Contains(t, fe.Msg, "its files stay staged: take the secret out, `git add` them again and commit again")
	require.NotContains(t, fe.Msg, fakeToken)
	require.Equal(t, CodeCommitSecret, ClassifyFindings(fe.Findings)[0].Code)
	// From a subfolder too: git runs hooks at the top, a person may not.
	require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "sub"), 0o755))
	_ = secretFail(t, PreCommitScan(ctx(), filepath.Join(p.Root, "sub")))

	t.Setenv(EnvScanned, "1")
	require.NoError(t, PreCommitScan(ctx(), p.Root), "ape's own commit, scanned already")

	t.Setenv(EnvScanned, "")
	plain := t.TempDir()
	run(t, plain, "init", "-q", "-b", "main")
	writeAt(t, plain, "app.sh", "export GITHUB_TOKEN="+fakeToken+"\n")
	run(t, plain, "add", "app.sh")
	require.NoError(t, PreCommitScan(ctx(), plain), "not a v1 project")
}

// Q89: ape's own commits are not scanned twice by its hook. A record
// commit passes --no-verify, so no hook runs (one that always refuses
// does not stop it); the archive squash runs the project's hooks with
// APE_SCANNED=1, which ape's hook script checks before starting ape.
func TestPreCommitHook_ApesOwnCommitsAreNotRescanned(t *testing.T) {
	p, _ := initRepo(t)
	t.Setenv(EnvScanned, "")
	hook := filepath.Join(p.Root, ".git", "hooks", "pre-commit")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho refused >&2\nexit 1\n"), 0o755))
	write(t, p, "notes.md", "a record\n")
	sha, err := p.commitRecord(ctx(), "apex: a record", "", "notes.md")
	require.NoError(t, err)
	require.NotEmpty(t, sha)

	seen := filepath.Join(t.TempDir(), "seen")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho \"APE_SCANNED=$APE_SCANNED\" > '"+seen+"'\n"), 0o755))
	write(t, p, "squash.md", "squashed\n")
	run(t, p.Root, "add", "squash.md")
	out, err := p.commitSquash(ctx(), "apex: squash", true)
	require.NoError(t, err, out)
	data, err := os.ReadFile(seen)
	require.NoError(t, err)
	require.Equal(t, "APE_SCANNED=1\n", string(data))

	// The installed script skips at once under it: an ape on PATH that
	// refuses everything is never started.
	require.NoError(t, os.WriteFile(hook, []byte(preCommitScript), 0o755))
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ape"), []byte("#!/bin/sh\necho ape ran >&2\nexit 1\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	write(t, p, "squash2.md", "squashed\n")
	run(t, p.Root, "add", "squash2.md")
	out, err = p.commitSquash(ctx(), "apex: squash 2", true)
	require.NoError(t, err, out)
}

// Q89: the lift lays down the pre-commit hook with the rest of init's
// skeleton (outside its commit), and a dry run writes none; a foreign hook
// is kept.
func TestLift_InstallsThePreCommitHook(t *testing.T) {
	root := liftFixture(t, true)
	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	dry, err := Lift(ctx(), root, LiftOptions{DryRun: true})
	require.NoError(t, err)
	require.Nil(t, dry.Init)
	require.NoFileExists(t, hook)

	res, err := Lift(ctx(), root, LiftOptions{NoVerifyRun: true, Exclude: []string{"schema/**"}})
	require.NoError(t, err)
	require.Equal(t, &PreCommitHook{Path: ".git/hooks/pre-commit", Status: PreCommitInstalled}, res.Init.PreCommit)
	data, err := os.ReadFile(hook)
	require.NoError(t, err)
	require.Equal(t, preCommitScript, string(data))
	require.NotContains(t, run(t, root, "show", "--name-only", "--format=", "HEAD"), "pre-commit")

	foreign := liftFixture(t, true)
	hook = filepath.Join(foreign, ".git", "hooks", "pre-commit")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	res, err = Lift(ctx(), foreign, LiftOptions{NoVerifyRun: true, Exclude: []string{"schema/**"}})
	require.NoError(t, err)
	require.Equal(t, PreCommitKept, res.Init.PreCommit.Status)
	data, err = os.ReadFile(hook)
	require.NoError(t, err)
	require.Equal(t, "#!/bin/sh\nexit 0\n", string(data))
}
