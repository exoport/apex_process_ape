package apexv1

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// liftWithCandidates lifts a fixture whose CLAUDE.md lists n binding-rule
// candidates of about width bytes each, beside 60 code files still
// naming a removed path in a comment, and returns the disposition file and how many
// candidates the lift found (the fixture's own, and these).
func liftWithCandidates(t *testing.T, n, width int) (disposition string, candidates int) {
	t.Helper()
	return liftWithCandidatesAnd(t, n, width, nil)
}

// liftWithCandidatesAnd is liftWithCandidates with extra files.
func liftWithCandidatesAnd(t *testing.T, n, width int, extra map[string]string) (disposition string, candidates int) {
	t.Helper()
	root := liftFixture(t, false)
	files := map[string]string{}
	maps.Copy(files, extra)
	for i := range 60 {
		files[fmt.Sprintf("tools/verify/check%02d.sh", i)] = "#!/bin/sh\n# was: development/implementation/sprint-status.yaml\necho ok\n"
	}
	claude, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	require.NoError(t, err)
	var rules strings.Builder
	for i := range n {
		rule := fmt.Sprintf("Rule %d: never write to the shared ledger outside a reviewed change", i)
		rule += strings.Repeat(" and say why", max(0, width-len(rule))/len(" and say why"))
		fmt.Fprintf(&rules, "- %s.\n", rule)
	}
	files["CLAUDE.md"] = string(claude) + "\n## More rules\n\n" + rules.String()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "rules")
	run(t, root, "tag", "-a", LiftTag, "-m", "final")
	run(t, root, "push", "-q", "origin", LiftTag)
	res, err := Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(res.Candidates), n)
	disp, err := os.ReadFile(filepath.Join(root, "development", LiftDisposition))
	require.NoError(t, err)
	return string(disp), len(res.Candidates)
}

// R14, from cortex on rc.4: 78 candidates (about 19 KB of table), of
// which the file listed 10 — the other 68 only in the checkout-local
// JSON, out of reach of the fresh reviewer and of apex-plan anywhere else.
// Every candidate is in the file now: the summaries go down to counts
// first, and the file grows past 16 KB for the candidates, within 48 KB.
func TestLift_DispositionKeepsEveryCandidate(t *testing.T) {
	d, n := liftWithCandidates(t, 78, 220)
	require.Greater(t, len(d), liftDispositionCap, "the candidates alone overflow 16 KB")
	require.LessOrEqual(t, len(d), liftDispositionCeiling)
	for i := range 78 {
		require.Contains(t, d, fmt.Sprintf("| Rule %d: never write", i))
	}
	require.Contains(t, d, fmt.Sprintf("| %d | ", n), "the last candidate too")
	require.NotContains(t, d, "more candidate(s)")
	require.Contains(t, d, "- … and 60 more file(s) (in `_apex/state/lift-disposition.json`)", "the summaries are cut to counts first; .github/ci.yml is a v0 state reader, in its own section")
	require.NotContains(t, d, "check00.sh")
}

// R14: past the 48 KB ceiling, the candidates are cut, the file saying
// how many and where they are.
func TestLift_DispositionCutsCandidatesAtTheCeiling(t *testing.T) {
	d, n := liftWithCandidates(t, 400, 220)
	require.LessOrEqual(t, len(d), liftDispositionCeiling)
	require.Greater(t, len(d), liftDispositionCeiling-1024, "it fills the ceiling before it cuts")
	m := regexp.MustCompile(`… and (\d+) more candidate\(s\), numbered on from (\d+), past this file's 48 KB ceiling: in ` + "`_apex/state/lift-disposition.json`" + ` \(rule_candidates\), in the checkout that lifted`).FindStringSubmatch(d)
	require.NotNil(t, m, "the cut says how many and where")
	var more, from int
	_, err := fmt.Sscan(m[1]+" "+m[2], &more, &from)
	require.NoError(t, err)
	require.Contains(t, d, fmt.Sprintf("| %d | ", from-1), "numbered up to the cut")
	require.NotContains(t, d, fmt.Sprintf("| %d | ", from))
	require.Equal(t, from-1+more, n, "every candidate is in the file or counted")
}

// The ceiling is the caps' hard ceiling on AGENTS.md (caps.go).
func TestLiftDispositionCeiling_IsTheCapsCeiling(t *testing.T) {
	for _, c := range capDefs {
		if c.key == CapAgentsMD {
			require.Equal(t, liftDispositionCeiling, c.ceiling)
		}
	}
}

// rg4ProjectContext is a v0 project-context.md shaped like the
// rehearsals' (RG-4): front matter, a title and its preamble, a pure
// overview, rule sections nested two deep, gates, a table and code.
const rg4ProjectContext = `---
project_name: 'rg4'
sections_completed: ['overview', 'technology_stack']
---

# Project Context for AI Agents

_This file holds the rules that agents must follow here._

## Overview

- Single Go module with three binaries and no daemon.
- Every overview line must stay out of the candidates.

### Layout

- Never a candidate either: a subsection of the overview.

## Technology Stack & Versions

- Go toolchain pinned exactly by go.mod's toolchain line.

## Critical Implementation Rules

### Testing Rules

- Integration tests run against a real engine,
  never a mock.
* Plain prose paragraphs are not rules
  unless they bind.

A shared fixture is read-only: never write to it
from a test, and always copy it into t.TempDir().

The suite takes about two minutes on this host.

| Rule | Why |
|---|---|
| never skip | flaky |

` + "```sh" + `
- never parsed: inside code
` + "```" + `

## Code Validation

` + "```sh" + `
make test
` + "```" + `

Never pass a positional argument to the guard loop.
`

const rg4TeamMemory = `# Team Memory

Durable lessons. Each entry is a rule worth carrying.

## Process & Workflow Rules

### Never invent an undesigned artifact — 2026-07-12

- If an AC asserts a mechanism nobody designed, refuse to fabricate it.
- Source: Epic 34–36 retros.

### Story amend on live change — 2026-10-14

When a live run overturns a story's Task, the dev pass routes the change to apex-story-amend in the same cycle.

Source: Epic 39 retrospective

### Error handling — 2026-08-01

Source: Epic 1 retrospective

## Known Repo State

- The audit gate exits 1 until the signal package has tests.

## Overview

- An overview line in team memory is skipped too.
`

// TestRuleCandidates_ProjectContext: RG-4 — every section but a pure
// overview gives its list items and its rule-shaped paragraphs, each with
// its heading path; front matter, the title, tables and code give none.
func TestRuleCandidates_ProjectContext(t *testing.T) {
	type row struct{ text, source, section string }
	cs := ruleCandidates(rg4ProjectContext, false)
	got := make([]row, 0, len(cs))
	for _, c := range cs {
		got = append(got, row{c.Text, c.Source, c.Section})
	}
	require.Equal(t, []row{
		{"_This file holds the rules that agents must follow here._", "8", ""},
		{"Go toolchain pinned exactly by go.mod's toolchain line.", "21", "Technology Stack & Versions"},
		{"Integration tests run against a real engine, never a mock.", "27", "Critical Implementation Rules > Testing Rules"},
		{"Plain prose paragraphs are not rules unless they bind.", "29", "Critical Implementation Rules > Testing Rules"},
		{"A shared fixture is read-only: never write to it from a test, and always copy it into t.TempDir().", "32", "Critical Implementation Rules > Testing Rules"},
		{"Never pass a positional argument to the guard loop.", "51", "Code Validation"},
	}, got)
}

// TestRuleCandidates_TeamMemory: RG-4 + D257 — every entry's body gives
// candidates (not only a gotcha's title), its "Source:" line none; an
// entry whose body has no rule-shaped text is listed by its paragraphs,
// and one with nothing but its source by its title.
func TestRuleCandidates_TeamMemory(t *testing.T) {
	type row struct{ text, source, section string }
	cs := ruleCandidates(rg4TeamMemory, true)
	got := make([]row, 0, len(cs))
	for _, c := range cs {
		got = append(got, row{c.Text, c.Source, c.Section})
	}
	require.Equal(t, []row{
		{"If an AC asserts a mechanism nobody designed, refuse to fabricate it.", "9", "Process & Workflow Rules > Never invent an undesigned artifact"},
		{"When a live run overturns a story's Task, the dev pass routes the change to apex-story-amend in the same cycle.", "14", "Process & Workflow Rules > Story amend on live change"},
		{"Error handling", "18", "Process & Workflow Rules"},
		{"The audit gate exits 1 until the signal package has tests.", "24", "Known Repo State"},
	}, got)
}

func TestIsOverview(t *testing.T) {
	for _, h := range []string{"Overview", "Project Overview", "1. Introduction", "**About**", "Purpose:", "Summary", "Table of Contents"} {
		require.True(t, isOverview(h), h)
	}
	for _, h := range []string{"Overview of the testing rules", "Technology Stack & Versions", "Critical Don't-Miss Rules", "Summary of gotchas"} {
		require.False(t, isOverview(h), h)
	}
}

// TestLift_CandidatesFromEverySection: RG-4 (D252, D257), from the radix
// rehearsal — the candidates came only from CLAUDE.md, project-context's
// "don't miss" section and team-memory gotcha titles, and every other
// rule section was deleted with its file, recorded nowhere. Every section
// but a pure overview gives candidates now, grouped in the .md by source
// and section, with `section` in the JSON; text found twice is listed
// once with both sources. (Written against the .md and the JSON only, so
// it runs on the old code too, and fails there.)
func TestLift_CandidatesFromEverySection(t *testing.T) {
	root := liftFixture(t, false)
	claude, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	require.NoError(t, err)
	for rel, content := range map[string]string{
		"development/project-context.md": rg4ProjectContext + "\n## Commits\n\n- Commit messages follow Conventional Commits, always.\n",
		"development/team-memory.md":     rg4TeamMemory,
		"CLAUDE.md":                      string(claude),
	} {
		require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(content), 0o644))
	}
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "rules")
	run(t, root, "tag", "-a", LiftTag, "-m", "final")
	run(t, root, "push", "-q", "origin", LiftTag)
	_, err = Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
	require.NoError(t, err)
	disp, err := os.ReadFile(filepath.Join(root, "development", LiftDisposition))
	require.NoError(t, err)
	d := string(disp)
	pc := "apex-v0-final:development/project-context.md"
	for _, want := range []string{
		"### `CLAUDE.md`\n\n| # | Candidate | Line | Disposition |\n|---|---|---|---|\n| 1 | NEVER push a tag without the operator's explicit say-so | `5` | |\n",
		"| 2 | Commit messages follow Conventional Commits, always | `6` · `" + pc + ":55` | |\n",
		"### `" + pc + "` — Technology Stack & Versions\n\n| # | Candidate | Line | Disposition |\n|---|---|---|---|\n| 4 | Go toolchain pinned exactly by go.mod's toolchain line. | `21` | |\n",
		"### `" + pc + "` — Critical Implementation Rules > Testing Rules\n",
		"| A shared fixture is read-only: never write to it from a test, and always copy it into t.TempDir(). | `32` | |",
		"### `apex-v0-final:development/team-memory.md` — Process & Workflow Rules > Never invent an undesigned artifact\n",
		"| If an AC asserts a mechanism nobody designed, refuse to fabricate it. | `9` | |",
		"| The audit gate exits 1 until the signal package has tests. | `24` | |",
	} {
		require.Contains(t, d, want)
	}
	for _, not := range []string{"overview", "Never a candidate", "two minutes", "never skip", "never parsed", "Source: Epic"} {
		require.NotContains(t, d, not)
	}
	require.Equal(t, 1, strings.Count(d, "Commit messages follow Conventional Commits"), "listed once")

	var report struct {
		Candidates []map[string]any `json:"rule_candidates"`
	}
	data, err := os.ReadFile(filepath.Join(root, "_apex", "state", LiftReport))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &report))
	require.Contains(t, report.Candidates, map[string]any{
		"text": "Integration tests run against a real engine, never a mock.", "source": pc + ":27",
		"section": "Critical Implementation Rules > Testing Rules",
	})
	require.Contains(t, report.Candidates, map[string]any{
		"text": "Commit messages follow Conventional Commits, always", "source": "CLAUDE.md:6", "also_in": []any{pc + ":55"},
	})
}

// Q136: past the ceiling, the overflow line says which sections went to
// the JSON only, the most-cut first, and the row's source cell is its line.
func TestCandidateOverflow_NamesTheCutSections(t *testing.T) {
	tm := "apex-v0-final:development/team-memory.md"
	cands := []LiftCandidate{
		{Text: "a", Source: tm + ":3", Section: "Gotchas"},
		{Text: "b", Source: tm + ":9", Section: "Process"},
		{Text: "c", Source: tm + ":12", Section: "Process"},
	}
	require.Equal(t, "By section: `"+tm+"` — Process (2); `"+tm+"` — Gotchas (1).", omittedGroups(cands))
	require.Equal(t, "12", candidateLine(tm+":12"))
	require.Equal(t, "CLAUDE.md", candidateLine("CLAUDE.md"))
	var b strings.Builder
	writeCandidates(&b, cands, 1, "_apex/state/lift-disposition.json")
	require.Contains(t, b.String(), "| 1 | a | `3` | |")
	require.Contains(t, b.String(), "… and 2 more candidate(s), numbered on from 2")
	require.Contains(t, b.String(), "By section: `"+tm+"` — Process (2).")
}
