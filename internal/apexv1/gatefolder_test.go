package apexv1

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// gateFolderFiles is baseFiles with M1's gate in a gate folder, green,
// beside the shared lib/.
func gateFolderFiles() map[string]string {
	files := baseFiles()
	files["development/roadmap.md"] = strings.Replace(roadmapApproved, "Gate: ./gate.sh", "Gate: ./development/gates/M1-skeleton/gate.sh", 1)
	files["development/gates/M1-skeleton/gate.sh"] = "#!/bin/sh\n. development/gates/lib/common.sh\nsh development/gates/M1-skeleton/check.sh\n"
	files["development/gates/M1-skeleton/check.sh"] = "exit 0\n"
	files["development/gates/M1-skeleton/stub"] = "stub\n"
	files["development/gates/lib/common.sh"] = "true\n"
	return files
}

// D155 Q74: close moves the milestone's gate folder to
// archive/milestones/<M>/gate/ in the close commit, as a rename, records
// the move, and the approval's hashes verify at the mapped paths; lib/
// stays.
func TestClose_ArchivesTheGateFolder(t *testing.T) {
	p := fixture(t, gateFolderFiles())
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	approved := recs["M1"].Approval.GateFiles
	require.Contains(t, approved, "development/gates/M1-skeleton/gate.sh")

	g, err := p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, MilestoneDone, g.Status)
	require.Equal(t, "apex: M1 done", subjects(t, p)[0])
	clean(t, p, "milestone close")
	want := []GateArchive{{From: "development/gates/M1-skeleton", To: "development/archive/milestones/M1/gate"}}
	require.Equal(t, want, g.GateArchived)

	// One commit: the done line, the record and the move, as renames.
	moves := run(t, p.Root, "show", "-M", "--name-status", "--format=", "HEAD")
	for _, f := range []string{"gate.sh", "check.sh", "stub"} {
		require.Contains(t, moves, "R100\tdevelopment/gates/M1-skeleton/"+f+"\tdevelopment/archive/milestones/M1/gate/"+f)
	}
	require.Contains(t, moves, "development/roadmap.md")
	require.Contains(t, moves, "_apex/state/milestones.json")
	require.NoDirExists(t, filepath.Join(p.Root, "development", "gates", "M1-skeleton"))
	require.FileExists(t, filepath.Join(p.Root, "development", "gates", "lib", "common.sh"), "lib/ is no milestone's")

	// The record keeps the approved paths; mapped, the same SHA-256.
	recs, err = p.LoadMilestoneRecords()
	require.NoError(t, err)
	rec := recs["M1"]
	require.Equal(t, want, rec.GateArchived)
	require.Equal(t, approved, rec.Approval.GateFiles, "gate_files keep the paths as approved")
	require.Equal(t, "development/archive/milestones/M1/gate/gate.sh", rec.ArchivedPath("development/gates/M1-skeleton/gate.sh"))
	require.Equal(t, "development/gates/M1-skeletonx/gate.sh", rec.ArchivedPath("development/gates/M1-skeletonx/gate.sh"), "a prefix maps by folder, not by string")
	mapped := rec.ArchivedGateFiles()
	require.Len(t, mapped, len(approved))
	for path, sum := range mapped {
		got, ok := p.fileSHA256(path)
		require.True(t, ok, path)
		require.Equal(t, sum, got, path)
	}
	require.Empty(t, p.Check())
}

// Several gate folders go to gate/<folder>/ each; M10's folder is not
// M1's.
func TestClose_ArchivesSeveralGateFolders(t *testing.T) {
	files := gateFolderFiles()
	files["development/gates/M1-extra/check.sh"] = "exit 0\n"
	files["development/gates/M10-later/gate.sh"] = "exit 1\n"
	p := fixture(t, files)
	g, err := p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, []GateArchive{
		{From: "development/gates/M1-extra", To: "development/archive/milestones/M1/gate/M1-extra"},
		{From: "development/gates/M1-skeleton", To: "development/archive/milestones/M1/gate/M1-skeleton"},
	}, g.GateArchived)
	clean(t, p, "milestone close")
	require.FileExists(t, filepath.Join(p.Root, "development", "archive", "milestones", "M1", "gate", "M1-extra", "check.sh"))
	require.FileExists(t, filepath.Join(p.Root, "development", "archive", "milestones", "M1", "gate", "M1-skeleton", "gate.sh"))
	require.FileExists(t, filepath.Join(p.Root, "development", "gates", "M10-later", "gate.sh"))
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Equal(t, "development/archive/milestones/M1/gate/M1-skeleton/gate.sh", recs["M1"].ArchivedPath("development/gates/M1-skeleton/gate.sh"))
}

// The accept-red close moves the gate folders too; a gate outside
// development/gates/ stays where it is (framework Q83).
func TestClose_AcceptRedArchivesTheGateFolder(t *testing.T) {
	files := baseFiles()
	files["development/roadmap.md"] = strings.Replace(roadmapApproved, "Gate: ./gate.sh", "Gate: ./scripts/gate.sh", 1)
	files["scripts/gate.sh"] = "#!/bin/sh\nexit 1\n"
	files["development/gates/M1-skeleton/check.sh"] = "exit 1\n"
	p := fixture(t, files)
	g, err := p.CloseMilestoneAcceptRed(ctx(), "M1", "known defect", "ann", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "apex: M1 done (accepted red)", subjects(t, p)[0])
	require.Equal(t, []GateArchive{{From: "development/gates/M1-skeleton", To: "development/archive/milestones/M1/gate"}}, g.GateArchived)
	require.Contains(t, run(t, p.Root, "show", "-M", "--name-status", "--format=", "HEAD"),
		"R100\tdevelopment/gates/M1-skeleton/check.sh\tdevelopment/archive/milestones/M1/gate/check.sh")
	require.FileExists(t, filepath.Join(p.Root, "scripts", "gate.sh"), "a gate outside development/gates/ stays")
	clean(t, p, "close --accept-red")
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Equal(t, "scripts/gate.sh", recs["M1"].ArchivedPath("scripts/gate.sh"))
}

// A milestone without a gate folder closes as before: nothing moved, no
// gate_archived.
func TestClose_NoGateFolder(t *testing.T) {
	files := baseFiles()
	files["gate.sh"] = "#!/bin/sh\nexit 0\n"
	p := fixture(t, files)
	g, err := p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Empty(t, g.GateArchived)
	require.NotContains(t, read(t, p, "_apex/state/milestones.json"), "gate_archived")
}

// An archive/milestones/<M>/gate/ already there refuses the close before
// the gate runs: nothing written.
func TestClose_GateArchiveExists(t *testing.T) {
	files := gateFolderFiles()
	files["development/archive/milestones/M1/gate/old.sh"] = "exit 0\n"
	p := fixture(t, files)
	head := HeadSHA(ctx(), p.Root)
	_, err := p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.Equal(t, CodeGateArchiveExists, failCode(t, err))
	require.Contains(t, err.Error(), "development/archive/milestones/M1/gate/ exists already")
	require.Equal(t, head, HeadSHA(ctx(), p.Root))
	clean(t, p, "a refused close")
	require.Nil(t, p.LastGateRun("M1"), "the gate did not run")
	require.Equal(t, MilestoneApproved, p.roadmap().Find("M1").Status)
}

// D155 Q73: an open milestone whose gate runs a script outside
// development/gates/ is a notice, never a finding; a bare command, a gate
// in its folder and a done milestone are none.
func TestNotices_GateOutsideGates(t *testing.T) {
	files := baseFiles()
	files["development/roadmap.md"] = `# Roadmap

## M1 — Walking skeleton
Status: done (2026-10-15T12:00:00Z 0000000)
Gate: ./scripts/m1.sh
Goal: Done.

## M2 — Second
Status: approved (by eval 2026-10-15T12:00:00Z)
Gate: ./scripts/gate.sh && bash scripts/more.sh && ./development/gates/M2-second/gate.sh
Goal: Two.

## M3 — Third
Status: proposed
Gate: bash scripts/third.sh
Goal: Three.

## M4 — Fourth
Status: proposed
Gate: make e2e && ./scripts/not-written-yet.sh
Goal: Four.

## M5 — Fifth
Status: proposed
Gate: ./development/gates/M5-fifth/gate.sh
Goal: Five.
`
	for _, s := range []string{"scripts/m1.sh", "scripts/gate.sh", "scripts/more.sh", "development/gates/M2-second/gate.sh", "scripts/third.sh", "development/gates/M5-fifth/gate.sh"} {
		files[s] = "#!/bin/sh\nexit 1\n"
	}
	files["_apex/state/milestones.json"] = `{"M1": {"result": "green", "done_at": "2026-10-15T12:00:00Z"}}`
	p := fixture(t, files)
	recordApprovals(t, p)
	commitAll(t, p, "approvals")

	ns := p.Notices()
	require.Len(t, ns, 2)
	require.Equal(t, Diagnostic{
		Severity: SeverityNotice, Code: CodeGateOutsideGates, Action: ActionPerson, File: "development/roadmap.md", Line: 8,
		Message: "M2 gate runs scripts outside development/gates/: scripts/gate.sh, scripts/more.sh",
		Fix:     "move them into development/gates/M2-<slug>/ with the milestone's next re-approval (moving an approved gate's script is gate drift)",
	}, ns[0])
	require.Equal(t, "M3 gate runs scripts outside development/gates/: scripts/third.sh", ns[1].Message)
	require.Equal(t, "move them into development/gates/M3-<slug>/ before it is approved", ns[1].Fix)
	require.NotContains(t, findingMessages(p.Check()), "outside", "a notice is not a finding")
}
