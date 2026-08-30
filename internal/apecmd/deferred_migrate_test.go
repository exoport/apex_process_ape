package apecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/deferred"
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

const legacyLedgerFixture = "# Deferred Work\n\n" +
	"## Deferred from: story review of 54-1 (2026-08-21)\n\n" +
	"- [ ] [Defer] First thing [pkg/a.go:1] — defer: owner=platform ; trigger: story 54-2 lands\n" +
	"- [Defer] Second thing [pkg/b.go:2] — defer: owner=data\n\n" +
	"A pasted paragraph with no marker at all, which must survive.\n"

// writeLegacyLedger drops the legacy file where the config resolves it.
func writeLegacyLedger(t *testing.T, root, body string) string {
	t.Helper()
	dir := filepath.Join(root, "development", "implementation")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "deferred-work.md")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func TestDeferredMigrate_HumanOutputAndGitAddHint(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeLegacyLedger(t, root, legacyLedgerFixture)

	out := runCmd(t, newDeferredMigrateCmd())
	require.Contains(t, out, "migration:")
	require.Contains(t, out, "every ledger line accounted for ... OK")
	require.Contains(t, out, "-> stub")

	// Nothing is committed, so the output has to carry what a commit
	// message would have.
	require.Contains(t, out, "nothing committed")
	require.Contains(t, out, "git add")
	require.Contains(t, out, filepath.Join("development", "deferred"))
}

func TestDeferredMigrate_JSONPayload(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeLegacyLedger(t, root, legacyLedgerFixture)

	out := runCmd(t, newDeferredMigrateCmd(), "--output-format", "json")
	var res deferred.MigrateResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.Equal(t, res.RecordsIn, res.RecordsOut)
	require.Positive(t, res.RecordsIn)
	require.True(t, res.StubWritten)
	require.NotEmpty(t, res.Paths)
}

func TestDeferredMigrate_DryRunWritesNothing(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	legacy := writeLegacyLedger(t, root, legacyLedgerFixture)
	before, err := os.ReadFile(legacy)
	require.NoError(t, err)

	out := runCmd(t, newDeferredMigrateCmd(), "--dry-run")
	require.Contains(t, out, "dry run")
	require.Contains(t, out, "Nothing written")
	require.NotContains(t, out, "git add", "there is nothing to add")

	after, err := os.ReadFile(legacy)
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = os.Stat(filepath.Join(root, "development", "deferred"))
	require.Error(t, err)
}

func TestDeferredMigrate_Idempotent(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeLegacyLedger(t, root, legacyLedgerFixture)

	runCmd(t, newDeferredMigrateCmd())
	out := runCmd(t, newDeferredMigrateCmd())
	require.Contains(t, out, "already migrated")
}

func TestDeferredMigrate_MissingLedgerIsAUsageError(t *testing.T) {
	newTestProject(t, realProjectConfig)
	cmd := newDeferredMigrateCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(nil)
	err := cmd.Execute()
	require.Error(t, err)
	code, _ := ExitCode(err)
	require.Equal(t, ExitUsage, code)
}

// TestDeferredMigrate_DirtyMigrationPathsRefuse is the path-scoped clean
// gate: with no commit to isolate ape's work, `git status` is the only
// separation between it and the operator's WIP, so those paths must start
// clean. Unrelated WIP elsewhere is explicitly fine.
func TestDeferredMigrate_DirtyMigrationPathsRefuse(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := newTestProject(t, realProjectConfig)
	git := func(args ...string) {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		require.NoError(t, cmd.Run(), "git %v", args)
	}
	git("init", "-q")
	writeLegacyLedger(t, root, legacyLedgerFixture)
	git("add", ".")
	git("commit", "-qm", "base")

	// Unrelated WIP: must NOT block the migration.
	require.NoError(t, os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("wip\n"), 0o644))
	out := runCmd(t, newDeferredMigrateCmd(), "--dry-run")
	require.Contains(t, out, "dry run", "unrelated WIP is nobody's business")

	// Now dirty a migration path itself.
	legacy := filepath.Join(root, "development", "implementation", "deferred-work.md")
	require.NoError(t, os.WriteFile(legacy, []byte(legacyLedgerFixture+"\n- [Defer] uncommitted\n"), 0o644))

	cmd := newDeferredMigrateCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(nil)
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "uncommitted changes")
}

func TestDeferredMigrate_RecoverDeletedFlagExists(t *testing.T) {
	cmd := newDeferredMigrateCmd()
	require.NotNil(t, cmd.Flags().Lookup("recover-deleted"))
	require.NotNil(t, cmd.Flags().Lookup("dry-run"))
	require.Nil(t, cmd.Flags().Lookup("no-commit"),
		"there is no commit to suppress, and offering the flag would imply there was")
}

// --- repair ---

// installRepairSkill puts a repair skill in the project's own skills tree.
// Every repair test past the nothing-to-repair short-circuit needs one,
// because a name that resolves to neither skill is refused up front.
func installRepairSkill(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, framework.ProjectSkillsDir, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# "+name+"\n"), 0o644))
}

func TestDeferredRepair_NothingToRepair(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeLegacyLedger(t, root, "## Deferred from: story review of 1-1 (2026-01-01)\n\n"+
		"- [Defer] Well-formed [a.go:1] — defer: owner=x\n")
	runCmd(t, newDeferredMigrateCmd())

	out := runCmd(t, newDeferredRepairCmd())
	require.Contains(t, out, "nothing to repair")
}

func TestDeferredRepair_DryRunSpawnsNothing(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeLegacyLedger(t, root, legacyLedgerFixture)
	runCmd(t, newDeferredMigrateCmd())
	installRepairSkill(t, root, repairSkill)

	out := runCmd(t, newDeferredRepairCmd(), "--dry-run")
	require.Contains(t, out, "free-form record(s)")
	require.Contains(t, out, repairSkill)
	require.Contains(t, out, repairModel)
	require.Contains(t, out, "no session spawned")
}

// TestDeferredRepair_RefusesWithoutATTY: it spends real money, and should
// not do that from a script that did not ask — the same refusal
// pickBootstrapper already makes rather than seeding a config silently.
func TestDeferredRepair_RefusesWithoutATTY(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeLegacyLedger(t, root, legacyLedgerFixture)
	runCmd(t, newDeferredMigrateCmd())
	installRepairSkill(t, root, repairSkill)

	cmd := newDeferredRepairCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(nil)
	err := cmd.Execute()
	require.Error(t, err, "a test process has no TTY, so this must refuse")
	require.Contains(t, err.Error(), "without a TTY")
	require.Contains(t, err.Error(), repairModel)
}

// TestRepairTaskOptions is the dispatch-shape assertion: the invocation is
// built and checked without spawning claude, the way task and pipeline
// invocation shapes are already tested here.
func TestRepairTaskOptions(t *testing.T) {
	opts := repairTaskOptions("/srv/project", repairPlan{Skill: repairSkill, Model: repairModel})
	require.Equal(t, repairSkill, opts.skill)
	require.Contains(t, opts.model, "opus", "resolved through the shared model arg")
	require.Equal(t, "/srv/project", opts.projectRoot)
	require.Contains(t, opts.args, "--autonomous")
	require.True(t, opts.skillNoCommit,
		"the judgment phase must not commit — the operator groups the commits")
}

// TestAssertNoRecordsLost is the ape-side post-condition on the prompt's
// "discard never deletes" rule. A prompt is not an enforcement mechanism.
func TestAssertNoRecordsLost(t *testing.T) {
	store := deferred.New(filepath.Join(t.TempDir(), "deferred"))
	_, err := store.Ingest([]byte("- [Defer] A [a.go:1]\n- [Defer] B [b.go:2]\n"),
		deferred.IngestOptions{Date: "2026-08-22"})
	require.NoError(t, err)
	before, err := store.Load(deferred.LoadOptions{IncludeClosed: true})
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, assertNoRecordsLost(&buf, store, before))
	require.Contains(t, buf.String(), "none lost")

	// Now simulate the phase deleting one.
	require.NoError(t, os.Remove(before.Records[0].Path))
	err = assertNoRecordsLost(&bytes.Buffer{}, store, before)
	require.Error(t, err)
	require.Contains(t, err.Error(), "LOST 1 record")
	require.Contains(t, err.Error(), before.Records[0].ID, "the missing record is named")
	require.Contains(t, err.Error(), "restore them before committing")
}

func TestEmitGitAddHint_CollapsesToDirectories(t *testing.T) {
	var buf bytes.Buffer
	emitGitAddHint(&buf, "/srv/p", []string{
		"/srv/p/development/deferred/DW-1_a.md",
		"/srv/p/development/deferred/DW-2_b.md",
		"/srv/p/development/implementation/deferred-work.md",
	})
	out := buf.String()
	require.Contains(t, out, "nothing committed")
	require.Equal(t, 1, strings.Count(out, "git add"))
	require.Contains(t, out, filepath.Join("development", "deferred"))
	require.Contains(t, out, filepath.Join("development", "implementation"))
	require.NotContains(t, out, "DW-1_a.md", "a git add line listing every record file is not usable")
}

func TestRelTo(t *testing.T) {
	sep := string(filepath.Separator)
	root := sep + "root"
	require.Equal(t, filepath.Join("a", "b"), relTo(root, filepath.Join(root, "a", "b")))

	outside := sep + filepath.Join("elsewhere", "x")
	require.Equal(t, outside, relTo(root, outside),
		"a path outside the root stays absolute rather than becoming ../..")
}

func TestMigrationPathsAreDisjointFromTheFrameworkInstall(t *testing.T) {
	// The install writes .claude/skills/apex-*, _apex/*, CLAUDE.md; the
	// migration writes under development/. Disjoint sets are what make the
	// two order-independent, so this asserts the write sets do not overlap.
	migration := migrationPaths("/p/development/deferred", "/p/development/implementation/deferred-work.md")
	root := string(filepath.Separator) + "p"
	install := []string{
		filepath.Join(root, ".claude", "skills"),
		filepath.Join(root, "_apex", "pipelines"),
		filepath.Join(root, "_apex", "framework.yaml"),
		filepath.Join(root, "_apex", "apex-operating-rules.md"),
		filepath.Join(root, "CLAUDE.md"),
	}
	for _, m := range migration {
		for _, i := range install {
			require.False(t, strings.HasPrefix(m, i), "%s overlaps the install path %s", m, i)
			require.False(t, strings.HasPrefix(i, m), "%s overlaps the migration path %s", i, m)
		}
	}
}

// TestResolveRepairSkill_AcceptsBothNames is what stops the framework's
// rename from being a lockstep release. A hard switch would break a new ape
// against an older framework AND an older ape against the renamed one, so
// the two repos would have to ship in the same hour.
func TestResolveRepairSkill_AcceptsBothNames(t *testing.T) {
	t.Run("current name", func(t *testing.T) {
		root := t.TempDir()
		installRepairSkill(t, root, repairSkill)
		name, found := resolveRepairSkill(root)
		require.True(t, found)
		require.Equal(t, repairSkill, name)
	})

	t.Run("legacy name on an older framework", func(t *testing.T) {
		root := t.TempDir()
		installRepairSkill(t, root, repairSkillLegacy)
		name, found := resolveRepairSkill(root)
		require.True(t, found)
		require.Equal(t, repairSkillLegacy, name)
	})

	t.Run("both installed prefers the current one", func(t *testing.T) {
		root := t.TempDir()
		installRepairSkill(t, root, repairSkillLegacy)
		installRepairSkill(t, root, repairSkill)
		name, found := resolveRepairSkill(root)
		require.True(t, found)
		require.Equal(t, repairSkill, name,
			"a framework mid-rename may ship both; the current name wins")
	})
}

// TestDeferredRepair_RefusesWhenNoSkillIsInstalled covers the gap the
// runner's own preflight leaves.
//
// A real dispatch is already safe: runTask goes through pipeline.Run, which
// calls PreflightSkills and exits 2 without reaching claude. But --dry-run
// never reaches the runner, so without this check it would print a plan
// naming a skill that does not exist, report "no session spawned", and read
// as a clean dry run — with the real run failing later. That is the case
// asserted here, and it is asserted through --dry-run for exactly that
// reason.
func TestDeferredRepair_RefusesWhenNoSkillIsInstalled(t *testing.T) {
	// ResolveSkill falls back to ~/.claude/skills, so a developer machine
	// with the real framework installed would resolve the name and this
	// would not test what it claims. Point HOME somewhere empty.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	root := newTestProject(t, realProjectConfig)
	writeLegacyLedger(t, root, legacyLedgerFixture)
	runCmd(t, newDeferredMigrateCmd())

	cmd := newDeferredRepairCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--dry-run"})
	err := cmd.Execute()

	require.Error(t, err, "--dry-run checks this too: finding out whether it would work is the point")
	require.Contains(t, err.Error(), repairSkill)
	require.Contains(t, err.Error(), repairSkillLegacy, "both names are named, so the operator can see which is expected")
	require.Contains(t, err.Error(), "ape framework update")
}

// TestDeferredMigrate_ReportsTheRelocation covers the reporting gap the
// first field migration hit: 456 KB of cited text left the folder a
// repo-wide anchor gate was scoped to, that gate's count fell by 44, and
// the drop read as a regression caused by the migration rather than as the
// migration itself. Nothing was wrong; nothing said so either.
func TestDeferredMigrate_ReportsTheRelocation(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeLegacyLedger(t, root, legacyLedgerFixture)

	out := runCmd(t, newDeferredMigrateCmd())
	require.Contains(t, out, "bytes of cited text moved out of")
	require.Contains(t, out, "not a regression")
	require.Contains(t, out, filepath.Join("development", "implementation"))
}

// TestDeferredMigrate_AlreadyDoneSaysHowToReMigrate: a wrong section date
// is hashed into the record id, so that class of defect can only be fixed
// by re-migrating. "already migrated" on its own is a dead end when
// re-migrating is exactly what the operator is trying to do.
func TestDeferredMigrate_AlreadyDoneSaysHowToReMigrate(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeLegacyLedger(t, root, legacyLedgerFixture)

	require.Contains(t, runCmd(t, newDeferredMigrateCmd()), "migration:")

	out := runCmd(t, newDeferredMigrateCmd())
	require.Contains(t, out, "already migrated")
	require.Contains(t, out, "to re-migrate")
	require.Contains(t, out, "restore")
	require.Contains(t, out, "remove")
}

// TestDeferredMigrate_RecoveryIsOnByDefault: the choice is not symmetric.
// Recovery only writes tombstones into closed/, and skipping it forfeits
// the history permanently because migrate short-circuits afterwards. The
// automatic path (`ape framework update`) was the one taking the lossy
// branch, so the default moved.
func TestDeferredMigrate_RecoveryIsOnByDefault(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeLegacyLedger(t, root, legacyLedgerFixture)

	out := runCmd(t, newDeferredMigrateCmd())
	require.Contains(t, out, "recovered from git history",
		"recovery must run without being asked, and report even at zero")
	require.NotContains(t, out, "SKIPPED")
}

// TestDeferredMigrate_OptOutIsStatedAsOneWay: --no-recover-deleted is a
// choice that cannot be revisited through this command.
func TestDeferredMigrate_OptOutIsStatedAsOneWay(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeLegacyLedger(t, root, legacyLedgerFixture)

	out := runCmd(t, newDeferredMigrateCmd(), "--no-recover-deleted")
	require.Contains(t, out, "history recovery SKIPPED")
	require.Contains(t, out, "one-way")
}

// TestDeferredMigrate_AlreadyDoneNamesTheIgnoredFlag: silently doing
// nothing is indistinguishable from running and finding nothing, and only
// one of those means the history is still recoverable.
func TestDeferredMigrate_AlreadyDoneNamesTheIgnoredFlag(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeLegacyLedger(t, root, legacyLedgerFixture)
	require.Contains(t, runCmd(t, newDeferredMigrateCmd()), "migration:")

	out := runCmd(t, newDeferredMigrateCmd(), "--recover-deleted")
	require.Contains(t, out, "already migrated")
	require.Contains(t, out, "NOT run")
	require.Contains(t, out, "ape deferred recover",
		"the refusal must name the command that still works")

	// And without the flag, no misleading note about it.
	plain := runCmd(t, newDeferredMigrateCmd(), "--no-recover-deleted")
	require.Contains(t, plain, "already migrated")
	require.NotContains(t, plain, "NOT run")
}

// TestDeferredDiscard_WritesTheThirdStatus covers the command whose
// absence caused a judgment phase to reach for `close` instead.
func TestDeferredDiscard_WritesTheThirdStatus(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeLegacyLedger(t, root, legacyLedgerFixture)
	require.Contains(t, runCmd(t, newDeferredMigrateCmd()), "migration:")

	store := deferred.New(filepath.Join(root, "development", "deferred"))
	loaded, err := store.Load(deferred.LoadOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, loaded.Records)
	id := loaded.Records[0].ID

	out := runCmd(t, newDeferredDiscardCmd(), id,
		"--reason", "superseded by the 91-2 rewrite",
		"--evidence", "pkg/a.go:12 is gone at HEAD")
	require.Contains(t, out, "discarded "+id)
	require.Contains(t, out, "superseded by the 91-2 rewrite")

	// Visible under the status the flag help now advertises.
	listed := runCmd(t, newDeferredListCmd(), "--status", "discarded")
	require.Contains(t, listed, id)
}

func TestDeferredDiscard_RefusesWithoutAReason(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeLegacyLedger(t, root, legacyLedgerFixture)
	require.Contains(t, runCmd(t, newDeferredMigrateCmd()), "migration:")

	cmd := newDeferredDiscardCmd()
	cmd.SetArgs([]string{"DW-anything", "--cwd", root})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	require.Error(t, cmd.Execute())
}

// TestDeferredList_AdvertisesAllThreeStatuses: the flag help said
// open|closed|all while the store modelled three, and a caller reading the
// short version concluded discard was unsupported.
func TestDeferredList_AdvertisesAllThreeStatuses(t *testing.T) {
	cmd := newDeferredListCmd()
	require.Contains(t, cmd.Flags().Lookup("status").Usage, "discarded")
	require.Contains(t, cmd.Long, "discarded")
}
