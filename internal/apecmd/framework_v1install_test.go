package apecmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

// Installing an APEX v1 release through `ape framework setup|update`
// (D163, from framework's D162 report): a v1 release ships skills and
// _apex/ape-commands.yaml and no pipelines or config template; setup seeds
// no config; a v1 install drops v0's migration entries and surfaces; and a
// blocking migration left unapplied commits nothing.

const v1ReleaseManifest = "min_ape_version: \"1.0.0\"\n"

// v1LiftEntry is a stand-in for the v1.0.0 lift entry: derivable and
// blocking, satisfied by its check unless the test says otherwise.
func v1LiftEntry(check, command string) string {
	return "---\nid: v1.0.0_seq-01\nversion: 1.0.0\nseq: 1\nkind: derivable\nblocking: true\nafter: []\n" +
		"check: \"" + check + "\"\ncommand: \"" + command + "\"\nsummary: Lift the project.\n---\nbody\n"
}

// v1Env isolates git and makes the test a person at a terminal, as v1Repo
// does, for the init half of the orders. It runs on Windows too: the
// migration checks and commands these tests' entries hold run under sh -c
// on every OS since Q163 (they ran under cmd /c there), and the helpers
// they share with TestFrameworkUpdate_V1ProjectKeepsTheMigrationLedger
// (setup, update, apexv1.Init, git) already run in the Windows job (W8b).
func v1Env(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"git", "sh"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not on PATH")
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("APE_SESSION", "")
	t.Setenv("CLAUDECODE", "")
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "t"}, {"GIT_AUTHOR_EMAIL", "t@t"}, {"GIT_COMMITTER_NAME", "t"}, {"GIT_COMMITTER_EMAIL", "t@t"}} {
		t.Setenv(kv[0], kv[1])
	}
	withApeVersion(t, "1.0.0")
}

// writeTree writes rel → body under root.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	}
}

// v1FrameworkRepo is a fake v1 release: two skills, the manifest, a lift
// entry and no pipelines or config template, tagged.
func v1FrameworkRepo(t *testing.T, tag, liftEntry string) string {
	t.Helper()
	fw := t.TempDir()
	writeTree(t, fw, map[string]string{
		framework.SubtreeSkills + "/apex-plan/SKILL.md":        "# apex-plan v1\n",
		framework.SubtreeSkills + "/apex-change/SKILL.md":      "# apex-change v1\n",
		framework.SubtreeApeCommands:                           v1ReleaseManifest,
		framework.SubtreeMigrations + "/v1.0.0_seq-01_lift.md": liftEntry,
	})
	gitInit(t, fw)
	gitCommitAll(t, fw, "v1 release")
	gitRun(t, fw, "tag", "-a", tag, "-m", tag)
	return fw
}

func emptyGitProject(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.MkdirAll(root, 0o755))
	v1Git(t, root, "init", "-q", "-b", "main")
	v1Git(t, root, "commit", "-q", "--allow-empty", "-m", "init")
	return root
}

// setup → init (the recommended order): setup accepts a release with no
// pipelines, needs no bootstrap flag headless, seeds no config, skips v0's
// surfaces, names itself in Generator:, and hints at init; init then runs.
func TestFrameworkInstallV1_SetupThenInit(t *testing.T) {
	v1Env(t)
	fw := v1FrameworkRepo(t, "v1.0.0-rc.9", v1LiftEntry("exit 0", "true"))
	root := emptyGitProject(t)

	out, err := runFramework(t, "setup", fw, root, "--version", "v1.0.0-rc.9", "--no-fetch")
	require.NoError(t, err, out)
	require.Contains(t, out, "next: `ape project init`", "setup of a v1 release into a project with no v1 layout hints at init")

	require.FileExists(t, filepath.Join(root, ".claude", "skills", "apex-plan", "SKILL.md"))
	require.FileExists(t, filepath.Join(root, "_apex", "migrations", "v1.0.0_seq-01_lift.md"))
	require.NoFileExists(t, filepath.Join(root, "_apex", "config.yaml"), "a v1 release seeds no config: init writes it")
	require.NoDirExists(t, filepath.Join(root, "_apex", "pipelines"), "no pipelines shipped, no empty folder made")
	require.FileExists(t, filepath.Join(root, ".aboard", ".gitignore"), ".aboard/ is still created (D126)")
	if b, err := os.ReadFile(filepath.Join(root, ".gitignore")); err == nil {
		require.NotContains(t, string(b), framework.GitignoreLockPattern, "no v0 lock-sidecar ignore line")
	}
	msg := v1Git(t, root, "log", "-1", "--format=%B")
	require.Contains(t, msg, "chore(framework): install APEX framework v1.0.0-rc.9")
	require.Contains(t, msg, "Generator: ape framework setup")
	require.Empty(t, v1Git(t, root, "status", "--porcelain"))

	_, stderr, err := v1Exec(t, newProjectCmd, "init", "--cwd", root)
	require.NoError(t, err, stderr)
	require.FileExists(t, filepath.Join(root, "_apex", "config.yaml"))
	require.Equal(t, "apex: project init", v1Git(t, root, "log", "-1", "--format=%s"))
	require.Contains(t, v1Git(t, root, "log", "--format=%s"), "chore(framework): install APEX framework v1.0.0-rc.9")
	require.FileExists(t, filepath.Join(root, ".claude", "skills", "apex-plan", "SKILL.md"), "init keeps the installed skills")
}

// init → setup: the config init wrote is kept, the bootstrap flags are
// said to be ignored, and there is no init hint.
func TestFrameworkInstallV1_InitThenSetup(t *testing.T) {
	v1Env(t)
	fw := v1FrameworkRepo(t, "v1.0.0-rc.9", v1LiftEntry("exit 0", "true"))
	root := emptyGitProject(t)
	_, stderr, err := v1Exec(t, newProjectCmd, "init", "--cwd", root)
	require.NoError(t, err, stderr)
	cfg, err := os.ReadFile(filepath.Join(root, "_apex", "config.yaml"))
	require.NoError(t, err)

	out, err := runFramework(t, "setup", fw, root, "--version", "v1.0.0-rc.9", "--no-fetch",
		"--project-name", "demo", "--extensions", "")
	require.NoError(t, err, out)
	require.Contains(t, out, "--project-name and --extensions are ignored for an APEX v1 release")
	require.NotContains(t, out, "next: `ape project init`", "the project already has its v1 layout")
	after, err := os.ReadFile(filepath.Join(root, "_apex", "config.yaml"))
	require.NoError(t, err)
	require.Equal(t, string(cfg), string(after), "setup never rewrites a v1 project's config")
	require.FileExists(t, filepath.Join(root, ".claude", "skills", "apex-change", "SKILL.md"))
	require.Contains(t, v1Git(t, root, "log", "-1", "--format=%B"), "Generator: ape framework setup")
}

// A release with neither the manifest nor pipelines is still refused, and
// the refusal names what is missing. Checked on the layout itself: the
// command exits the process on a ValidationError.
func TestFrameworkInstallV1_LayoutRefusalNamesWhatIsMissing(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{framework.SubtreeSkills + "/apex-plan/SKILL.md": "x"})
	_, err := framework.Setup(t.Context(), &framework.UpdateOptions{
		FrameworkRepo: dir, ProjectRoot: t.TempDir(), NoFetch: true, Force: true,
		Bootstrapper: framework.NoopBootstrapper{},
	})
	require.ErrorContains(t, err, framework.SubtreeApeCommands)
	require.ErrorContains(t, err, framework.SubtreePipelines)
}

// v0Installed is a committed project with an APEX v0 release installed by
// setup, plus two v0 migration entries: v0.16.0 recorded in the ledger and
// v0.24.0 still pending — the shape washmash's ledger has.
func v0Installed(t *testing.T) (fw, root string) {
	t.Helper()
	v1Env(t)
	fw = releasedFramework(t, "v0.31.0")
	root = committedProject(t)
	out, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap")
	require.NoError(t, err, out)
	writeTree(t, root, map[string]string{
		"_apex/migrations/v0.16.0_seq-01_retro.md": "---\nid: v0.16.0_seq-01\nversion: 0.16.0\nseq: 1\nkind: derivable\n" +
			"blocking: false\ncheck: \"exit 1\"\ncommand: \"touch v0a-ran\"\n---\nbody\n",
		"_apex/migrations/v0.24.0_seq-01_backfill.md": "---\nid: v0.24.0_seq-01\nversion: 0.24.0\nseq: 1\nkind: derivable\n" +
			"blocking: false\ncheck: \"exit 1\"\ncommand: \"touch v0b-ran\"\n---\nbody\n",
	})
	require.NoError(t, framework.AppendMigrations(root, []framework.AppliedMigration{
		{ID: "v0.16.0_seq-01", Version: "0.16.0", AppliedAt: "20260901120000"},
	}))
	gitRun(t, root, "add", "_apex")
	gitRun(t, root, "commit", "-q", "-m", "v0 migrations")
	return fw, root
}

// v1OnTopOf turns the fake v0 framework repo into a v1 release, v1.0.0: the
// manifest and a lift entry added, the v0 pipelines kept so the old ape's
// layout check passes and only the migration behaviour is under test.
func v1OnTopOf(t *testing.T, fw, liftEntry string) {
	t.Helper()
	const tag = "v1.0.0"
	writeTree(t, fw, map[string]string{
		framework.SubtreeApeCommands:                           v1ReleaseManifest,
		framework.SubtreeSkills + "/apex-foo/SKILL.md":         "# apex-foo v1\n",
		framework.SubtreeMigrations + "/v1.0.0_seq-01_lift.md": liftEntry,
	})
	gitCommitAll(t, fw, "v1")
	gitRun(t, fw, "tag", "-a", tag, "-m", tag)
}

// D162-3: an update to v1 removes the v0 entries in its commit, never runs
// one (the pending v0.24.0 included), and the ledger keeps its v0 history.
func TestFrameworkInstallV1_UpdateFromV0DropsV0Migrations(t *testing.T) {
	fw, root := v0Installed(t)
	v1OnTopOf(t, fw, v1LiftEntry("test -f lifted", "touch lifted"))

	out, err := runFramework(t, "update", fw, root, "--no-fetch", "--version", "v1.0.0")
	require.NoError(t, err, out)
	require.NoFileExists(t, filepath.Join(root, "v0a-ran"))
	require.NoFileExists(t, filepath.Join(root, "v0b-ran"), "a pending v0 entry is never run by a v1 install")
	require.FileExists(t, filepath.Join(root, "lifted"), "the v1 entry ran")
	for _, f := range []string{"v0.16.0_seq-01_retro.md", "v0.24.0_seq-01_backfill.md"} {
		require.NoFileExists(t, filepath.Join(root, "_apex", "migrations", f))
	}
	require.Empty(t, gitRun(t, root, "ls-files", "_apex/migrations/v0.*"), "the removal is in the update commit")
	require.Equal(t, "?? notes.txt", gitRun(t, root, "status", "--porcelain"))

	meta, err := framework.ReadMetadata(root)
	require.NoError(t, err)
	ids := make([]string, 0, len(meta.Migrations))
	for _, m := range meta.Migrations {
		ids = append(ids, m.ID)
	}
	require.Equal(t, []string{"v0.16.0_seq-01", "v1.0.0_seq-01"}, ids, "the ledger keeps its v0 history")
}

// The plan agrees with the run: --plan of a v1 release lists no v0 entry,
// even the installed ones the update is about to remove.
func TestFrameworkInstallV1_PlanListsNoV0Entry(t *testing.T) {
	fw, root := v0Installed(t)
	v1OnTopOf(t, fw, v1LiftEntry("test -f lifted", "touch lifted"))
	r, cwd := fw, root
	cmd := newFrameworkUpdateCmd(&r, &cwd)
	cmd.SetArgs([]string{"--plan", "--no-fetch", "--no-check", "--version", "v1.0.0"})
	var buf strings.Builder
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	require.NoError(t, cmd.Execute())
	require.Contains(t, buf.String(), "v1.0.0_seq-01")
	require.NotContains(t, buf.String(), "v0.24.0_seq-01")
	require.NotContains(t, buf.String(), "v0.16.0_seq-01")
}

// D162-4: a blocking migration that fails makes the update exit 1, commits
// nothing, and leaves the tree as the update found it, so a re-run starts
// from the release again.
func TestFrameworkInstallV1_FailedBlockingMigrationCommitsNothing(t *testing.T) {
	fw, root := v0Installed(t)
	v1OnTopOf(t, fw, v1LiftEntry("exit 1", "echo lift refused >&2; exit 3"))
	head := gitRun(t, root, "rev-parse", "HEAD")
	skill, err := os.ReadFile(filepath.Join(root, framework.ProjectSkillsDir, "apex-foo", "SKILL.md"))
	require.NoError(t, err)

	out, err := runFramework(t, "update", fw, root, "--no-fetch", "--version", "v1.0.0")
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err), out)
	require.Contains(t, out, "blocking migration v1.0.0_seq-01 failed (exit 3)")
	require.Contains(t, out, "the install is not committed")

	require.Equal(t, head, gitRun(t, root, "rev-parse", "HEAD"), "nothing is committed")
	require.Equal(t, "?? notes.txt", gitRun(t, root, "status", "--porcelain"), "the tree is as the update found it")
	after, err := os.ReadFile(filepath.Join(root, framework.ProjectSkillsDir, "apex-foo", "SKILL.md"))
	require.NoError(t, err)
	require.Equal(t, string(skill), string(after), "the v0 skills are back")
	meta, err := framework.ReadMetadata(root)
	require.NoError(t, err)
	require.Equal(t, "v0.31.0", meta.Framework.VersionTag)
	require.FileExists(t, filepath.Join(root, "_apex", "migrations", "v0.24.0_seq-01_backfill.md"))
	require.NoFileExists(t, filepath.Join(root, "_apex", "migrations", "v1.0.0_seq-01_lift.md"))
}

// D162-4, not attempted: a blocking entry after a failed one is refused
// the same way.
func TestFrameworkInstallV1_UnattemptedBlockingMigrationCommitsNothing(t *testing.T) {
	fw, root := v0Installed(t)
	writeTree(t, fw, map[string]string{
		framework.SubtreeMigrations + "/v1.0.0_seq-00_first.md": "---\nid: v1.0.0_seq-00\nversion: 1.0.0\nseq: 0\n" +
			"kind: derivable\nblocking: false\ncheck: \"exit 1\"\ncommand: \"exit 4\"\n---\nbody\n",
	})
	v1OnTopOf(t, fw, v1LiftEntry("test -f lifted", "touch lifted"))
	head := gitRun(t, root, "rev-parse", "HEAD")

	out, err := runFramework(t, "update", fw, root, "--no-fetch", "--version", "v1.0.0")
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err), out)
	require.Contains(t, out, "blocking migration v1.0.0_seq-01 not attempted")
	require.Equal(t, head, gitRun(t, root, "rev-parse", "HEAD"))
	require.Equal(t, "?? notes.txt", gitRun(t, root, "status", "--porcelain"))
	require.NoFileExists(t, filepath.Join(root, "lifted"))
}

// E-R7-1: an apex-v0 → v1 first update in one run. seq-07's check cannot
// tell on the v0 layout (exit 2, as `ape project operator-interface
// --check` does there) and answers "not applied" once the lift has run; the
// update asks it again after the lift, applies both and exits 0. On the
// old runner the up-front verdict stood and the update exited 1.
func TestFrameworkInstallV1_V0FirstUpdateRejudgesAfterTheLift(t *testing.T) {
	fw, root := v0Installed(t)
	writeTree(t, fw, map[string]string{
		framework.SubtreeMigrations + "/v1.0.0_seq-07_operator-interface.md": "---\nid: v1.0.0_seq-07\nversion: 1.0.0\n" +
			"seq: 7\nkind: derivable\nblocking: true\nafter: [v1.0.0_seq-01]\n" +
			"check: \"test -f lifted || exit 2; test -f oi-done\"\ncommand: \"touch oi-done\"\n---\nbody\n",
	})
	v1OnTopOf(t, fw, v1LiftEntry("test -f lifted", "touch lifted"))

	out, err := runFramework(t, "update", fw, root, "--no-fetch", "--version", "v1.0.0")
	require.NoError(t, err, out)
	require.FileExists(t, filepath.Join(root, "lifted"))
	require.FileExists(t, filepath.Join(root, "oi-done"), "seq-07 ran in the same update, after the lift")
	require.Contains(t, out, "migration v1.0.0_seq-07: judged again (cannot-tell → pending, after v1.0.0_seq-01 applied)")
	require.Contains(t, out, "asks the check again once an earlier entry in its run applies")

	meta, err := framework.ReadMetadata(root)
	require.NoError(t, err)
	ids := make([]string, 0, len(meta.Migrations))
	for _, m := range meta.Migrations {
		ids = append(ids, m.ID)
	}
	require.Equal(t, []string{"v0.16.0_seq-01", "v1.0.0_seq-01", "v1.0.0_seq-07"}, ids)
	require.Equal(t, "v1.0.0", meta.Framework.VersionTag)
	require.Contains(t, gitRun(t, root, "log", "-1", "--format=%s"), "update APEX framework to v1.0.0")
}

// A v0 release installs exactly as before: config seeded from its template,
// pipelines, the lock-sidecar ignore line, no hint. A regression guard for
// the v1 branches, so it passes on the old code by design.
func TestFrameworkInstallV1_V0ReleaseInstallsAsBefore(t *testing.T) {
	v1Env(t)
	fw := releasedFramework(t, "v0.31.0")
	root := emptyGitProject(t)

	out, err := runFramework(t, "setup", fw, root, "--no-fetch", "--project-name", "demo", "--extensions", "")
	require.NoError(t, err, out)
	require.NotContains(t, out, "next: `ape project init`")
	require.NotContains(t, out, "ignored for an APEX v1 release")
	cfg, err := os.ReadFile(filepath.Join(root, "_apex", "config.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(cfg), "demo")
	require.FileExists(t, filepath.Join(root, "_apex", "pipelines", "design.yaml"))
	ignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	require.NoError(t, err)
	require.Contains(t, string(ignore), framework.GitignoreLockPattern)
}

// D203, Q113: with .claude/ ignored, an APEX v1 install committed none of
// the skills it installed. setup and update put the .claude/ shape in the
// root .gitignore in their commit, so the skills are committed with it; a
// file of the person's the rewrite un-ignored is left out (and listed).
// setup runs before init (D162), and init then finds the shape in place.
func TestFrameworkInstallV1_IgnoredClaudeCommitsSkills(t *testing.T) {
	t.Run("setup before init", func(t *testing.T) {
		v1Env(t)
		fw := v1FrameworkRepo(t, "v1.0.0-rc.9", v1LiftEntry("exit 0", "true"))
		root := emptyGitProject(t)
		writeTree(t, root, map[string]string{
			".gitignore": ".claude/\n", ".claude/skills/mine/SKILL.md": "# mine\n", ".claude/settings.local.json": "{}\n",
		})
		gitRun(t, root, "add", ".gitignore")
		gitRun(t, root, "commit", "-q", "-m", "ignore .claude")

		out, err := runFramework(t, "setup", fw, root, "--version", "v1.0.0-rc.9", "--no-fetch")
		require.NoError(t, err, out)
		tracked := gitRun(t, root, "ls-files", ".claude", ".gitignore")
		require.Equal(t, ".claude/skills/apex-change/SKILL.md\n.claude/skills/apex-plan/SKILL.md\n.gitignore", tracked,
			"the install's skills and the shape are committed; the person's skill and settings.local.json are not")
		require.Contains(t, gitRun(t, root, "show", "--format=", "HEAD", "--", ".gitignore"), "+!.claude/skills/")
		require.Equal(t, "?? .claude/skills/mine/SKILL.md", gitRun(t, root, "status", "--porcelain", "-uall"))

		_, stderr, err := v1Exec(t, newProjectCmd, "init", "--cwd", root)
		require.NoError(t, err, stderr)
		require.Contains(t, gitRun(t, root, "ls-files", ".claude"), ".claude/settings.json")
		require.Equal(t, "?? .claude/skills/mine/SKILL.md", gitRun(t, root, "status", "--porcelain", "-uall"))
	})
	t.Run("update", func(t *testing.T) {
		fw, root := v0Installed(t)
		gitRun(t, root, "rm", "-r", "-q", "--cached", ".claude")
		ignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
		require.NoError(t, err)
		writeTree(t, root, map[string]string{".gitignore": string(ignore) + ".claude\n"})
		gitRun(t, root, "add", ".gitignore")
		gitRun(t, root, "commit", "-q", "-m", "ignore .claude")
		v1OnTopOf(t, fw, v1LiftEntry("test -f lifted", "touch lifted"))

		out, err := runFramework(t, "update", fw, root, "--no-fetch", "--version", "v1.0.0")
		require.NoError(t, err, out)
		require.Contains(t, gitRun(t, root, "ls-files", ".claude/skills"), ".claude/skills/apex-foo/SKILL.md")
		require.Contains(t, gitRun(t, root, "show", "--format=", "HEAD", "--", ".gitignore"), "+!.claude/skills/")
		require.Equal(t, "?? notes.txt", gitRun(t, root, "status", "--porcelain"))
	})
}
