package apecmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
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
		"config.resolved":   checkConfigResolved,
		"registry.drift":    checkRegistryDrift,
		"story.frontmatter": checkStoryFrontmatter,
		"sprint.divergence": checkSprintDivergence,
		"memory.size":       checkMemorySize,
		"migration.pending": checkMigrationPending,
	}
	for name, fn := range checks {
		t.Run(name, func(t *testing.T) {
			res := fn(ctx, env)
			require.Equal(t, StatusInfo, res.Status, "%s must degrade to INFO: %s", name, res.Message)
		})
	}
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
