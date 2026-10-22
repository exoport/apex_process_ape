package apecmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// releasedFramework is fakeFrameworkForContract tagged as a release.
func releasedFramework(t *testing.T, tag string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	fw := t.TempDir()
	fakeFrameworkForContract(t, fw)
	gitRun(t, fw, "tag", "-a", tag, "-m", tag)
	return fw
}

// committedProject is a git project with a committed baseline and one
// untracked file of the user's that no install may commit.
func committedProject(t *testing.T) string {
	t.Helper()
	root := newTestProject(t, realProjectConfig)
	gitInit(t, root)
	gitCommitAll(t, root, "project baseline")
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.txt"), []byte("mine"), 0o644))
	return root
}

func runFramework(t *testing.T, sub, repo, root string, args ...string) (string, error) {
	t.Helper()
	r, cwd := repo, root
	cmd := newFrameworkUpdateCmd(&r, &cwd)
	if sub == "setup" {
		cmd = newFrameworkSetupCmd(&r, &cwd)
	}
	cmd.SetArgs(args)
	var buf strings.Builder
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	return buf.String(), err
}

func withApeVersion(t *testing.T, v string) {
	t.Helper()
	old := Version
	Version = v
	t.Cleanup(func() { Version = old })
}

// TestContract_FrameworkUpdateNoCommitWritesNoCommits keeps the v0.3.x
// property available: with --no-commit, the whole result sits in the
// working tree for one `git diff`, and the project's history is untouched.
func TestContract_FrameworkUpdateNoCommitWritesNoCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	fwRepo := t.TempDir()
	fakeFrameworkForContract(t, fwRepo)
	root := newTestProject(t, realProjectConfig)
	gitInit(t, root)
	writeLegacyLedger(t, root)
	gitCommitAll(t, root, "project baseline")
	before := gitLog(t, root)

	_, err := runFramework(t, "setup", fwRepo, root, "--from-worktree", "--no-commit", "--no-fetch", "--no-bootstrap")
	require.NoError(t, err)
	out, err := runFramework(t, "update", fwRepo, root, "--from-worktree", "--no-commit", "--no-fetch")
	require.NoError(t, err, out)

	require.Equal(t, before, gitLog(t, root), "--no-commit wrote a commit into the project:\n%s", out)
	require.False(t, pendingMigrations(root)[0].Pending,
		"the migration must have run for the no-commit assertion to mean anything")
	require.NotEmpty(t, gitRun(t, root, "status", "--porcelain"), "the result is left in the working tree")
}

// The default install commits the release it installed — one commit, with
// the trailers the framework's release record reads — and never a file of
// the user's that was already untracked.
func TestFrameworkInstall_CommitsTheRelease(t *testing.T) {
	withApeVersion(t, "0.4.0")
	fw := releasedFramework(t, "v1.0.0")
	root := committedProject(t)
	baseline := gitRun(t, root, "rev-parse", "HEAD")

	out, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap")
	require.NoError(t, err, out)
	require.Equal(t, baseline, gitRun(t, root, "rev-parse", "HEAD~1"), "exactly one commit")
	msg := gitRun(t, root, "log", "-1", "--format=%B")
	require.Contains(t, msg, "chore(framework): install APEX framework v1.0.0")
	require.Contains(t, msg, "Framework-Version: v1.0.0")
	require.Contains(t, msg, "Framework-Commit: "+gitRun(t, fw, "rev-parse", "v1.0.0^{commit}"))
	require.Contains(t, msg, "Generator: ape framework setup")
	require.Equal(t, "?? notes.txt", gitRun(t, root, "status", "--porcelain"),
		"only the user's own untracked file is left, uncommitted")

	meta, err := framework.ReadMetadata(root)
	require.NoError(t, err)
	require.Equal(t, framework.SourceTag, meta.Framework.Source)

	// Nothing new: no commit.
	head := gitRun(t, root, "rev-parse", "HEAD")
	out, err = runFramework(t, "update", fw, root, "--no-fetch")
	require.NoError(t, err, out)
	require.Equal(t, head, gitRun(t, root, "rev-parse", "HEAD"), "an update that changed nothing commits nothing:\n%s", out)

	// A newer release: one update commit. Unreleased work on the
	// framework's main is never installed.
	require.NoError(t, os.WriteFile(filepath.Join(fw, framework.SubtreeSkills, "apex-foo", "SKILL.md"), []byte("# v1.1.0\n"), 0o644))
	gitCommitAll(t, fw, "next")
	gitRun(t, fw, "tag", "v1.1.0")
	require.NoError(t, os.WriteFile(filepath.Join(fw, framework.SubtreeSkills, "apex-foo", "SKILL.md"), []byte("# unreleased\n"), 0o644))
	gitCommitAll(t, fw, "unreleased")

	out, err = runFramework(t, "update", fw, root, "--no-fetch")
	require.NoError(t, err, out)
	require.Contains(t, gitRun(t, root, "log", "-1", "--format=%s"), "chore(framework): update APEX framework to v1.1.0")
	b, err := os.ReadFile(filepath.Join(root, framework.ProjectSkillsDir, "apex-foo", "SKILL.md"))
	require.NoError(t, err)
	require.Equal(t, "# v1.1.0\n", string(b))
}

func TestFrameworkInstall_RefusesADirtyTreeBeforeWriting(t *testing.T) {
	withApeVersion(t, "0.4.0")
	fw := releasedFramework(t, "v1.0.0")
	root := committedProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.yaml"), []byte("edited: true\n"), 0o644))

	_, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap")
	require.Equal(t, exitCodeProjectSkillsModified, exitCodeOf(t, err))
	_, statErr := os.Stat(framework.MetadataPath(root))
	require.True(t, os.IsNotExist(statErr), "nothing is written")
}

// Below the release's min_ape_version, without a terminal: no prompt, no
// update, nothing written, exit 11 and the command to run.
func TestFrameworkInstall_BelowMinimumWithoutATerminal(t *testing.T) {
	withApeVersion(t, "0.3.1")
	fw := t.TempDir()
	fakeFrameworkForContract(t, fw)
	require.NoError(t, os.WriteFile(filepath.Join(fw, framework.ProjectApeCommands), []byte("min_ape_version: 99.0.0\n"), 0o644))
	gitCommitAll(t, fw, "needs a future ape")
	gitRun(t, fw, "tag", "v1.0.0")
	root := committedProject(t)

	_, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap")
	require.Equal(t, exitCodeApeBelowMinimum, exitCodeOf(t, err))
	_, statErr := os.Stat(framework.MetadataPath(root))
	require.True(t, os.IsNotExist(statErr), "nothing is written")

	// An rc of exactly the minimum passes it.
	require.NoError(t, os.WriteFile(filepath.Join(fw, framework.ProjectApeCommands), []byte("min_ape_version: 0.4.0\n"), 0o644))
	gitCommitAll(t, fw, "needs 0.4.0")
	gitRun(t, fw, "tag", "v1.1.0")
	withApeVersion(t, "0.4.0-rc.1")
	out, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap")
	require.NoError(t, err, out)
}

func TestFrameworkInstall_UsageErrors(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"--version", "v1.0.0", "--from-worktree"},
		{"--version", "v1.0.0-beta"},
	} {
		_, err := runFramework(t, "update", t.TempDir(), root, args...)
		require.Equal(t, ExitUsage, exitCodeOf(t, err), "%v", args)
	}
}

func TestDoctor_FrameworkApeVersion(t *testing.T) {
	cases := []struct {
		ape, manifest string
		want          DoctorStatus
	}{
		{"0.4.0", "min_ape_version: 0.4.0\n", StatusOK},
		{"0.4.0-rc.1", "min_ape_version: 0.4.0\n", StatusOK},
		{"0.3.1", "min_ape_version: 0.4.0\n", StatusFail},
		{"dev", "min_ape_version: 0.4.0\n", StatusWarn},
		{"0.3.1", "required_commands: []\n", StatusInfo},
	}
	for _, c := range cases {
		withApeVersion(t, c.ape)
		root := commandSurfaceProject(t, c.manifest)
		res := checkFrameworkApeVersion(context.Background(), doctorEnv{ProjectRoot: root})
		require.Equal(t, c.want, res.Status, "ape %s, %q: %s", c.ape, c.manifest, res.Message)
	}
}

// A default update never goes backwards from a candidate installed by name.
func TestFrameworkUpdate_KeepsACandidateAheadOfTheNewestRelease(t *testing.T) {
	withApeVersion(t, "0.4.0")
	fw := releasedFramework(t, "v1.0.0")
	require.NoError(t, os.WriteFile(filepath.Join(fw, framework.SubtreeSkills, "apex-foo", "SKILL.md"), []byte("# rc\n"), 0o644))
	gitCommitAll(t, fw, "rc")
	gitRun(t, fw, "tag", "v1.1.0-rc.1")
	root := committedProject(t)
	_, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap", "--version", "v1.1.0-rc.1")
	require.NoError(t, err)
	head := gitRun(t, root, "rev-parse", "HEAD")

	out, err := runFramework(t, "update", fw, root, "--no-fetch")
	require.NoError(t, err, out)
	require.Equal(t, head, gitRun(t, root, "rev-parse", "HEAD"), "nothing reinstalled, nothing committed")
	meta, err := framework.ReadMetadata(root)
	require.NoError(t, err)
	require.Equal(t, "v1.1.0-rc.1", meta.Framework.VersionTag)

	_, err = runFramework(t, "update", fw, root, "--no-fetch", "--version", "v1.0.0")
	require.NoError(t, err)
	meta, err = framework.ReadMetadata(root)
	require.NoError(t, err)
	require.Equal(t, "v1.0.0", meta.Framework.VersionTag, "--version goes back when asked")
}

// Backlog 26: `ape framework setup` and `update` never touch an APEX v1
// project's _apex/state/ — not even when the framework itself ships a file
// at that path — and their commits take nothing from it.
func TestFrameworkInstall_LeavesV1StateByteIdentical(t *testing.T) {
	withApeVersion(t, "1.0.0")
	fw := releasedFramework(t, "v1.0.0")
	for rel, body := range map[string]string{
		"_apex/state/milestones.json": "{\"from\": \"the framework\"}\n",
		"_apex/state/.gitignore":      "# the framework's\n",
		// An apex-v1 release: a v1 project takes no other (RG-3).
		framework.ProjectApeCommands: "min_ape_version: 1.0.0\n",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(fw, rel)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(fw, rel), []byte(body), 0o644))
	}
	gitCommitAll(t, fw, "a framework that ships _apex/state")
	gitRun(t, fw, "tag", "v1.0.1")

	root := committedProject(t)
	state := runlog.V1StateDir(root)
	for rel, body := range map[string]string{
		".gitignore":                     runlog.V1StateGitignore,
		"milestones.json":                "{\"M1\": {\"result\": \"green\"}}\n",
		"decisions.json":                 "{\"ADR-0001\": {\"by\": \"ann\"}}\n",
		"project.json":                   "{\"brownfield\": true}\n",
		"logs/add-wave/T1.log":           "ok\n",
		"hooks/sess-1.jsonl":             "{\"verdict\": \"allow\"}\n",
		"runs/tasks/apex-apply/r1/x.txt": "run\n",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(state, rel)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(state, rel), []byte(body), 0o644))
	}
	gitCommitAll(t, root, "v1 state")
	baseline := gitRun(t, root, "rev-parse", "HEAD")
	before := treeBytes(t, state)

	for _, args := range [][]string{
		{"setup", "--no-fetch", "--no-bootstrap"},
		{"update", "--no-fetch"},
		{"setup", "--no-fetch", "--no-bootstrap", "--force"},
	} {
		out, err := runFramework(t, args[0], fw, root, args[1:]...)
		require.NoError(t, err, "%v: %s", args, out)
		require.Equal(t, before, treeBytes(t, state), "%v changed _apex/state/", args)
	}
	require.Empty(t, gitRun(t, root, "log", "--format=%h", "--name-only", baseline+"..HEAD", "--", "_apex/state"),
		"no install commit touched _apex/state/")
	require.NotEqual(t, baseline, gitRun(t, root, "rev-parse", "HEAD"), "the install did commit — the assertion means something")
}

// treeBytes maps every file under dir to its content.
func treeBytes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	}))
	return out
}

// migrationEntry is a derivable entry whose check never reports it applied
// — so only the ledger stands between it and a second run — and whose
// command counts its runs in <name>.count.
func migrationEntry(id, version, name string) string {
	return "---\nid: " + id + "\nversion: " + version + "\nseq: 1\nkind: derivable\nblocking: false\n" +
		"check: \"exit 1\"\ncommand: \"echo run >> " + name + ".count\"\nsummary: count a run\n---\n\nbody\n"
}

// Backlog 20: a v1 project keeps framework.yaml's applied-migration
// ledger. Its _apex/config.yaml is the minimal v1 shape, v1 commands have
// run (project init, ape's state in _apex/state/), and framework.yaml is
// regenerated by the update — and still no applied migration runs twice,
// the ledger keeps every row and appends the new one, and _apex/state/ is
// untouched.
func TestFrameworkUpdate_V1ProjectKeepsTheMigrationLedger(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(`migration commands run with cmd /c, whose echo writes "run \r\n"`)
	}
	withApeVersion(t, "0.4.0")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	fw := t.TempDir()
	fakeFrameworkForContract(t, fw)
	mk := func(rel, body string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(fw, rel)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(fw, rel), []byte(body), 0o644))
	}
	mk(framework.SubtreeMigrations+"/v0.1.0_seq-01_x.md", migrationEntry("v0.1.0_seq-01", "0.1.0", "x"))
	gitCommitAll(t, fw, "migration x")
	gitRun(t, fw, "tag", "-a", "v1.0.0", "-m", "v1.0.0")

	root := committedProject(t)
	out, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap")
	require.NoError(t, err, out)
	out, err = runFramework(t, "update", fw, root, "--no-fetch")
	require.NoError(t, err, out)
	require.Equal(t, "run\n", readFile(t, filepath.Join(root, "x.count")), "x ran at the first update:\n%s", out)

	// The project becomes v1: the minimal config, ape project init, and
	// ape's state recorded and committed.
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.yaml"),
		[]byte("project_name: axon\ndevelopment_folder: development\nmodels:\n  apex-review: sonnet\nregression: ./suite.sh\n"), 0o644))
	gitCommitAll(t, root, "config: the v1 shape")
	_, err = apexv1.Init(t.Context(), root, true)
	require.NoError(t, err)
	state := runlog.V1StateDir(root)
	require.NoError(t, os.WriteFile(filepath.Join(state, "milestones.json"), []byte("{}\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(state, "logs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(state, "logs", "gate-M1.json"), []byte("{}\n"), 0o644))
	gitCommitAll(t, root, "v1 records")
	stateBefore := treeBytes(t, state)

	mk(framework.SubtreeMigrations+"/v1.1.0_seq-01_y.md", migrationEntry("v1.1.0_seq-01", "1.1.0", "y"))
	// The project is apex-v1 now, so its next release must be one (RG-3).
	mk(framework.ProjectApeCommands, "min_ape_version: 1.0.0\n")
	withApeVersion(t, "1.0.0")
	gitCommitAll(t, fw, "migration y")
	gitRun(t, fw, "tag", "-a", "v1.1.0", "-m", "v1.1.0")

	out, err = runFramework(t, "update", fw, root, "--no-fetch")
	require.NoError(t, err, out)
	require.Equal(t, "run\n", readFile(t, filepath.Join(root, "x.count")), "x is in the ledger: never re-applied:\n%s", out)
	require.Equal(t, "run\n", readFile(t, filepath.Join(root, "y.count")), "y is new: applied once:\n%s", out)
	meta, err := framework.ReadMetadata(root)
	require.NoError(t, err)
	require.Equal(t, "v1.1.0", meta.Framework.VersionTag, "framework.yaml was regenerated by the update")
	ids := make([]string, 0, len(meta.Migrations))
	for _, m := range meta.Migrations {
		ids = append(ids, m.ID)
	}
	require.Equal(t, []string{"v0.1.0_seq-01", "v1.1.0_seq-01"}, ids, "the ledger keeps x and appends y, in order")
	require.Equal(t, stateBefore, treeBytes(t, state), "the update left _apex/state/ alone")

	// And again: a no-op, nothing re-applied.
	out, err = runFramework(t, "update", fw, root, "--no-fetch")
	require.NoError(t, err, out)
	require.Equal(t, "run\n", readFile(t, filepath.Join(root, "y.count")))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// D155: the install commit is scanned like every commit ape makes; a
// framework file that adds a secret refuses it with commit.secret (exit 1),
// committing nothing and leaving the install staged.
func TestFrameworkInstall_RefusesASecret(t *testing.T) {
	withApeVersion(t, "0.4.0")
	fw := t.TempDir()
	fakeFrameworkForContract(t, fw)
	token := "gh" + "p_" + "aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5"
	require.NoError(t, os.WriteFile(filepath.Join(fw, framework.SubtreeSkills, "apex-foo", "SKILL.md"), []byte("# foo\n\nexport GITHUB_TOKEN="+token+"\n"), 0o644))
	gitCommitAll(t, fw, "a leaked token")
	gitRun(t, fw, "tag", "-a", "v1.0.0", "-m", "v1.0.0")
	root := committedProject(t)
	head := gitRun(t, root, "rev-parse", "HEAD")

	out, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap")
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err), out)
	require.ErrorContains(t, err, "commit.secret: nothing was committed: chore(framework): install APEX framework v1.0.0 adds a secret: ")
	require.ErrorContains(t, err, "apex-foo/SKILL.md:3 rule github-pat: ")
	require.NotContains(t, err.Error(), token)
	require.Equal(t, head, gitRun(t, root, "rev-parse", "HEAD"), "nothing was committed")
	require.Contains(t, gitRun(t, root, "diff", "--cached", "--name-only"), "apex-foo/SKILL.md", "the install is staged")
}
