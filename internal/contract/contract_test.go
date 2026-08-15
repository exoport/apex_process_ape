package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeTable drops a terminal-contracts.csv into a fresh project root.
func writeTable(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, TableFile), []byte(body), 0o600))
	return root
}

// The table the framework ships, verbatim from its answer handoff.
const frameworkTable = `# _apex/terminal-contracts.csv
skill,pattern
apex-story-batch-dev,^run_status:
apex-story-batch-review,^run_status:
apex-epic-batch-review,^epics:
`

// TestLoad_AbsentFileEnrolsNothing is the version-skew lock: a project
// whose framework predates the table must behave exactly as it did
// before this check existed — no error, no enrolment, no check.
func TestLoad_AbsentFileEnrolsNothing(t *testing.T) {
	t.Parallel()
	tbl, err := Load(t.TempDir())
	require.NoError(t, err)
	require.Zero(t, tbl.Len())
	require.Empty(t, tbl.Warnings)
	// And Check on an empty table is inert for any skill.
	require.Equal(t, StatusNotEnrolled,
		tbl.Check("apex-story-batch-dev", "anything", true).Status)
}

func TestLoad_FrameworkTable(t *testing.T) {
	t.Parallel()
	tbl, err := Load(writeTable(t, frameworkTable))
	require.NoError(t, err)
	require.Empty(t, tbl.Warnings)
	require.Equal(t, []string{
		"apex-epic-batch-review", "apex-story-batch-dev", "apex-story-batch-review",
	}, tbl.Skills())
}

// TestCheck_RealContractBlock matches against the closing message of an
// actual captured run — the `run_status: partial` YAML block sits at the
// end of a 3.5 KB message, so the pattern must anchor per line, not at
// string start.
func TestCheck_RealContractBlock(t *testing.T) {
	t.Parallel()
	tbl, err := Load(writeTable(t, frameworkTable))
	require.NoError(t, err)

	closing := strings.Join([]string{
		"**Story Batch Dev — Partial**",
		"",
		"5 of 6 stories are done and committed.",
		"",
		"```yaml",
		"run_status: partial",
		`batch_id: "story-batch-dev-20260808013500"`,
		"```",
	}, "\n")

	res := tbl.Check("apex-story-batch-dev", closing, true)
	require.Equal(t, StatusPresent, res.Status)
	require.Empty(t, res.Diagnostic())
}

// TestCheck_MissingContract is the defect this gate exists to catch: a
// batch orchestrator that yields mid-run and never reaches its summary.
func TestCheck_MissingContract(t *testing.T) {
	t.Parallel()
	tbl, err := Load(writeTable(t, frameworkTable))
	require.NoError(t, err)

	// The real closing message from the 2026-07-31 incident.
	closing := "The story 1.2 agent has been resumed in the background to finish its " +
		"work. I'll wait for it to complete before spawning the agent for story 1.3."

	res := tbl.Check("apex-story-batch-review", closing, true)
	require.Equal(t, StatusMissing, res.Status)
	require.Contains(t, res.Diagnostic(), "did not reach its summary step")
	require.Contains(t, res.Diagnostic(), "apex-story-batch-review")
}

// TestCheck_UnenrolledSkillIsInert: apex-story-batch-create emits no
// terminal contract, so the absence of one must never be read as a
// failure. Absence of a contract that was never promised is not evidence.
func TestCheck_UnenrolledSkillIsInert(t *testing.T) {
	t.Parallel()
	tbl, err := Load(writeTable(t, frameworkTable))
	require.NoError(t, err)

	res := tbl.Check("apex-story-batch-create", "Batch Story Creation Complete", true)
	require.Equal(t, StatusNotEnrolled, res.Status)
	require.Empty(t, res.Diagnostic())
}

// TestCheck_EpicPatternDiffers proves the pattern approach earns its
// keep: epic-batch-review's contract is a different shape entirely, and
// a hard-coded `run_status` would have missed it.
func TestCheck_EpicPatternDiffers(t *testing.T) {
	t.Parallel()
	tbl, err := Load(writeTable(t, frameworkTable))
	require.NoError(t, err)

	epicBlock := "Totals across epics:\n\n```yaml\nepics:\n  - epic: \"1\"\n```"
	require.Equal(t, StatusPresent, tbl.Check("apex-epic-batch-review", epicBlock, true).Status)
	// The story pattern must NOT match an epic contract.
	require.Equal(t, StatusMissing, tbl.Check("apex-story-batch-dev", epicBlock, true).Status)
}

// TestCheck_NoTranscriptIsReportedNotViolated: an unreadable transcript
// is an ape-side gap, not evidence the run failed.
func TestCheck_NoTranscriptIsReportedNotViolated(t *testing.T) {
	t.Parallel()
	tbl, err := Load(writeTable(t, frameworkTable))
	require.NoError(t, err)

	res := tbl.Check("apex-story-batch-dev", "", false)
	require.Equal(t, StatusNoTranscript, res.Status)
	require.Contains(t, res.Diagnostic(), "not verified")

	// Whitespace-only closing message lands the same way.
	require.Equal(t, StatusNoTranscript,
		tbl.Check("apex-story-batch-dev", "   \n  ", true).Status)
}

// TestParse_MalformedRowsWarnButNeverFail: ape does not own this file,
// so a bad row must degrade to "that skill is not enrolled", never to a
// failed run.
func TestParse_MalformedRowsWarnButNeverFail(t *testing.T) {
	t.Parallel()
	tbl, err := Load(writeTable(t, strings.Join([]string{
		"skill,pattern",
		"good-skill,^ok:",
		"bad-regex-skill,^(unclosed",
		"lonely-column",
		",^empty-skill:",
		"empty-pattern-skill,",
		"",
		"# a comment row",
		"another-good,^fine:",
	}, "\n")))
	require.NoError(t, err)

	require.Equal(t, []string{"another-good", "good-skill"}, tbl.Skills())
	require.Len(t, tbl.Warnings, 4)
	require.Equal(t, StatusPresent, tbl.Check("good-skill", "ok: yes", true).Status)
	// The skill with the invalid pattern is simply not enrolled.
	require.Equal(t, StatusNotEnrolled, tbl.Check("bad-regex-skill", "anything", true).Status)
}

// TestParse_HeaderOptional: the header row is tolerated, not required.
func TestParse_HeaderOptional(t *testing.T) {
	t.Parallel()
	tbl, err := Load(writeTable(t, "apex-story-batch-dev,^run_status:\n"))
	require.NoError(t, err)
	require.Equal(t, []string{"apex-story-batch-dev"}, tbl.Skills())
}

// TestTable_NilSafe: every accessor tolerates a nil table, which is what
// a failed load leaves behind.
func TestTable_NilSafe(t *testing.T) {
	t.Parallel()
	var tbl *Table
	require.Zero(t, tbl.Len())
	require.Nil(t, tbl.Skills())
	require.Equal(t, StatusNotEnrolled, tbl.Check("any", "text", true).Status)
}
