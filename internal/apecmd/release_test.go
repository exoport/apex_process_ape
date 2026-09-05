package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/release"
	"github.com/stretchr/testify/require"
)

const releaseTracker = `created_at: "20260901120000"
release_slices:
  v1.0.0:
    epics: [1]
    declared_at: "20260903120000"
    leave: []
  v1.1.0:
    epics: [2]
    declared_at: "20260904120000"
    leave: []
active_slice: v1.1.0
development_status:
  epic-1: done
  1-1_a: done
  epic-2: backlog
  2-1_b: backlog
`

func writeReleaseTracker(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, "development", "implementation")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sprint-status.yaml"), []byte(body), 0o644))
}

func writeReleaseRecord(t *testing.T, root, sliceID, body string) {
	t.Helper()
	dir := filepath.Join(root, "development", "planning", release.RecordsDirName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(release.RecordPath(dir, sliceID), []byte(body), 0o644))
}

func TestReleaseStatus_JSONProjection(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeReleaseTracker(t, root, releaseTracker)
	writeReleaseRecord(t, root, "v1.0.0", `---
release_id: "v1.0.0"
slice: "v1.0.0"
status: released
frozen_at: "abc1234"
blocking: []
acceptance: "ok"
gates:
  - {name: tests, command: make test, required: true, tier: gate}
---

body
`)

	out := runCmd(t, newReleaseStatusCmd(), "--output-format", "json")
	var st release.Status
	require.NoError(t, json.Unmarshal([]byte(out), &st))
	require.Equal(t, release.ActiveDeclared, st.ActiveResolution)
	require.Equal(t, "v1.1.0", st.ActiveSlice)
	require.Equal(t, []int{2}, st.ActiveEpics)
	require.Equal(t, []int{2}, st.UnreleasedEpics)
	require.Len(t, st.Slices, 2)
	require.True(t, st.Slices[0].Released)
	require.False(t, st.Slices[1].Released)
}

// TestReleaseStatus_ExitZeroOnAnUnreadableRecord: a projection that
// halted on one bad record could not report the others.
func TestReleaseStatus_ExitZeroOnAnUnreadableRecord(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeReleaseTracker(t, root, releaseTracker)
	writeReleaseRecord(t, root, "v1.0.0", "not a record\n")

	out := runCmd(t, newReleaseStatusCmd())
	require.Contains(t, out, "unreadable")
	require.Contains(t, out, "counted as NOT released")
	// runCmd asserts Execute() returned nil.
}

func TestReleaseStatus_HumanTable(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeReleaseTracker(t, root, releaseTracker)
	writeReleaseRecord(t, root, "v1.1.0", `---
release_id: "v1.1.0"
status: declared
blocking:
  - "no gate declared — awaits a --gate \"<name>=<command>\" invocation"
acceptance: "none-declared"
gates: []
---

body
`)

	out := runCmd(t, newReleaseStatusCmd())
	require.Contains(t, out, "active_slice: v1.1.0")
	require.Contains(t, out, "v1.1.0 *", "the active slice is marked")
	require.Contains(t, out, "0 declared, 0 required")
	require.Contains(t, out, "no gate declared")
	// v1.0.0 has no record at all: absent, and no status invented for it.
	require.Contains(t, out, "absent")
}

// TestReleaseStatus_GateResultsAreNotReported locks the cut the framework
// confirmed: the declaration is projected, the body's per-row results are
// not, because the record asserts its verdict in exactly one place.
func TestReleaseStatus_GateResultsAreNotReported(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeReleaseTracker(t, root, releaseTracker)
	writeReleaseRecord(t, root, "v1.1.0", `---
release_id: "v1.1.0"
status: declared
blocking: []
gates:
  - {name: tests, command: make test, required: true, tier: gate}
---

## Gate table

| Gate | Command | Required | Tier | Covers | Exit | Sha | Duration | Transcript | Digest | Result |
| ---- | ------- | -------- | ---- | ------ | ---- | --- | -------- | ---------- | ------ | ------ |
| tests | make test | true | gate | | 1 | abc | 3s | evidence/t.log | deadbeef | RED |
`)

	out := runCmd(t, newReleaseStatusCmd())
	require.Contains(t, out, "1 declared, 1 required")
	require.NotContains(t, out, "RED", "gate results belong to the record's body, not to this projection")

	jsonOut := runCmd(t, newReleaseStatusCmd(), "--output-format", "json")
	require.NotContains(t, jsonOut, "RED")
}

func TestReleaseStatus_NoTrackerSaysSo(t *testing.T) {
	newTestProject(t, realProjectConfig)

	out := runCmd(t, newReleaseStatusCmd())
	require.Contains(t, out, "no tracker at")
	require.Contains(t, out, "epic shards")

	jsonOut := runCmd(t, newReleaseStatusCmd(), "--output-format", "json")
	var st release.Status
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &st))
	require.False(t, st.TrackerPresent)
	require.Nil(t, st.UnreleasedEpics, "null, not empty — there was nothing to enumerate from")
}

func TestReleaseStatus_NoSliceDeclared(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeReleaseTracker(t, root, "development_status:\n  epic-1: done\n  1-1_a: done\n  epic-2: backlog\n  2-1_b: backlog\n")

	out := runCmd(t, newReleaseStatusCmd())
	require.Contains(t, out, "no release slice declared")
	require.Contains(t, out, "1,2")
}

func TestReleaseStatus_SliceFlag(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeReleaseTracker(t, root, releaseTracker)

	out := runCmd(t, newReleaseStatusCmd(), "--slice", "v1.0.0", "--output-format", "json")
	var st release.Status
	require.NoError(t, json.Unmarshal([]byte(out), &st))
	require.Len(t, st.Slices, 1)
	require.Equal(t, "v1.0.0", st.Slices[0].ID)
	require.Equal(t, []int{1, 2}, st.UnreleasedEpics,
		"--slice narrows the view, never the derived sets")
}

func TestReleaseStatus_SliceFlagUnknownIDNamesTheDeclaredOnes(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeReleaseTracker(t, root, releaseTracker)

	cmd := newReleaseStatusCmd()
	cmd.SetArgs([]string{"--slice", "v9.9.9"})
	cmd.SetOut(&strings.Builder{})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "v1.0.0, v1.1.0")
}

func TestReleaseCmd_Surface(t *testing.T) {
	cmd := newReleaseCmd()
	names := map[string]bool{}
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	require.True(t, names["status"], "ape release status missing")
}
