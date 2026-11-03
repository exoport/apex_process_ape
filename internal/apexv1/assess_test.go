package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const roadmapTwoOpen = roadmapApproved + `
## M2 — Greeting checks
Status: approved (by eval 2026-10-15T12:00:00Z)
Gate: ./check-greet.sh
Goal: The greeting is checked.

## M3 — Farewells
Status: proposed
Gate: ./bye.sh
Goal: Say goodbye.
`

// assessFixture is a coordinated project whose main has two open
// milestones, M1 and M2, and a proposed M3. M2's gate runs check-greet.sh,
// which calls greet.sh; M1's change m1-edit, landed on milestone/M1, edits
// greet.sh: the planted overlap. main also holds what the folder must not
// pass on: a symbolic link out of the project, a hot/ file and a file
// named input.md. forged is a commit on no milestone branch.
func assessFixture(t *testing.T) (p *Project, landed, forged string) {
	t.Helper()
	files := baseFiles()
	files["_apex/config.yaml"] = coordinatedConfig
	files["development/roadmap.md"] = roadmapTwoOpen
	files["check-greet.sh"] = "#!/bin/sh\n./greet.sh Ann | grep -q 'Hello Ann'\n"
	files["hot/ape-nats.creds"] = "not a real credential\n"
	files["input.md"] = "the project's own input.md\n"
	p = fixture(t, files)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("OUTSIDE-SECRET\n"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(p.Root, "leak")))
	run(t, p.Root, "add", "-f", "leak", "hot/ape-nats.creds")
	run(t, p.Root, "commit", "-q", "-m", "chore: a link and a hot file on main")
	withOrigin(t, p)

	run(t, p.Root, "switch", "-q", "-c", "milestone/M2")
	write(t, p, "development/changes/m2-check/change.md", "---\nid: m2-check\nmilestone: M2\ntouches: [greeting]\npaths: [check-greet.sh]\n---\n# M2 check\n")
	commitAll(t, p, "plan: m2-check")
	run(t, p.Root, "push", "-q", "origin", "milestone/M2")

	run(t, p.Root, "switch", "-q", "main")
	run(t, p.Root, "switch", "-q", "-c", "milestone/M1")
	write(t, p, "development/changes/m1-edit/change.md", "---\nid: m1-edit\nmilestone: M1\ntouches: [greeting]\npaths: [greet.sh]\n---\n# M1 edit\n")
	commitAll(t, p, "plan: m1-edit")
	write(t, p, "greet.sh", "#!/bin/sh\necho Hi $1\n")
	commitAll(t, p, "feat: greet with Hi\n\nChange: m1-edit")
	landed = run(t, p.Root, "rev-parse", "HEAD")
	run(t, p.Root, "push", "-q", "origin", "milestone/M1")

	run(t, p.Root, "switch", "-q", "-c", "elsewhere", "main")
	write(t, p, "planted.txt", "FORGED-CONTENT\n")
	commitAll(t, p, "feat: not on any milestone branch\n\nChange: m1-edit")
	forged = run(t, p.Root, "rev-parse", "HEAD")
	run(t, p.Root, "switch", "-q", "main")
	return p, landed, forged
}

// PLAN-V15-ASSESSOR step 3 (D1, R1): the batch folder is main's files and
// input.md — two open milestones' plans, their gate paths and changes, and
// the archived change's diff, read from the commit on milestone/M1. The
// controls: an event naming a commit that is not on milestone/<M> gets no
// diff, and the input says so (that commit's content appears nowhere); a
// symbolic link, hot/ and .git are not in the folder, nor is main's own
// input.md; a proposed milestone is not open.
func TestAssessFolder_PlansDiffsAndWhatItLeavesOut(t *testing.T) {
	p, landed, forged := assessFixture(t)
	dir := filepath.Join(t.TempDir(), "batch")
	f, err := p.BuildAssessFolder(ctx(), dir, AssessRequest{
		Batch: "20261103T020000Z-1", Project: "notes",
		Events: []AssessEvent{
			{MsgID: "notes/ck/change-archived/M1/m1-edit", Event: EventChangeArchived, Milestone: "M1", Change: "m1-edit", Commit: landed, Paths: []string{"greet.sh"}},
			{MsgID: "notes/ck/change-archived/M1/m1-forged", Event: EventChangeArchived, Milestone: "M1", Change: "m1-forged", Commit: forged, Paths: []string{"planted.txt"}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"M1", "M2"}, f.Open, "M3 is proposed, not open")
	require.Equal(t, run(t, p.Root, "rev-parse", "origin/main"), f.Main)

	require.FileExists(t, filepath.Join(dir, "check-greet.sh"))
	require.FileExists(t, filepath.Join(dir, "development", "roadmap.md"))
	for _, absent := range []string{".git", "hot", "leak"} {
		_, err := os.Lstat(filepath.Join(dir, absent))
		require.ErrorIs(t, err, os.ErrNotExist, absent+" must not be in the folder")
	}
	require.Contains(t, f.Dropped, "leak (a symbolic link)")
	require.Contains(t, f.Dropped, "hot/")
	require.Contains(t, f.Dropped, "input.md (the folder's own file)")

	in := read(t, &Project{Root: dir}, AssessInput)
	require.NotContains(t, in, "the project's own input.md")
	require.Contains(t, in, "### M2: Greeting checks")
	require.Contains(t, in, "- gate paths: `check-greet.sh`")
	require.Contains(t, in, "- change m2-check on `milestone/M2`: touches: `greeting`; paths: `check-greet.sh`")
	require.Contains(t, in, "- change m1-edit on `milestone/M1`: touches: `greeting`; paths: `greet.sh`")
	require.Contains(t, in, "Gate: ./check-greet.sh", "M2's roadmap section, verbatim")
	require.NotContains(t, in, "### M3:")

	require.Equal(t, landed, f.Diffs["m1-edit"])
	require.Contains(t, in, "- diff: under Diffs below, from "+short(landed)+" on `milestone/M1`")
	require.Contains(t, in, "+echo Hi $1", "the diff of the landed commit")

	require.Empty(t, f.Diffs["m1-forged"], "control: a commit not on milestone/M1 is never read")
	require.Contains(t, f.DiffNotes["m1-forged"], "the event's commit is not on `milestone/M1`, and was not read")
	require.Contains(t, in, "- diff: not read: the event's commit is not on `milestone/M1`, and was not read")
	require.NotContains(t, in, "FORGED-CONTENT")
	require.NotContains(t, in, "OUTSIDE-SECRET")
	t.Logf("input.md, %d bytes:\n%s", len(in), in)
}

// D1 again: an event naming a forged commit for a change that did land
// gets the diff of the commit on milestone/<M> whose Change: trailer names
// it — never the forged one, though that carries the same trailer. And the
// caps cut a diff and input.md, saying so.
func TestAssessFolder_ForgedCommitAndCaps(t *testing.T) {
	p, landed, forged := assessFixture(t)
	dir := filepath.Join(t.TempDir(), "batch")
	f, err := p.BuildAssessFolder(ctx(), dir, AssessRequest{
		Batch: "b-2", Project: "notes", DiffCap: 40,
		Events: []AssessEvent{{MsgID: "x", Event: EventChangeArchived, Milestone: "M1", Change: "m1-edit", Commit: forged}},
	})
	require.NoError(t, err)
	require.Equal(t, landed, f.Diffs["m1-edit"])
	in := read(t, &Project{Root: dir}, AssessInput)
	require.NotContains(t, in, "FORGED-CONTENT")
	require.Contains(t, in, "whose Change: trailer names m1-edit")
	require.Contains(t, in, "[cut: the diff is ")
	require.True(t, f.Truncated)

	dir2 := filepath.Join(t.TempDir(), "batch")
	f2, err := p.BuildAssessFolder(ctx(), dir2, AssessRequest{Batch: "b-3", Project: "notes", InputCap: 300})
	require.NoError(t, err)
	in2 := read(t, &Project{Root: dir2}, AssessInput)
	require.True(t, f2.Truncated)
	require.True(t, strings.HasSuffix(in2, "the first 300 are shown]\n"))
}
