package apexv1

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

// D274 (operator, before promotion): past the 48 KB ceiling the candidates
// are no longer cut to the git-ignored JSON — apex-plan in any other clone
// lost them (cortex on rc7f: 271 of 384). Every row is in the file, past
// the ceiling; the other lists are already at their minimum (counts), and
// the count line names the last row and the file's size.
func TestLift_DispositionNeverCutsCandidatesPastTheCeiling(t *testing.T) {
	d, n := liftWithCandidates(t, 400, 220)
	require.Greater(t, len(d), liftDispositionCeiling, "400 candidates need more than 48 KB, and get it")
	for i := range 400 {
		require.Contains(t, d, fmt.Sprintf("| Rule %d: never write", i))
	}
	require.Contains(t, d, fmt.Sprintf("| %d | ", n), "the last candidate too")
	require.NotContains(t, d, "more candidate")
	m := regexp.MustCompile(`This file lists every candidate: (\d+) rows, numbered 1–(\d+) \(about (\d+) KB\)\. If a Read stops short, read on with offset/limit until row (\d+)\.`).FindStringSubmatch(d)
	require.NotNil(t, m, "the count line")
	require.Equal(t, []string{strconv.Itoa(n), strconv.Itoa(n), strconv.Itoa(n)}, []string{m[1], m[2], m[4]})
	kb, err := strconv.Atoi(m[3])
	require.NoError(t, err)
	require.Equal(t, (len(d)+1023)>>10, kb, "the size it states is the file's own (no regression section here: --no-verify-run)")
	require.Contains(t, d, "- … and 60 more file(s) (in `_apex/state/lift-disposition.json`)", "the non-rule lists stay summarised")
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
// rehearsals' (RG-4): front matter, a title and its preamble, an
// overview with a binding rule and a table of contents (RG-4b), rule
// sections nested two deep, gates with notes in their fence (RG-4b), a
// table and code.
const rg4ProjectContext = `---
project_name: 'rg4'
sections_completed: ['overview', 'technology_stack']
---

# Project Context for AI Agents

_This file holds the rules that agents must follow here._

## Overview

- Single Go module with three binaries and no daemon.
- Never fold the release generator into the main binary.

### Layout

- [Testing Rules](#testing-rules) · [Code Validation](#code-validation)
- A subsection of the overview is read like any other.

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
#!/bin/sh
make test      # the full suite, adopted verbatim; do not narrow it
               # to the packages a change touched
make vet       # vet
# the guard loop takes its default roots, so
# it proves the rewiring
for g in tools/ci/*.sh; do "$g" || exit 1; done   # every guard, NO argument
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

- An overview line in team memory counts too (RG-4b).
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
		{"_This file holds the rules that agents must follow here._", "8", "Project Context for AI Agents"}, // E-R7f-1
		{"Single Go module with three binaries and no daemon.", "12", "Overview"},
		{"Never fold the release generator into the main binary.", "13", "Overview"},
		{"A subsection of the overview is read like any other.", "18", "Overview > Layout"},
		{"Go toolchain pinned exactly by go.mod's toolchain line.", "22", "Technology Stack & Versions"},
		{"Integration tests run against a real engine, never a mock.", "28", "Critical Implementation Rules > Testing Rules"},
		{"Plain prose paragraphs are not rules unless they bind.", "30", "Critical Implementation Rules > Testing Rules"},
		{"A shared fixture is read-only: never write to it from a test, and always copy it into t.TempDir().", "33", "Critical Implementation Rules > Testing Rules"},
		{"Never pass a positional argument to the guard loop.", "58", "Code Validation"},
	}, got)
}

// TestCodeValidationNotes: RG-4b (D262), from radix — the rule text
// inside `## Code Validation`'s fence ("do not reduce it", "make audit is
// NOT the route") was read by neither the gate extraction nor the prose
// walk. A command's comment is a note shown with its command, continued
// by deeper-indented comment lines; a run of comment-only lines is one
// note; a note under three words, the shebang and the commands give none.
func TestCodeValidationNotes(t *testing.T) {
	type row struct{ text, source, section string }
	cs := codeValidationNotes(rg4ProjectContext)
	got := make([]row, 0, len(cs))
	for _, c := range cs {
		got = append(got, row{c.Text, c.Source, c.Section})
	}
	require.Equal(t, []row{
		{"`make test` — the full suite, adopted verbatim; do not narrow it to the packages a change touched", "50", "Code Validation"},
		{"the guard loop takes its default roots, so it proves the rewiring", "53", "Code Validation"},
		{"`for g in tools/ci/*.sh; do \"$g\" || exit 1; done` — every guard, NO argument", "55", "Code Validation"},
	}, got)
	// The commands still go to AGENTS.md, and no note is one.
	require.Equal(t, []string{"( make test\nmake vet\n" + `for g in tools/ci/*.sh; do "$g" || exit 1; done` + "\n)"},
		extractGates("pc.md", rg4ProjectContext).Commands)
	require.Empty(t, codeValidationNotes("# T\n\n## Rules\n\n```sh\nmake x # not under Code Validation, so not here\n```\n"))
}

// TestClaudeCandidates: RG-4c (D264), from cortex's review — ten CLAUDE.md
// rules had no row, and seven of them were not rule-worded: plain prose,
// table cells and a code block's comment. CLAUDE.md is read whole: every
// paragraph of three words or more, every table body row (not the header,
// not the separator), every code-block comment note, and its list items.
func TestClaudeCandidates(t *testing.T) {
	doc := "# Cortex\n\n> Project guidance for agents. Keep this file current.\n\n" +
		"## Architecture\n\n| Layer | What |\n|---|---|\n| Memory plane | MCP session state is **not** the ledger. |\n\n" +
		"**Hybrid versioning decision (settled).** Power users may still author native workflows.\n\n" +
		"## Repo structure\n\n```\nREADME.md   # Human entry point.\n_output/    # GITIGNORED — never commit anything here.\n```\n\n" +
		"- Two words\n- Keep commits small and focused\n\nOk then.\n"
	type row struct{ text, source, section string }
	cs := claudeCandidates(doc)
	got := make([]row, 0, len(cs))
	for _, c := range cs {
		got = append(got, row{c.Text, c.Source, c.Section})
	}
	require.Equal(t, []row{
		{"Project guidance for agents. Keep this file current.", "3", "Cortex"}, // E-R7f-1
		{"Memory plane | MCP session state is **not** the ledger.", "9", "Architecture"},
		{"**Hybrid versioning decision (settled).** Power users may still author native workflows.", "11", "Architecture"},
		{"`README.md` — Human entry point.", "16", "Repo structure"},
		{"`_output/` — GITIGNORED — never commit anything here.", "17", "Repo structure"},
		{"Keep commits small and focused", "21", "Repo structure"},
	}, got)
	// project-context keeps the rule-word test: the same prose gives none.
	require.Empty(t, ruleCandidates("# P\n\n## A\n\nPower users may still author native workflows.\n\n| a | b |\n|---|---|\n| x y z | w |\n", false))
}

// TestRuleCandidates_TableOfContents: RG-4b — no section is skipped, but a
// list item of links only names sections and binds nothing.
func TestRuleCandidates_TableOfContents(t *testing.T) {
	cs := ruleCandidates("# T\n\n## Table of Contents\n\n- [Testing Rules](#testing-rules)\n"+
		"- [Code Validation](#code-validation), [Gotchas](#gotchas) and more\n- [A](#a) · [B](#b) · [C](#c)\n", false)
	require.Len(t, cs, 1)
	require.Equal(t, "[Code Validation](#code-validation), [Gotchas](#gotchas) and more", cs[0].Text)
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
		{"An overview line in team memory counts too (RG-4b).", "28", "Overview"},
	}, got)
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
		"CLAUDE.md": strings.Replace(string(claude), "- ok\n",
			"- ok\n\nThe `_output/` folder is scratch: never commit anything here.\n", 1),
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
		// E-R7f-1: the rows under CLAUDE.md's title carry it as their section.
		"### `CLAUDE.md` — Greeter\n\n| # | Candidate | Line | Disposition |\n|---|---|---|---|\n| 1 | Project rules (see ADR-0003). | `3` | |\n| 2 | NEVER push a tag without the operator's explicit say-so | `5` | |\n",
		"| 3 | Commit messages follow Conventional Commits, always | `6` · `" + pc + ":62` | |\n",
		"### `" + pc + "` — Technology Stack & Versions\n\n| # | Candidate | Line | Disposition |\n|---|---|---|---|\n| 9 | Go toolchain pinned exactly by go.mod's toolchain line. | `22` | |\n",
		"### `" + pc + "` — Critical Implementation Rules > Testing Rules\n",
		"| A shared fixture is read-only: never write to it from a test, and always copy it into t.TempDir(). | `33` | |",
		// RG-4c (D264): a CLAUDE.md rule written as a paragraph.
		"| The `_output/` folder is scratch: never commit anything here. | `9` | |",
		// RG-4b (D262): the overview, and a note inside Code Validation's fence.
		"### `" + pc + "` — Overview\n",
		"| Never fold the release generator into the main binary. | `13` | |",
		"| `make test` — the full suite, adopted verbatim; do not narrow it to the packages a change touched | `50` | |",
		"### `apex-v0-final:development/team-memory.md` — Process & Workflow Rules > Never invent an undesigned artifact\n",
		"| If an AC asserts a mechanism nobody designed, refuse to fabricate it. | `9` | |",
		"| The audit gate exits 1 until the signal package has tests. | `24` | |",
	} {
		require.Contains(t, d, want)
	}
	for _, not := range []string{"(#testing-rules)", "| ok |", "two minutes", "never skip", "never parsed", "Source: Epic"} {
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
		"text": "Integration tests run against a real engine, never a mock.", "source": pc + ":28",
		"section": "Critical Implementation Rules > Testing Rules",
	})
	require.Contains(t, report.Candidates, map[string]any{
		"text": "Commit messages follow Conventional Commits, always", "source": "CLAUDE.md:6", "also_in": []any{pc + ":62"},
		"section": "Greeter", // E-R7f-1: under CLAUDE.md's title
	})
}

// D274: the table lists every candidate, and says first how many rows there
// are and how big the file is, so a reader that reads it in parts can tell
// it reached the last; there is no overflow line. Q136's row keeps its line.
func TestWriteCandidates_EveryRowAndItsCount(t *testing.T) {
	tm := "apex-v0-final:development/team-memory.md"
	cands := []LiftCandidate{
		{Text: "a", Source: tm + ":3", Section: "Gotchas"},
		{Text: "b", Source: tm + ":9", Section: "Process"},
		{Text: "c", Source: tm + ":12", Section: "Process"},
	}
	require.Equal(t, "12", candidateLine(tm+":12"))
	require.Equal(t, "CLAUDE.md", candidateLine("CLAUDE.md"))
	var b strings.Builder
	writeCandidates(&b, cands, 7)
	d := b.String()
	require.True(t, strings.HasPrefix(d, "This file lists every candidate: 3 rows, numbered 1–3 (about 7 KB). If a Read stops short, read on with offset/limit until row 3.\n\n"), d)
	for _, row := range []string{"| 1 | a | `3` | |", "| 2 | b | `9` | |", "| 3 | c | `12` | |"} {
		require.Contains(t, d, row)
	}
	require.NotContains(t, d, "more candidate")
	b.Reset()
	writeCandidates(&b, nil, 1)
	require.NotContains(t, b.String(), "This file lists", "no count line when there is nothing to list")
}

// E-R7f-1 (eval 15:30): a candidate right under a document's sole H1,
// before any other heading, has the title as its section — in the walk
// (project-context, team-memory, CLAUDE.md) and in headingPaths (AGENTS.md's
// list items) alike; text above the H1 still has none, and a document with
// two H1s keeps them in its paths as before.
func TestSection_UnderTheSoleH1(t *testing.T) {
	doc := "above the title, must stay without a section\n\n# The Title\n\nNever skip the title's own rules here.\n\n- a list item under the title\n\n## Part\n\n- an item under the part\n"
	type row struct{ text, section string }
	cs := ruleCandidates(doc, false)
	got := make([]row, 0, len(cs))
	for _, c := range cs {
		got = append(got, row{c.Text, c.Section})
	}
	require.Equal(t, []row{
		{"above the title, must stay without a section", ""},
		{"Never skip the title's own rules here.", "The Title"},
		{"a list item under the title", "The Title"},
		{"an item under the part", "Part"},
	}, got)
	paths := headingPaths(splitLines(doc))
	require.Empty(t, paths[1])
	require.Equal(t, "The Title", paths[7])
	require.Equal(t, "Part", paths[11])
	two := headingPaths(splitLines("# A\n\n- x\n\n# B\n\n- y\n"))
	require.Equal(t, "A", two[3])
	require.Equal(t, "B", two[7])
}

// D267 (D277): team-memory is read whole, like CLAUDE.md — mdpdf's review
// on rc7g found entry prose with no rule word ("a session may not grant
// itself this route") with no row. Every paragraph and table body row of
// three words or more is a candidate now, and a code-block note; a
// "Source:" line still is not, and an entry with nothing else is still
// listed by its title.
func TestTeamMemoryCandidates_ReadWhole(t *testing.T) {
	doc := "# Team Memory\n\nDurable lessons, kept on purpose.\n\n## Security\n\n" +
		"### Grants — 2026-10-01\n\nA session may not grant itself this route.\n\nSource: Epic 9 retrospective\n\n" +
		"### Ports — 2026-10-02\n\n| Service | Port |\n|---|---|\n| nats server here | 4222 |\n\n" +
		"### Gate — 2026-10-03\n\n```sh\nmake gate   # the whole gate, every time\n```\n\n" +
		"### Error handling — 2026-08-01\n\nSource: Epic 1 retrospective\n"
	type row struct{ text, source, section string }
	cs := teamMemoryCandidates(doc)
	got := make([]row, 0, len(cs))
	for _, c := range cs {
		got = append(got, row{c.Text, c.Source, c.Section})
	}
	require.Equal(t, []row{
		{"Durable lessons, kept on purpose.", "3", "Team Memory"},
		{"A session may not grant itself this route.", "9", "Security > Grants"},
		{"nats server here | 4222", "17", "Security > Ports"},
		{"`make gate` — the whole gate, every time", "22", "Security > Gate"},
		{"Error handling", "25", "Security"},
	}, got)
	// project-context keeps the rule-word test: the same prose is no candidate there.
	require.Empty(t, ruleCandidates("# P\n\n## Rules\n\nA session may not grant itself this route.\n", false))
	// A heading inside a Markdown fence is an example, not a comment.
	for _, c := range teamMemoryCandidates("# T\n\n## A\n\n### E — 2026-10-01\n\nKeep entries dated like this one.\n\n```markdown\n### Not an entry — 2026-10-02\n```\n") {
		require.NotContains(t, c.Text, "Not an entry")
	}
}
