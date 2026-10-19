package apexv1

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAppendNextLines(t *testing.T) {
	l := []string{"- 2026-10-16 b (from add-x)"}
	cases := map[string]struct{ in, want string }{
		"section at the end": {
			"# Roadmap\n\n## M1 — A\nStatus: proposed\n\n## Next\n- 2026-10-01 a\n\n",
			"# Roadmap\n\n## M1 — A\nStatus: proposed\n\n## Next\n- 2026-10-01 a\n- 2026-10-16 b (from add-x)\n\n",
		},
		"section between milestones": {
			"# Roadmap\n## Next\n- a\n\n## M1 — A\nStatus: proposed\n",
			"# Roadmap\n## Next\n- a\n- 2026-10-16 b (from add-x)\n\n## M1 — A\nStatus: proposed\n",
		},
		"apex-plan's label": {
			"# Roadmap\nDelivery: x\nNext — not milestones:\n- a\n  more of a\n\n## M1 — A\n",
			"# Roadmap\nDelivery: x\nNext — not milestones:\n- a\n  more of a\n- 2026-10-16 b (from add-x)\n\n## M1 — A\n",
		},
		"none yet": {
			"# Roadmap\n\n## M1 — A\nStatus: proposed\n\n",
			"# Roadmap\n\n## M1 — A\nStatus: proposed\n\n## Next\n- 2026-10-16 b (from add-x)\n",
		},
		"a Next heading in code is not one": {
			"# Roadmap\n## M1 — A\n```\n## Next\n```\n",
			"# Roadmap\n## M1 — A\n```\n## Next\n```\n\n## Next\n- 2026-10-16 b (from add-x)\n",
		},
	}
	for name, c := range cases {
		require.Equal(t, c.want, string(AppendNextLines([]byte(c.in), l)), name)
	}
	require.Equal(t, "- 2026-10-16 x", NextLine("2026-10-16", " x ", ""))
}

// H26, D217: `ape next add` writes a dated line to next.md on the base
// branch, from a change's branch too — a project with neither next.md nor
// a roadmap Next block gets next.md and the roadmap's pointer line in the
// same commit; the roadmap stays valid.
func TestAddNext(t *testing.T) {
	now = func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = time.Now })
	p := startedWave(t) // HEAD on change/add-wave
	head := HeadSHA(ctx(), p.Root)
	_, err := p.AddNext(ctx(), "two\nlines", "add-wave")
	require.ErrorContains(t, err, "one line of text")
	_, err = p.AddNext(ctx(), "x", "Not An Id")
	require.ErrorContains(t, err, "is not a change id")

	res, err := p.AddNext(ctx(), "the CSV export ignores --tz", "add-wave")
	require.NoError(t, err)
	require.Equal(t, "- 2026-10-16 the CSV export ignores --tz (from add-wave)", res.Line)
	require.Equal(t, "development/next.md", res.Path)
	require.Equal(t, "development/roadmap.md", res.Pointer)
	require.Equal(t, "main", res.Branch)
	require.Equal(t, head, HeadSHA(ctx(), p.Root), "the change's branch did not move")
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
	require.Equal(t, "apex: next — the CSV export ignores --tz", run(t, p.Root, "log", "-1", "--format=%s", "main"))
	require.Equal(t, nextMDSkeleton+"\n- 2026-10-16 the CSV export ignores --tz (from add-wave)\n", run(t, p.Root, "show", "main:development/next.md")+"\n")
	onMain := run(t, p.Root, "show", "main:development/roadmap.md")
	require.Contains(t, onMain, "# Roadmap\n\n"+NextPointer+"\n\n## M1 — Walking skeleton\n")
	require.NotContains(t, onMain, "--tz")

	// The change does not catch up with a base moved by a record: next.md
	// is one (recordsOnlyMoved).
	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Nil(t, v.CatchUp)

	run(t, p.Root, "switch", "-q", "main")
	res, err = p.AddNext(ctx(), "a person's own note", "")
	require.NoError(t, err)
	require.Empty(t, res.Pointer, "the roadmap has its pointer already")
	require.Equal(t, "apex: next — a person's own note", subjects(t, p)[0])
	require.Equal(t, []string{"development/next.md"}, strings.Fields(run(t, p.Root, "show", "--name-only", "--format=", "HEAD")))
	require.Contains(t, read(t, p, "development/next.md"), "(from add-wave)\n- 2026-10-16 a person's own note\n")
	doc := read(t, p, "development/roadmap.md")
	require.Empty(t, findingMessages(ValidateRoadmap("development/roadmap.md", doc)), "the pointer line keeps the roadmap valid")
	require.Len(t, p.roadmap().Milestones, 1)
	require.Empty(t, p.nextInlineNotices(), "no Next block is left in the roadmap")
	clean(t, p, "next add")
}

// D217 Q125: a project not yet moved — a roadmap Next block, no next.md —
// keeps getting its lines in the block; once next.md exists (a project
// with both), they go there, and status reads both, each item once.
func TestAddNext_BeforeTheMove(t *testing.T) {
	now = func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = time.Now })
	files := baseFiles()
	files["development/roadmap.md"] = roadmapApproved + "\n## Next\n- 2026-10-01 a\n"
	p := fixture(t, files)
	res, err := p.AddNext(ctx(), "b", "")
	require.NoError(t, err)
	require.Equal(t, "development/roadmap.md", res.Path)
	require.Empty(t, res.Pointer)
	require.NoFileExists(t, filepath.Join(p.Root, "development", "next.md"))
	require.True(t, strings.HasSuffix(read(t, p, "development/roadmap.md"), "## Next\n- 2026-10-01 a\n- 2026-10-16 b\n"))

	write(t, p, "development/next.md", "# Next\n\n- 2026-10-01 a\n")
	commitAll(t, p, "both")
	res, err = p.AddNext(ctx(), "c", "")
	require.NoError(t, err)
	require.Equal(t, "development/next.md", res.Path)
	require.Empty(t, res.Pointer, "a roadmap with a Next block gets no pointer beside it")
	require.Equal(t, "# Next\n\n- 2026-10-01 a\n- 2026-10-16 c\n", read(t, p, "development/next.md"))
	require.NotContains(t, read(t, p, "development/roadmap.md"), "c\n")
	require.Equal(t, []string{"2026-10-01 a", "2026-10-16 c", "2026-10-16 b"}, p.Status().RoadmapNext)
	n := p.nextInlineNotices()
	require.Len(t, n, 1)
	require.Equal(t, CodeRoadmapNextInline, n[0].Code)
	require.Equal(t, 11, n[0].Line, "the ## Next heading")
	require.Contains(t, n[0].Message, "the roadmap still holds Next (2 item(s))")
	require.Equal(t, "`ape next move` moves it there in one commit, merged with what next.md holds (the framework's v1.0.0_seq-09 runs it)", n[0].Fix)
}

// D60: status reads the roadmap's Next back — the section's items and
// those under apex-plan's label, continuations joined, code skipped.
func TestNextItems(t *testing.T) {
	cases := map[string]struct {
		in   string
		want []string
	}{
		"none":    {"# Roadmap\n\n## M1 — A\nStatus: proposed\n", []string{}},
		"section": {"# Roadmap\n## M1 — A\n\n## Next\n- 2026-10-01 a\n\n* 2026-10-02 b (from add-x)\n", []string{"2026-10-01 a", "2026-10-02 b (from add-x)"}},
		"apex-plan's label": {
			"# Roadmap\nNext — not milestones:\n- a\n  more of a\n\nprose\n- not next\n## M1 — A\n- not next either\n",
			[]string{"a more of a"},
		},
		"label and section":                    {"# Roadmap\nNext:\n- a\n## M1 — A\n## Next\n- b\n", []string{"a", "b"}},
		"code is skipped":                      {"# Roadmap\n## Next\n```\n- not an item\n```\n- c\n## M1 — A\n- d\n", []string{"c"}},
		"a label below a milestone is not one": {"# Roadmap\n## M1 — A\nNext:\n- x\n", []string{}},
	}
	for name, c := range cases {
		require.Equal(t, c.want, NextItems(c.in), name)
	}
}

// D60: status --json lists the base branch's Next, from a change's
// branch too, where the working tree lags behind.
func TestStatusRoadmapNext(t *testing.T) {
	now = func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = time.Now })
	p := startedWave(t) // HEAD on change/add-wave
	require.Equal(t, []string{}, p.Status().RoadmapNext)
	_, err := p.AddNext(ctx(), "the CSV export ignores --tz", "add-wave")
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(p.Root, "development", "next.md"), "the change's branch has not caught up")
	require.Equal(t, []string{"2026-10-16 the CSV export ignores --tz (from add-wave)"}, p.Status().RoadmapNext)
}

// D217: next.md's items are every list item outside code, under any
// heading, continuations joined.
func TestNextFileItems(t *testing.T) {
	require.Equal(t, []string{}, NextFileItems(nextMDSkeleton))
	require.Equal(t, []string{"2026-10-01 a more of a", "b", "c"},
		NextFileItems("# Next\n\nprose\n- 2026-10-01 a\n  more of a\n\n### Later\n* b\n```\n- not one\n```\n- c\n"))
}
