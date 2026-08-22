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
	require.Contains(t, out, "bodies byte-identical ... OK")
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
	require.NotContains(t, out, "DW-1_a.md", "227 paths in a git add line is not usable")
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
