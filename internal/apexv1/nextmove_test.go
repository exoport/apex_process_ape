package apexv1

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// D217 Q126: the move, as text — apex-plan's label form replaced in place
// by the pointer, a `## Next` section removed wherever it is and the
// pointer put above the first milestone, the items carried byte for byte
// in order, each once, a roadmap with the pointer given no second one.
func TestPlanNextMove(t *testing.T) {
	cases := map[string]struct {
		roadmap  string
		next     *string
		wantRM   string
		wantNext string
		moved    []string
		dups     []string
	}{
		"apex-plan's label": {
			roadmap:  "# Roadmap\n\nDelivery: x\n\nNext — not milestones:\n- A web interface — a later product.\n- 2026-10-01 b (from add-x)\n  more of b\n\n## M1 — A\nStatus: proposed\n",
			wantRM:   "# Roadmap\n\nDelivery: x\n\n" + NextPointer + "\n\n## M1 — A\nStatus: proposed\n",
			wantNext: nextMDSkeleton + "\n- A web interface — a later product.\n- 2026-10-01 b (from add-x)\n  more of b\n",
			moved:    []string{"A web interface — a later product.", "2026-10-01 b (from add-x) more of b"},
		},
		"a label right under the title is replaced where it is": {
			roadmap:  "# Roadmap\nNext:\n* a\n## M1 — A\n",
			wantRM:   "# Roadmap\n" + NextPointer + "\n## M1 — A\n",
			wantNext: nextMDSkeleton + "\n* a\n",
			moved:    []string{"a"},
		},
		"## Next after the milestones, with prose": {
			roadmap:  "# Roadmap\n\n## M1 — A\nStatus: proposed\n\n## Next\nFindings to triage.\n\n- 2026-10-01 a\n* 2026-10-02 b\n\n",
			wantRM:   "# Roadmap\n\n" + NextPointer + "\n\n## M1 — A\nStatus: proposed\n",
			wantNext: nextMDSkeleton + "\nFindings to triage.\n\n- 2026-10-01 a\n* 2026-10-02 b\n",
			moved:    []string{"2026-10-01 a", "2026-10-02 b"},
		},
		"## Next above the first milestone": {
			roadmap:  "# Roadmap\n\n## Next\n- a\n\n## M1 — A\n",
			wantRM:   "# Roadmap\n\n" + NextPointer + "\n\n## M1 — A\n",
			wantNext: nextMDSkeleton + "\n- a\n",
			moved:    []string{"a"},
		},
		"## Next between milestones": {
			roadmap:  "# Roadmap\n\n## M1 — A\n\n## Next\n- a\n\n## M2 — B\n",
			wantRM:   "# Roadmap\n\n" + NextPointer + "\n\n## M1 — A\n\n## M2 — B\n",
			wantNext: nextMDSkeleton + "\n- a\n",
			moved:    []string{"a"},
		},
		"label and section, merged into next.md, each item once": {
			roadmap:  "# Roadmap\nNext — not milestones:\n- a\n- b\n\n## M1 — A\n\n## Next\n- b\n- c\n```\n- code, carried\n```\n",
			next:     new("# Next\n\n- 2026-10-01 x\n- a\n"),
			wantRM:   "# Roadmap\n" + NextPointer + "\n\n## M1 — A\n",
			wantNext: "# Next\n\n- 2026-10-01 x\n- a\n- b\n- c\n```\n- code, carried\n```\n",
			moved:    []string{"b", "c"},
			dups:     []string{"a", "b"},
		},
		"no Next at all: next.md and the pointer": {
			roadmap:  "# Roadmap\n\n## M1 — A\n",
			wantRM:   "# Roadmap\n\n" + NextPointer + "\n\n## M1 — A\n",
			wantNext: nextMDSkeleton,
		},
		"no milestone yet": {
			roadmap:  "# Roadmap\n",
			wantRM:   "# Roadmap\n\n" + NextPointer + "\n",
			wantNext: nextMDSkeleton,
		},
		"the pointer is there: no second one": {
			roadmap:  "# Roadmap\n\n" + NextPointer + "\n\n## M1 — A\n\n## Next\n- a\n",
			next:     new(nextMDSkeleton),
			wantRM:   "# Roadmap\n\n" + NextPointer + "\n\n## M1 — A\n",
			wantNext: nextMDSkeleton + "\n- a\n",
			moved:    []string{"a"},
		},
		"a Next heading in code is not one": {
			roadmap:  "# Roadmap\n\n## M1 — A\n```\n## Next\n- x\n```\n",
			wantRM:   "# Roadmap\n\n" + NextPointer + "\n\n## M1 — A\n```\n## Next\n- x\n```\n",
			wantNext: nextMDSkeleton,
		},
	}
	for name, c := range cases {
		var next []byte
		if c.next != nil {
			next = []byte(*c.next)
		}
		res := &NextMoveResult{Moved: []string{}, Duplicates: []string{}}
		rm, nx := planNextMove(c.roadmap, next, res)
		require.Equal(t, c.wantRM, rm, name)
		require.Equal(t, c.wantNext, nx, name)
		require.Equal(t, append([]string{}, c.moved...), res.Moved, name)
		require.Equal(t, append([]string{}, c.dups...), res.Duplicates, name)
		require.True(t, res.Pending, name)
		require.Equal(t, []string{}, NextItems(rm), name+": no Next is left in the roadmap")

		// Moved once, there is nothing left to move.
		again := &NextMoveResult{Moved: []string{}, Duplicates: []string{}}
		rm2, nx2 := planNextMove(rm, []byte(nx), again)
		require.False(t, again.Pending, name)
		require.Equal(t, rm, rm2, name)
		require.Equal(t, nx, nx2, name)
		require.Empty(t, findingMessages(ValidateNextMD("development/next.md", nx)), name)
	}
}

// D217 Q126: `ape next move` on a project made before D217 — apex-plan's
// label form — in one commit; idempotent; --check before and after; the
// project check's notice gone with the block.
func TestNextMove(t *testing.T) {
	files := baseFiles()
	files["development/roadmap.md"] = strings.Replace(roadmapApproved, "# Roadmap\n\n", "# Roadmap\n\nNext — not milestones:\n- A web interface — a later product.\n- 2026-10-01 b (from add-x)\n\n", 1)
	p := fixture(t, files)
	require.Len(t, p.nextInlineNotices(), 1)
	require.Empty(t, findingMessages(p.Check()), "a roadmap holding Next is no finding")

	res, err := p.NextMoveCheck()
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodeNextMovePending, fe.Code)
	require.Equal(t, ActionApply, ActionFor(fe.Code))
	require.Contains(t, fe.Msg, "development/roadmap.md holds 2 Next item(s), development/next.md does not exist, development/roadmap.md has no line pointing at it")
	require.True(t, res.Pending)
	require.NoFileExists(t, filepath.Join(p.Root, "development", "next.md"), "--check writes nothing")

	head := HeadSHA(ctx(), p.Root)
	res, err = p.NextMove(ctx())
	require.NoError(t, err)
	require.Equal(t, []string{"A web interface — a later product.", "2026-10-01 b (from add-x)"}, res.Moved)
	require.True(t, res.Created)
	require.True(t, res.PointerAdded)
	require.Equal(t, HeadSHA(ctx(), p.Root), res.Commit)
	require.Equal(t, head, run(t, p.Root, "rev-parse", "HEAD~1"), "one commit")
	require.Equal(t, "apex: Next moved to development/next.md (seq-09)", subjects(t, p)[0])
	require.Equal(t, roadmapApproved[:len("# Roadmap\n\n")]+NextPointer+"\n\n"+roadmapApproved[len("# Roadmap\n\n"):], read(t, p, "development/roadmap.md"))
	require.Equal(t, nextMDSkeleton+"\n- A web interface — a later product.\n- 2026-10-01 b (from add-x)\n", read(t, p, "development/next.md"))
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
	require.Equal(t, []string{"A web interface — a later product.", "2026-10-01 b (from add-x)"}, p.Status().RoadmapNext, "status reads the same items")
	require.Empty(t, p.nextInlineNotices())
	require.Empty(t, findingMessages(p.Check()))

	res, err = p.NextMoveCheck()
	require.NoError(t, err)
	require.False(t, res.Pending)
	res, err = p.NextMove(ctx())
	require.NoError(t, err)
	require.False(t, res.Pending)
	require.Empty(t, res.Commit, "idempotent: nothing written")
	require.Equal(t, "apex: Next moved to development/next.md (seq-09)", subjects(t, p)[0])
}

// D217 Q126: the move is refused on a change branch and while either file
// has uncommitted changes, writing nothing.
func TestNextMove_Refused(t *testing.T) {
	p := startedWave(t) // HEAD on change/add-wave
	_, err := p.NextMove(ctx())
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodeChangeBranch, fe.Code)

	run(t, p.Root, "switch", "-q", "main")
	write(t, p, "development/roadmap.md", read(t, p, "development/roadmap.md")+"\n## Next\n- a person's, uncommitted\n")
	_, err = p.NextMove(ctx())
	fe, ok = errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "tree.dirty", fe.Code)
	require.NoFileExists(t, filepath.Join(p.Root, "development", "next.md"))
	require.Contains(t, read(t, p, "development/roadmap.md"), "## Next\n- a person's, uncommitted\n")
}

// D217 Q127: next.md is checked for its items' shape only; an undated
// item, as apex-plan wrote them, is accepted.
func TestValidateNextMD(t *testing.T) {
	doc := "# Next\n\nprose: 2026-99-99 is not an item\n\n- 2026-10-16 good (from add-x)\n- an undated line\n" +
		"- 2026-13-01 bad month\n- 2026-1-5 short\n- 2026-10-16\n- 2026-10-16 x (from Not An Id)\n  - 2026-99-99 nested\n```\n- 2026-99-99 code\n```\n"
	fs := ClassifyFindings(ValidateNextMD("development/next.md", doc))
	require.Len(t, fs, 4)
	for _, f := range fs {
		require.Equal(t, "next.bad-item", f.Code, f.Message)
	}
	require.Equal(t, 7, fs[0].Line)
	require.Contains(t, fs[0].Message, "its date 2026-13-01 is not a real YYYY-MM-DD")
	require.Contains(t, fs[1].Message, "its date 2026-1-5 is not a real YYYY-MM-DD")
	require.Contains(t, fs[2].Message, "it has no text after its date")
	require.Contains(t, fs[3].Message, "(from Not An Id) names no change id")

	files := baseFiles()
	files["development/next.md"] = "# Next\n\n- 2026-02-30 no such day\n"
	p := fixture(t, files)
	fs = ClassifyFindings(p.Check())
	require.Len(t, fs, 1)
	require.Equal(t, "next.bad-item", fs[0].Code)
	require.Equal(t, "development/next.md", fs[0].File)
}
