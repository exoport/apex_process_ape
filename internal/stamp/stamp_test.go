package stamp

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

const projectConfig = `config_schema_version: "1"
project_name: ts-project
extensions: []
user_name: Boss
communication_language: English
document_output_language: English
user_skill_level: intermediate
apex_folder: _apex
output_folder: _output
development_folder: development
docs_folder: docs
implementation_folder: development/implementation
planning_folder: development/planning
governance_repository_path: ""
governance_folder: development/governance
governance_staleness: warn
functionality_folder: development/functionality
`

func newProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "_apex", "config.yaml"), []byte(projectConfig), 0o644,
	))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "development", "implementation"), 0o755))
	return root
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	//nolint:gosmopolitan // local wall-clock is the contract under test
	parsed, err := time.ParseInLocation(apexcfg.TimestampLayout, s, time.Local)
	require.NoError(t, err)
	return parsed
}

// TestIssue_NeverMovesBackwards is the guarantee, stated as the framework
// states it: a field answering "when was this last written" never moves
// backwards. This is the unit test PLAN-64 5.6 asks for by name.
func TestIssue_NeverMovesBackwards(t *testing.T) {
	root := newProject(t)

	// A skewed sequence: forward, then a clock that jumps back nearly
	// three hours (the ~2h44m regression observed in the field), then
	// back to normal.
	sequence := []string{
		"20260903120000",
		"20260903120500",
		"20260903092100", // the skew
		"20260903092200", // still behind
		"20260903130000", // recovered
	}
	want := []string{
		"20260903120000",
		"20260903120500",
		"20260903120500", // clamped to the floor, not the wall clock
		"20260903120500", // still clamped
		"20260903130000", // moves again once the clock passes the floor
	}

	var idx int
	iss := New(root, func() time.Time { return at(t, sequence[idx]) })
	for i := range sequence {
		idx = i
		require.Equal(t, want[i], iss.Issue(), "issue %d", i)
	}
}

// TestIssue_MonotonicAcrossIssuers is the property that matters in the
// field: the regressions were written by DIFFERENT agents, so the floor
// has to survive the process that set it.
func TestIssue_MonotonicAcrossIssuers(t *testing.T) {
	root := newProject(t)

	first := New(root, func() time.Time { return at(t, "20260903120500") })
	require.Equal(t, "20260903120500", first.Issue())

	// A second process, on a machine whose clock is behind.
	second := New(root, func() time.Time { return at(t, "20260903092100") })
	require.Equal(t, "20260903120500", second.Issue())
}

// TestIssue_SeedsFromTrackerWhenStateIsAbsent is the fresh-clone case.
// Without the seed a clean checkout resets the floor to the wall clock,
// and `ape sprint verify`'s exit-5 check — meant to be the detector of
// last resort — becomes the only detector.
func TestIssue_SeedsFromTrackerWhenStateIsAbsent(t *testing.T) {
	root := newProject(t)
	writeTracker(t, root, "updated_at: \"20260903120500\"\n")

	// No state file exists: this is a clone, and the wall clock is behind
	// what the tracker already claims.
	require.NoFileExists(t, runlog.TimestampStatePath(root))

	iss := New(root, func() time.Time { return at(t, "20260903092100") })
	require.Equal(t, "20260903120500", iss.Issue(),
		"the floor must be seeded from the tracker, not reset to the wall clock")
}

// TestIssue_SeedAcceptsAnUnquotedStamp — a 14-digit YAML scalar decodes as
// an int, and rejecting it would drop exactly the seed the read exists to
// find.
func TestIssue_SeedAcceptsAnUnquotedStamp(t *testing.T) {
	root := newProject(t)
	writeTracker(t, root, "updated_at: 20260903120500\n")

	iss := New(root, func() time.Time { return at(t, "20260903092100") })
	require.Equal(t, "20260903120500", iss.Issue())
}

// TestIssue_IgnoresAMalformedFloor covers both halves of "never compare a
// value that is not a stamp": a corrupt state file must not become the
// floor for the life of the project, and a tracker whose updated_at is
// prose must not either.
func TestIssue_IgnoresAMalformedFloor(t *testing.T) {
	root := newProject(t)
	writeTracker(t, root, "updated_at: \"not a stamp\"\n")
	statePath := runlog.TimestampStatePath(root)
	require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))
	require.NoError(t, os.WriteFile(statePath, []byte("garbage\n"), 0o644))

	iss := New(root, func() time.Time { return at(t, "20260903092100") })
	require.Equal(t, "20260903092100", iss.Issue(),
		"neither a corrupt state file nor a prose updated_at may become the floor")
}

// TestIssue_NoProjectStillIssues — `ape chat` in a bare directory needs a
// timestamp, and there is no project to hold a floor.
func TestIssue_NoProjectStillIssues(t *testing.T) {
	iss := New(t.TempDir(), func() time.Time { return at(t, "20260903092100") })
	require.Equal(t, "20260903092100", iss.Issue())
}

// TestIssue_UnwritableStateDoesNotFail — a read-only checkout or a sandbox
// mount must still resolve a timestamp. Losing the floor degrades the
// guarantee to what it was before this package; failing the command would
// degrade it to nothing.
func TestIssue_UnwritableStateDoesNotFail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows has no POSIX permission bits — chmod sets only the read-only attribute")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this test sets")
	}
	root := newProject(t)
	apeRoot := filepath.Dir(runlog.TimestampStatePath(root))
	require.NoError(t, os.MkdirAll(apeRoot, 0o755))
	require.NoError(t, os.Chmod(apeRoot, 0o500))
	t.Cleanup(func() { _ = os.Chmod(apeRoot, 0o755) })

	iss := New(root, func() time.Time { return at(t, "20260903092100") })
	require.Equal(t, "20260903092100", iss.Issue())
}

// TestClock_DateAndTimestampShareOneInstant is why the Issuer exposes a
// Clock rather than a formatted string. Deriving `date` from the raw wall
// clock while `timestamp` is clamped forward would let a record carry a
// date of one day and a timestamp of the next.
func TestClock_DateAndTimestampShareOneInstant(t *testing.T) {
	root := newProject(t)

	// Floor is just after midnight on the 4th.
	first := New(root, func() time.Time { return at(t, "20260904000030") })
	require.Equal(t, "20260904000030", first.Issue())

	// A second process is still on the 3rd. Both fields must clamp
	// together onto the 4th.
	second := New(root, func() time.Time { return at(t, "20260903235900") })
	res, err := apexcfg.ResolveAt(root, second.Clock())
	require.NoError(t, err)
	require.Equal(t, "20260904000030", res.Timestamp)
	require.Equal(t, "2026-09-04", res.Date,
		"date and timestamp must derive from the same clamped instant")
}

func writeTracker(t *testing.T, root, body string) {
	t.Helper()
	path := filepath.Join(root, "development", "implementation", "sprint-status.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}
