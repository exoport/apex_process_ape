package release

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// tracker writes a sprint-status.yaml with the given top-level body and
// returns its path, plus the planning dir that holds records.
func project(t *testing.T, tracker string) (trackerPath, planningDir string) {
	t.Helper()
	root := t.TempDir()
	impl := filepath.Join(root, "implementation")
	planning := filepath.Join(root, "planning")
	require.NoError(t, os.MkdirAll(impl, 0o755))
	require.NoError(t, os.MkdirAll(planning, 0o755))
	path := filepath.Join(impl, "sprint-status.yaml")
	if tracker != "" {
		require.NoError(t, os.WriteFile(path, []byte(tracker), 0o644))
	}
	return path, planning
}

func writeRecord(t *testing.T, planningDir, sliceID, body string) {
	t.Helper()
	dir := filepath.Join(planningDir, RecordsDirName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(RecordPath(dir, sliceID), []byte(body), 0o644))
}

const twoSliceTracker = `created_at: "20260901120000"
release_slices:
  v1.0.0:
    epics: [1, 2]
    declared_at: "20260903120000"
    leave: []
  v1.1.0:
    epics: [3]
    declared_at: "20260904120000"
    leave:
      - story: 3-2_late-thing
        reason: "waits on 4-1"
active_slice: v1.1.0
development_status:
  epic-1: done
  1-1_a: done
  epic-2: done
  2-1_b: done
  epic-3: backlog
  3-1_c: backlog
  3-2_late-thing: backlog
`

// releasedRecord is the framework's own template shape, filled in.
const releasedRecord = `---
release_id: "v1.0.0"
slice: "v1.0.0"
status: released
frozen_at: "abc1234"
blocking: []
acceptance: "ok"
ships:
  - cmd/thing
gates:
  - name: tests
    command: make test
    required: true
    tier: gate
  - name: lint
    command: make lint
    required: false
    tier: per-patch
evidence_folder: "evidence"
repair_commit_type: fix
readiness: {}
overrides: []
slice_change_log:
  - {timestamp: "20260903120000", sha: "abc", before: "", after: declared, authorization: "operator: ship it"}
---

# Release v1.0.0
`

// TestProject_ReleasedSliceLeavesTheUnreleasedSet is the whole point of
// reading the record rather than the tracker entry: `released` is
// asserted in exactly one place, and it is what removes epics from scope.
func TestProject_ReleasedSliceLeavesTheUnreleasedSet(t *testing.T) {
	trackerPath, planning := project(t, twoSliceTracker)
	writeRecord(t, planning, "v1.0.0", releasedRecord)

	st, err := Project(trackerPath, planning)
	require.NoError(t, err)
	require.True(t, st.TrackerPresent)
	require.Equal(t, ActiveDeclared, st.ActiveResolution)
	require.Equal(t, "v1.1.0", st.ActiveSlice)
	require.Equal(t, []int{3}, st.ActiveEpics)
	require.Equal(t, []int{3}, st.UnreleasedEpics, "epics 1 and 2 shipped, per the record")

	require.Len(t, st.Slices, 2)
	require.True(t, st.Slices[0].Released)
	require.False(t, st.Slices[1].Released, "no record for v1.1.0 means not released")
	require.True(t, st.Slices[1].Active)
	require.Equal(t, "3-2_late-thing", st.Slices[1].Leave[0].Story)
}

// TestProject_NoRecordIsNotReleased — an absent record, an absent folder
// and an unreadable one all mean not released, and the epic stays in
// scope. That asymmetry is the point: the only way out of the set is a
// record that says the epic shipped.
func TestProject_NoRecordIsNotReleased(t *testing.T) {
	trackerPath, planning := project(t, twoSliceTracker)

	st, err := Project(trackerPath, planning)
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3}, st.UnreleasedEpics)
	for _, sl := range st.Slices {
		require.False(t, sl.Released)
		require.False(t, sl.Record.Present)
		require.Empty(t, sl.Record.Unreadable, "an absent record is absent, not unreadable")
		require.Empty(t, sl.Record.Status, "an absent record asserts no status")
	}
}

func TestProject_UnreadableRecordIsReportedNotGuessed(t *testing.T) {
	trackerPath, planning := project(t, twoSliceTracker)
	writeRecord(t, planning, "v1.0.0", "no frontmatter here at all\n")
	writeRecord(t, planning, "v1.1.0", "---\nstatus: [unclosed\n")

	st, err := Project(trackerPath, planning)
	require.NoError(t, err)
	for _, sl := range st.Slices {
		require.True(t, sl.Record.Present)
		require.NotEmpty(t, sl.Record.Unreadable)
		require.Empty(t, sl.Record.Status, "a record that cannot be read asserts nothing")
		require.False(t, sl.Released)
	}
	require.Equal(t, []int{1, 2, 3}, st.UnreleasedEpics)
}

// TestProject_MalformedYAMLInFrontmatter separates "no frontmatter block"
// from "a block that is not YAML" — both unreadable, neither a status.
func TestProject_MalformedYAMLInFrontmatter(t *testing.T) {
	trackerPath, planning := project(t, twoSliceTracker)
	writeRecord(t, planning, "v1.0.0", "---\nstatus: released\n  bad: indent\n---\n\nbody\n")

	st, err := Project(trackerPath, planning)
	require.NoError(t, err)
	require.Contains(t, st.Slices[0].Record.Unreadable, "not valid YAML")
	require.False(t, st.Slices[0].Released, "unparseable is never released")
}

func TestProject_RecordFieldsAreReadVerbatim(t *testing.T) {
	trackerPath, planning := project(t, twoSliceTracker)
	writeRecord(t, planning, "v1.0.0", releasedRecord)

	st, err := Project(trackerPath, planning)
	require.NoError(t, err)
	rec := st.Slices[0].Record
	require.Equal(t, "v1.0.0", rec.ReleaseID)
	require.Equal(t, StatusReleased, rec.Status)
	require.Equal(t, "abc1234", rec.FrozenAt)
	require.Equal(t, "ok", rec.Acceptance)
	require.Equal(t, []string{"cmd/thing"}, rec.Ships)
	require.Equal(t, "evidence", rec.EvidenceFolder)
	require.Equal(t, "fix", rec.RepairCommitType)
	require.Len(t, rec.Gates, 2)
	require.Equal(t, 1, rec.GatesRequired())
	require.Equal(t, "per-patch", rec.Gates[1].Tier)
	require.Empty(t, rec.Blocking)
	require.Empty(t, rec.ReleaseNotes, "an absent release_notes is a correct record")
}

// TestProject_SliceChangeLogIsACount: the log is read so its length can be
// reported, and is never carried into the projection.
func TestProject_SliceChangeLogIsACount(t *testing.T) {
	trackerPath, planning := project(t, twoSliceTracker)
	writeRecord(t, planning, "v1.0.0", releasedRecord)

	st, err := Project(trackerPath, planning)
	require.NoError(t, err)
	require.Equal(t, 1, st.Slices[0].Record.SliceChangeLogEntries)
}

// TestProject_TaggerFromObjectStaysSeparate — provenance is not
// authorization, and a --backfill-legacy record must not be able to claim
// one by carrying the other.
func TestProject_TaggerFromObjectStaysSeparate(t *testing.T) {
	trackerPath, planning := project(t, twoSliceTracker)
	writeRecord(t, planning, "v1.0.0", `---
release_id: "v1.0.0"
status: released
blocking: []
acceptance: "legacy-backfill"
tagger_from_object: "Someone <s@example.com>"
---

body
`)
	st, err := Project(trackerPath, planning)
	require.NoError(t, err)
	rec := st.Slices[0].Record
	require.Equal(t, "Someone <s@example.com>", rec.TaggerFromObject)
	require.Empty(t, rec.TagAuthorization, "provenance never populates authorization")
}

// TestProject_SynthesizedFieldsSurviveAHostileRecord: a record carrying
// `present:` or `unreadable:` keys cannot talk its way into a state.
func TestProject_SynthesizedFieldsSurviveAHostileRecord(t *testing.T) {
	trackerPath, planning := project(t, twoSliceTracker)
	writeRecord(t, planning, "v1.0.0", "---\nstatus: released\npresent: false\nunreadable: \"nope\"\npath: /elsewhere\n---\n\nbody\n")

	st, err := Project(trackerPath, planning)
	require.NoError(t, err)
	rec := st.Slices[0].Record
	require.True(t, rec.Present)
	require.Empty(t, rec.Unreadable)
	require.Equal(t, RecordPath(filepath.Join(planning, RecordsDirName), "v1.0.0"), rec.Path)
}

func TestProject_UndeclaredActiveSliceFallsBackToUnreleased(t *testing.T) {
	trackerPath, planning := project(t, `development_status:
  epic-1: done
  1-1_a: done
  epic-2: backlog
  2-1_b: backlog
`)
	st, err := Project(trackerPath, planning)
	require.NoError(t, err)
	require.Equal(t, ActiveUndeclared, st.ActiveResolution)
	require.Empty(t, st.Slices)
	require.Equal(t, []int{1, 2}, st.ActiveEpics)
	require.Equal(t, []int{1, 2}, st.UnreleasedEpics)
}

// TestProject_DanglingActiveSlice: `active_slice` naming a slice
// `release_slices` does not carry resolves the same as absent — a
// fallback, reported as its own state so a consumer can tell a repair
// from a default.
func TestProject_DanglingActiveSlice(t *testing.T) {
	trackerPath, planning := project(t, `release_slices:
  v1.0.0:
    epics: [1]
    declared_at: "20260903120000"
active_slice: v9.9.9
development_status:
  epic-1: done
  1-1_a: done
  epic-2: backlog
  2-1_b: backlog
`)
	st, err := Project(trackerPath, planning)
	require.NoError(t, err)
	require.Equal(t, ActiveDangling, st.ActiveResolution)
	require.Equal(t, "v9.9.9", st.ActiveSlice)
	require.Equal(t, []int{1, 2}, st.ActiveEpics, "falls back to every unreleased epic")
	for _, sl := range st.Slices {
		require.False(t, sl.Active)
	}
}

// TestProject_NoTrackerReportsNullNotEmpty is the distinction the
// framework called load-bearing: "no tracker to enumerate from" and "no
// epics" are different answers, and only one is true here.
func TestProject_NoTrackerReportsNullNotEmpty(t *testing.T) {
	trackerPath, planning := project(t, "")

	st, err := Project(trackerPath, planning)
	require.NoError(t, err)
	require.False(t, st.TrackerPresent)
	require.Nil(t, st.UnreleasedEpics)
	require.Nil(t, st.ActiveEpics)
	require.Empty(t, st.Slices)
}

func TestProject_MalformedSliceEntryIsListed(t *testing.T) {
	trackerPath, planning := project(t, `release_slices:
  v1.0.0: "not a mapping"
active_slice: v1.0.0
development_status:
  epic-1: done
  1-1_a: done
`)
	st, err := Project(trackerPath, planning)
	require.NoError(t, err)
	require.Len(t, st.Slices, 1, "a slice nobody can parse is a finding, not something to drop")
	require.NotEmpty(t, st.Slices[0].Malformed)
	require.Equal(t, ActiveDeclared, st.ActiveResolution)
	require.Empty(t, st.Slices[0].Epics)
}

func TestActiveRecord(t *testing.T) {
	trackerPath, planning := project(t, twoSliceTracker)
	writeRecord(t, planning, "v1.1.0", `---
release_id: "v1.1.0"
status: declared
blocking:
  - "no gate declared — awaits a --gate invocation"
acceptance: "none-declared"
---

body
`)
	st, err := Project(trackerPath, planning)
	require.NoError(t, err)
	rec, ok := st.ActiveRecord()
	require.True(t, ok)
	require.Equal(t, StatusDeclared, rec.Status)
	require.Len(t, rec.Blocking, 1)
	require.Equal(t, "none-declared", rec.Acceptance)
}

func TestActiveRecord_NoneWhenImplicit(t *testing.T) {
	trackerPath, planning := project(t, "development_status:\n  epic-1: done\n  1-1_a: done\n")
	st, err := Project(trackerPath, planning)
	require.NoError(t, err)
	_, ok := st.ActiveRecord()
	require.False(t, ok, "an implicit scope has no record")
}
