package apexv1

import (
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

// H26: `ape next add` writes a dated line under the roadmap's Next on the
// base branch, from a change's branch too; the roadmap stays valid.
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
	require.Equal(t, "main", res.Branch)
	require.Equal(t, head, HeadSHA(ctx(), p.Root), "the change's branch did not move")
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
	require.Equal(t, "apex: next — the CSV export ignores --tz", run(t, p.Root, "log", "-1", "--format=%s", "main"))
	onMain := run(t, p.Root, "show", "main:development/roadmap.md")
	require.Contains(t, onMain, "## Next\n- 2026-10-16 the CSV export ignores --tz (from add-wave)")

	// The change does not catch up with a base moved by a record.
	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Nil(t, v.CatchUp)

	run(t, p.Root, "switch", "-q", "main")
	_, err = p.AddNext(ctx(), "a person's own note", "")
	require.NoError(t, err)
	require.Equal(t, "apex: next — a person's own note", subjects(t, p)[0])
	doc := read(t, p, "development/roadmap.md")
	require.Contains(t, doc, "(from add-wave)\n- 2026-10-16 a person's own note\n")
	require.Empty(t, findingMessages(ValidateRoadmap("development/roadmap.md", doc)), "## Next is not a milestone")
	require.Len(t, p.roadmap().Milestones, 1)
	clean(t, p, "next add")
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
// branch too, where the working tree's roadmap lags behind.
func TestStatusRoadmapNext(t *testing.T) {
	now = func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = time.Now })
	p := startedWave(t) // HEAD on change/add-wave
	require.Equal(t, []string{}, p.Status().RoadmapNext)
	_, err := p.AddNext(ctx(), "the CSV export ignores --tz", "add-wave")
	require.NoError(t, err)
	require.NotContains(t, read(t, p, "development/roadmap.md"), "--tz", "the change's branch has not caught up")
	require.Equal(t, []string{"2026-10-16 the CSV export ignores --tz (from add-wave)"}, p.Status().RoadmapNext)
}
