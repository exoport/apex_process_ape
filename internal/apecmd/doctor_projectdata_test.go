package apecmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/sprint"
	"github.com/stretchr/testify/require"
)

// projectDataEnv builds a doctorEnv pointed at a project root.
func projectDataEnv(root string) doctorEnv {
	return doctorEnv{ProjectRoot: root, OS: "linux", Arch: "amd64", OSRelease: map[string]string{}}
}

// projectFor writes a project WITHOUT chdir-ing, since the doctor checks
// take their root from doctorEnv.
func projectFor(t *testing.T, cfg string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, apexcfg.DirName), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, apexcfg.DirName, apexcfg.BaseFile), []byte(cfg), 0o644))
	return root
}

func TestCheckConfigResolved(t *testing.T) {
	ctx := context.Background()

	t.Run("outside a project", func(t *testing.T) {
		res := checkConfigResolved(ctx, projectDataEnv(t.TempDir()))
		require.Equal(t, StatusInfo, res.Status)
	})

	t.Run("resolved", func(t *testing.T) {
		root := projectFor(t, realProjectConfig)
		res := checkConfigResolved(ctx, projectDataEnv(root))
		require.Equal(t, StatusOK, res.Status)
		require.Contains(t, res.Message, "axon")
		require.Contains(t, res.Message, "no local overlay")
	})

	t.Run("overlay reported", func(t *testing.T) {
		root := projectFor(t, realProjectConfig)
		require.NoError(t, os.WriteFile(
			filepath.Join(root, apexcfg.DirName, apexcfg.LocalFile),
			[]byte("user_name: Diego\n"), 0o644))
		res := checkConfigResolved(ctx, projectDataEnv(root))
		require.Equal(t, StatusOK, res.Status)
		require.Contains(t, res.Message, "user_name")
	})

	// A malformed override is the failure most likely to send every other
	// check at the wrong tree, so this one FAILs rather than warns.
	t.Run("malformed local overlay fails", func(t *testing.T) {
		root := projectFor(t, realProjectConfig)
		require.NoError(t, os.WriteFile(
			filepath.Join(root, apexcfg.DirName, apexcfg.LocalFile),
			[]byte("user_name: [unterminated\n"), 0o644))
		res := checkConfigResolved(ctx, projectDataEnv(root))
		require.Equal(t, StatusFail, res.Status)
		require.Contains(t, res.Message, apexcfg.LocalFile)
		require.Contains(t, res.Remediation, "first act")
	})
}

func TestCheckMemorySize_States(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		size   int
		status DoctorStatus
	}{
		{"absent", -1, StatusOK},
		{"under budget", 1 << 10, StatusOK},
		{"over soft", 100 << 10, StatusWarn},
		{"over hard", 300 << 10, StatusFail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := projectFor(t, realProjectConfig)
			if tc.size >= 0 {
				dir := filepath.Join(root, "development")
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "team-memory.md"),
					[]byte(strings.Repeat("x", tc.size)), 0o644))
			}
			res := checkMemorySize(ctx, projectDataEnv(root))
			require.Equal(t, tc.status, res.Status, res.Message)
		})
	}
}

// TestCheckMemorySize_IsRequired is the wiring assertion that makes the
// hard ceiling surfaceable at all: runDoctor downgrades a non-required
// FAIL to WARN, so a non-required memory.size could never report it.
func TestCheckMemorySize_IsRequired(t *testing.T) {
	var found bool
	for _, c := range allChecks {
		if c.Name == "memory.size" {
			found = true
			require.True(t, c.Required,
				"memory.size must be Required or runDoctor downgrades its FAIL to WARN")
		}
	}
	require.True(t, found, "memory.size is registered")

	for _, c := range allChecks {
		if c.Name == "config.resolved" {
			require.True(t, c.Required, "nothing else can see the project without it")
		}
	}
}

// TestProjectDataChecks_DegradeOutsideAProject: absence of a project is
// not a finding.
func TestProjectDataChecks_DegradeOutsideAProject(t *testing.T) {
	ctx := context.Background()
	env := projectDataEnv(t.TempDir())
	checks := map[string]func(context.Context, doctorEnv) CheckResult{
		"config.resolved":     checkConfigResolved,
		"registry.drift":      checkRegistryDrift,
		"story.frontmatter":   checkStoryFrontmatter,
		"sprint.divergence":   checkSprintDivergence,
		"sprint.lock_ignored": checkSprintLockIgnored,
		"memory.size":         checkMemorySize,
		"migration.pending":   checkMigrationPending,
	}
	// The table has to hold every project-data check, or a new one degrades
	// however it happens to and nobody notices until it fires on a host.
	require.Len(t, checks, countProjectDataChecks(t),
		"a project-data check was added to the registry and not to this table")
	for name, fn := range checks {
		t.Run(name, func(t *testing.T) {
			res := fn(ctx, env)
			require.Equal(t, StatusInfo, res.Status, "%s must degrade to INFO: %s", name, res.Message)
		})
	}
}

// countProjectDataChecks counts the registry entries this family owns, by
// the name prefixes it uses.
func countProjectDataChecks(t *testing.T) int {
	t.Helper()
	n := 0
	for _, c := range allChecks {
		for _, prefix := range []string{"config.", "registry.", "story.", "sprint.", "memory.", "migration."} {
			if strings.HasPrefix(c.Name, prefix) {
				n++
				break
			}
		}
	}
	return n
}

func TestCheckRegistryDrift(t *testing.T) {
	ctx := context.Background()
	root := projectFor(t, allExtensionsConfig)
	seedADRCorpus(t, root, 4)

	res := checkRegistryDrift(ctx, projectDataEnv(root))
	require.Equal(t, StatusOK, res.Status, res.Message)
	require.Contains(t, res.Message, "no drift")

	// Now orphan one.
	seedADRCorpus(t, root, 4, 3)
	res = checkRegistryDrift(ctx, projectDataEnv(root))
	require.Equal(t, StatusWarn, res.Status, "drift is a warn, not a fail")
	require.Contains(t, res.Message, "orphan_record")
	require.Contains(t, res.FixCommand, "ape registry verify")
}

func TestCheckStoryFrontmatterAndSprintDivergence(t *testing.T) {
	ctx := context.Background()
	root := projectFor(t, realProjectConfig)
	dir := filepath.Join(root, "development", "implementation")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1-1_x.md"),
		[]byte("---\nstory_id: 1-1\n---\n\nx\n"), 0o644))

	res := checkStoryFrontmatter(ctx, projectDataEnv(root))
	require.Equal(t, StatusWarn, res.Status, "epic/status/output_document are missing")
	require.Contains(t, res.Message, "finding")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "sprint-status.yaml"),
		[]byte("development_status:\n  9-9: done\n"), 0o644))
	res = checkSprintDivergence(ctx, projectDataEnv(root))
	require.Equal(t, StatusWarn, res.Status)
	require.Contains(t, res.Message, "divergence")
}

// TestCheckSprintLockIgnored walks every state, because the one that
// matters — the sidecar already committed — is the one a project reaches by
// accident and never notices. ape's own repository reached it while this
// check was being written: `ape sprint reconcile` ran against a test
// fixture, left the lock behind, and `git add` swept it into a commit.
func TestCheckSprintLockIgnored(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	ctx := context.Background()
	root := projectFor(t, realProjectConfig)
	dir := filepath.Join(root, "development", "implementation")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	tracker := filepath.Join(dir, "sprint-status.yaml")
	lock := sprint.LockPath(tracker)
	rel := filepath.Join("development", "implementation", "sprint-status.yaml.lock")

	// No tracker: reconcile has nothing to lock, so a warning here would be
	// about a file that cannot yet exist.
	res := checkSprintLockIgnored(ctx, projectDataEnv(root))
	require.Equal(t, StatusOK, res.Status, res.Message)
	require.Contains(t, res.Message, "nothing to lock")

	require.NoError(t, os.WriteFile(tracker,
		[]byte("development_status:\n  epic-1: backlog\n  1-1_x: done\n"), 0o644))

	// A tracker but no repository: nothing to ignore into, and nothing that
	// can accidentally commit it either.
	res = checkSprintLockIgnored(ctx, projectDataEnv(root))
	require.Equal(t, StatusInfo, res.Status, res.Message)
	require.Contains(t, res.Message, "not a git repository")

	gitInit(t, root)
	res = checkSprintLockIgnored(ctx, projectDataEnv(root))
	require.Equal(t, StatusWarn, res.Status)
	require.Contains(t, res.Message, "not ignored")
	require.Contains(t, res.Remediation, "never unlinks it",
		"the remediation says WHY the file keeps coming back")

	// Reconcile really does leave it behind — the premise of the check.
	_, err := sprint.Reconcile(tracker, sprint.ReconcileOptions{Epic: 1, Timestamp: "20260401090000"})
	require.NoError(t, err)
	require.FileExists(t, lock, "the lock sidecar outlives the run that took it")

	// Swept into a commit by a routine `git add -A`.
	gitCommitAll(t, root, "everything")
	res = checkSprintLockIgnored(ctx, projectDataEnv(root))
	require.Equal(t, StatusWarn, res.Status)
	require.Contains(t, res.Message, "COMMITTED",
		"already in history is a worse state than merely unignored, and says so")
	require.Contains(t, res.FixCommand, "git rm --cached",
		"ignoring it now changes nothing until it is also untracked")

	// Untracked and ignored: clean.
	untrack := exec.CommandContext(ctx, "git", "rm", "--cached", "-q", "--", rel)
	untrack.Dir = root
	require.NoError(t, untrack.Run())
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"),
		[]byte("*"+sprint.LockSuffix+"\n"), 0o644))
	res = checkSprintLockIgnored(ctx, projectDataEnv(root))
	require.Equal(t, StatusOK, res.Status, res.Message)
	require.Contains(t, res.Message, "is ignored")
}

// TestGitIgnores_DistinguishesNotIgnoredFromNoAnswer: git says "not
// ignored" with exit 1 and "not a repository" with 128. Collapsing the two
// would report a finding about the environment as one about the project.
func TestGitIgnores_DistinguishesNotIgnoredFromNoAnswer(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	ctx := context.Background()
	root := t.TempDir()
	require.Equal(t, ignoreNotARepo, gitIgnores(ctx, root, filepath.Join(root, "x.lock")))

	gitInit(t, root)
	require.Equal(t, ignoreNo, gitIgnores(ctx, root, filepath.Join(root, "x.lock")))

	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.lock\n"), 0o644))
	require.Equal(t, ignoreYes, gitIgnores(ctx, root, filepath.Join(root, "x.lock")))

	// A nested .gitignore and a later negation are why this asks git rather
	// than matching patterns by hand.
	nested := filepath.Join(root, "sub")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nested, ".gitignore"), []byte("!keep.lock\n"), 0o644))
	require.Equal(t, ignoreNo, gitIgnores(ctx, root, filepath.Join(nested, "keep.lock")))
}

func TestCheckMigrationPending(t *testing.T) {
	ctx := context.Background()
	root := projectFor(t, realProjectConfig)

	// No legacy ledger: nothing pending.
	res := checkMigrationPending(ctx, projectDataEnv(root))
	require.Equal(t, StatusOK, res.Status, res.Message)

	// A legacy ledger with no store: pending.
	writeLegacyLedger(t, root, legacyLedgerFixture)
	res = checkMigrationPending(ctx, projectDataEnv(root))
	require.Equal(t, StatusWarn, res.Status)
	require.Contains(t, res.Message, "deferred")
	require.Contains(t, res.FixCommand, "dry-run", "the fix command looks before it writes")
}

// --- D10: framework update integration ---

func TestPendingMigrations_DetectedFromDiskState(t *testing.T) {
	root := projectFor(t, realProjectConfig)
	require.Len(t, pendingMigrations(root), 1)
	require.False(t, pendingMigrations(root)[0].Pending, "no legacy file, nothing to do")

	writeLegacyLedger(t, root, legacyLedgerFixture)
	require.True(t, pendingMigrations(root)[0].Pending)

	// After migrating, it reads as done — with no version marker stored
	// anywhere, so there is nothing that can drift.
	var buf bytes.Buffer
	require.NoError(t, runProjectMigrations(context.Background(), &buf, root))
	require.False(t, pendingMigrations(root)[0].Pending)
}

func TestRunProjectMigrations_NothingPending(t *testing.T) {
	root := projectFor(t, realProjectConfig)
	var buf bytes.Buffer
	require.NoError(t, runProjectMigrations(context.Background(), &buf, root))
	require.Contains(t, buf.String(), "nothing pending")
}

func TestRunProjectMigrations_RunsAndPrintsTheGitAddLine(t *testing.T) {
	root := projectFor(t, realProjectConfig)
	writeLegacyLedger(t, root, legacyLedgerFixture)

	var buf bytes.Buffer
	require.NoError(t, runProjectMigrations(context.Background(), &buf, root))
	out := buf.String()
	require.Contains(t, out, "bodies byte-identical ... OK")
	require.Contains(t, out, "nothing committed")
	require.Contains(t, out, "git add")
}

func TestRunProjectMigrations_NoConfigIsNotAFailure(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, runProjectMigrations(context.Background(), &buf, t.TempDir()))
	require.Contains(t, buf.String(), "skipped")
}

// TestFrameworkUpdate_HasTheThreeFlagsAndNoCommitFlag pins the surface,
// including the flag that deliberately does NOT exist.
func TestFrameworkUpdate_Flags(t *testing.T) {
	repo, cwd := "", ""
	cmd := newFrameworkUpdateCmd(&repo, &cwd)
	for _, name := range []string{"dry-run", "no-migrate", "repair"} {
		require.NotNil(t, cmd.Flags().Lookup(name), "--%s must exist", name)
	}
	require.Nil(t, cmd.Flags().Lookup("no-repair"),
		"repair is opt-IN: a file-copying verb must not start spending money by default")
	require.Nil(t, cmd.Flags().Lookup("no-commit"),
		"this command commits nothing, so there is nothing to suppress")
	require.Contains(t, cmd.Long, "COMMITS NOTHING")
}

func TestEmitFrameworkDryRun_ReportsPendingMigrations(t *testing.T) {
	root := projectFor(t, realProjectConfig)
	writeLegacyLedger(t, root, legacyLedgerFixture)

	var buf bytes.Buffer
	// No framework repo configured: the framework half is reported as
	// uncomparable and stepped over, because the migration half is often
	// the reason someone ran --dry-run at all.
	require.NoError(t, emitFrameworkDryRun(context.Background(), &buf, "", root))
	out := buf.String()
	require.Contains(t, out, "framework: cannot compare")
	require.Contains(t, out, "migration deferred: PENDING")
	require.Contains(t, out, "nothing written, nothing committed")
}
