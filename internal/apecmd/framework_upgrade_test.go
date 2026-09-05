package apecmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/migration"
	"github.com/stretchr/testify/require"
)

// seedMigration writes one entry under the project's _apex/migrations/.
func seedMigration(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, "_apex", migration.DirName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
}

func seedFrameworkMetadata(t *testing.T, root string, rows []framework.AppliedMigration) {
	t.Helper()
	require.NoError(t, framework.WriteMetadata(root, &framework.Metadata{
		InstalledAt: time.Now(),
		Migrations:  rows,
	}))
}

const derivableEntry = `---
id: v0.16.0_seq-01
version: 0.16.0
seq: 1
kind: derivable
blocking: true
check: "exit 1"
command: "true"
summary: Slice keys and retro rows.
---

body
`

const judgedEntry = `---
id: v0.17.0_seq-02
version: 0.17.0
seq: 2
kind: judged
blocking: false
skill: apex-upgrade-project
summary: Resolve requirement_ids.
---

## Instructions

Read the story.
`

func TestMigrationPlan_AbsentFolderIsNormal(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	p, err := loadMigrationPlan(t.Context(), root, migration.NoCheckRunner(), false)
	require.NoError(t, err)
	require.False(t, p.Present)

	var b strings.Builder
	emitMigrationPlan(&b, p)
	require.Contains(t, b.String(), "ships no migration list")
}

func TestMigrationPlan_DerivableAndJudged(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	seedMigration(t, root, "v0.16.0_seq-01_slice-keys.md", derivableEntry)
	seedMigration(t, root, "v0.17.0_seq-02_requirement-ids.md", judgedEntry)

	p, err := loadMigrationPlan(t.Context(), root, migration.ShellRunner{}, true)
	require.NoError(t, err)
	require.True(t, p.Present)
	require.Len(t, p.Rows, 2)

	var b strings.Builder
	emitMigrationPlan(&b, p)
	out := b.String()
	require.Contains(t, out, "run: true", "the derivable entry names the command ape would run")
	require.Contains(t, out, "dispatch apex-upgrade-project",
		"the judged entry names its skill and is never run")
	require.Contains(t, out, "2 pending (1 runnable now, 1 judged)")
}

// TestMigrationPlan_LedgerMakesItApplied is the ledger's whole job: a
// second run is a no-op because the id is recorded, independent of what
// the command does.
func TestMigrationPlan_LedgerMakesItApplied(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	seedMigration(t, root, "v0.16.0_seq-01_slice-keys.md", derivableEntry)
	seedFrameworkMetadata(t, root, []framework.AppliedMigration{
		{ID: "v0.16.0_seq-01", Version: "0.16.0", AppliedAt: "20260905120000"},
	})

	// Checks off: the ledger alone answers "what does this project owe".
	p, err := loadMigrationPlan(t.Context(), root, migration.NoCheckRunner(), false)
	require.NoError(t, err)
	require.Equal(t, migration.StateApplied, p.Rows[0].State)
	require.Equal(t, migration.SourceLedger, p.Rows[0].Source)
	require.Equal(t, 0, p.Counts().Runnable)
}

// TestRunUpgradeMigrations_AppliesAndRecords runs a real derivable entry
// end to end: the command executes, the ledger gains a row, and a second
// run does nothing.
func TestRunUpgradeMigrations_AppliesAndRecords(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	marker := filepath.Join(root, "ran-once")
	seedMigration(t, root, "v0.16.0_seq-01_slice-keys.md", `---
id: v0.16.0_seq-01
version: 0.16.0
seq: 1
kind: derivable
blocking: false
check: "test -f ran-once"
command: "touch ran-once"
---

body
`)
	seedFrameworkMetadata(t, root, nil)

	var b strings.Builder
	require.NoError(t, runUpgradeMigrations(t.Context(), &b, root))
	require.FileExists(t, marker)
	require.Contains(t, b.String(), "applied at")

	meta, err := framework.ReadMetadata(root)
	require.NoError(t, err)
	require.Len(t, meta.Migrations, 1)
	require.Equal(t, "v0.16.0_seq-01", meta.Migrations[0].ID)
	require.NotEmpty(t, meta.Migrations[0].AppliedAt)

	// Second run: nothing to do, and nothing appended twice.
	var b2 strings.Builder
	require.NoError(t, runUpgradeMigrations(t.Context(), &b2, root))
	require.Contains(t, b2.String(), "nothing to run")
	meta2, err := framework.ReadMetadata(root)
	require.NoError(t, err)
	require.Len(t, meta2.Migrations, 1, "an applied id is never recorded twice")
}

// TestRunUpgradeMigrations_JudgedIsNeverExecuted: the runner lists it and
// names the skill, under every flag there is.
func TestRunUpgradeMigrations_JudgedIsNeverExecuted(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	seedMigration(t, root, "v0.17.0_seq-02_requirement-ids.md", `---
id: v0.17.0_seq-02
version: 0.17.0
seq: 2
kind: judged
blocking: false
skill: apex-upgrade-project
command: "touch must-not-run"
---

body
`)
	seedFrameworkMetadata(t, root, nil)

	var b strings.Builder
	require.NoError(t, runUpgradeMigrations(t.Context(), &b, root))
	require.NoFileExists(t, filepath.Join(root, "must-not-run"),
		"a judged entry's command is never executed, even when it carries one")
	require.Contains(t, b.String(), "dispatch apex-upgrade-project")

	meta, err := framework.ReadMetadata(root)
	require.NoError(t, err)
	require.Empty(t, meta.Migrations)
}

// TestRunUpgradeMigrations_FailureStopsAndRecordsThePrefix is the
// resumability property: what succeeded is recorded, what came after is
// reported as not attempted, and the command exits without error because
// the framework install itself succeeded.
func TestRunUpgradeMigrations_FailureStopsAndRecordsThePrefix(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	seedMigration(t, root, "v0.16.0_seq-01_first.md", `---
id: v0.16.0_seq-01
version: 0.16.0
seq: 1
kind: derivable
command: "true"
---
body
`)
	seedMigration(t, root, "v0.17.0_seq-01_second.md", `---
id: v0.17.0_seq-01
version: 0.17.0
seq: 1
kind: derivable
command: "exit 4"
---
body
`)
	seedMigration(t, root, "v0.18.0_seq-01_third.md", `---
id: v0.18.0_seq-01
version: 0.18.0
seq: 1
kind: derivable
command: "touch third-ran"
---
body
`)
	seedFrameworkMetadata(t, root, nil)

	var b strings.Builder
	require.NoError(t, runUpgradeMigrations(t.Context(), &b, root),
		"a failed migration is reported, not returned — the install succeeded")
	out := b.String()
	require.Contains(t, out, "v0.17.0_seq-01: FAILED")
	require.Contains(t, out, "stopped at v0.17.0_seq-01")
	require.NoFileExists(t, filepath.Join(root, "third-ran"),
		"later entries may depend on the failed one")

	meta, err := framework.ReadMetadata(root)
	require.NoError(t, err)
	require.Len(t, meta.Migrations, 1)
	require.Equal(t, "v0.16.0_seq-01", meta.Migrations[0].ID, "the completed prefix is recorded")
}

// TestFrameworkMetadata_LedgerSurvivesARewrite: framework.yaml is
// regenerated wholesale by every update, so losing the ledger would make
// every applied migration look pending again.
func TestFrameworkMetadata_LedgerSurvivesARewrite(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	seedFrameworkMetadata(t, root, []framework.AppliedMigration{
		{ID: "v0.16.0_seq-01", Version: "0.16.0", AppliedAt: "20260905120000"},
	})

	require.NoError(t, framework.AppendMigrations(root, []framework.AppliedMigration{
		{ID: "v0.16.0_seq-01", Version: "0.16.0", AppliedAt: "20260906120000"},
		{ID: "v0.17.0_seq-01", Version: "0.17.0", AppliedAt: "20260906120001"},
	}))

	meta, err := framework.ReadMetadata(root)
	require.NoError(t, err)
	require.Len(t, meta.Migrations, 2)
	require.Equal(t, "20260905120000", meta.Migrations[0].AppliedAt,
		"an id already recorded keeps its original stamp")
	require.Equal(t, "v0.17.0_seq-01", meta.Migrations[1].ID)
}

func TestDoctorUpgradeMigrations_States(t *testing.T) {
	t.Run("no list", func(t *testing.T) {
		root := newTestProject(t, realProjectConfig)
		res := checkUpgradeMigrations(t.Context(), doctorEnv{ProjectRoot: root})
		require.Equal(t, StatusInfo, res.Status)
	})
	t.Run("pending warns and never fails", func(t *testing.T) {
		root := newTestProject(t, realProjectConfig)
		seedMigration(t, root, "v0.16.0_seq-01_slice-keys.md", derivableEntry)
		seedFrameworkMetadata(t, root, nil)
		res := checkUpgradeMigrations(t.Context(), doctorEnv{ProjectRoot: root})
		require.Equal(t, StatusWarn, res.Status,
			"a project that owes a migration is behind, not broken")
		require.Contains(t, res.Message, "1 pending")
		require.Contains(t, res.Message, "marked blocking")
	})
	t.Run("all applied", func(t *testing.T) {
		root := newTestProject(t, realProjectConfig)
		seedMigration(t, root, "v0.16.0_seq-01_slice-keys.md", derivableEntry)
		seedFrameworkMetadata(t, root, []framework.AppliedMigration{{ID: "v0.16.0_seq-01"}})
		res := checkUpgradeMigrations(t.Context(), doctorEnv{ProjectRoot: root})
		require.Equal(t, StatusOK, res.Status)
	})
}

// TestDoctorUpgradeMigrations_RunsNoChecks: doctor rows run unattended in
// --strict CI, and a row that shelled out per entry would be neither
// cheap nor predictable.
func TestDoctorUpgradeMigrations_RunsNoChecks(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	seedMigration(t, root, "v0.16.0_seq-01_slice-keys.md", `---
id: v0.16.0_seq-01
version: 0.16.0
seq: 1
kind: derivable
check: "touch check-ran"
command: "true"
---
body
`)
	seedFrameworkMetadata(t, root, nil)

	checkUpgradeMigrations(t.Context(), doctorEnv{ProjectRoot: root})
	require.NoFileExists(t, filepath.Join(root, "check-ran"))
}
