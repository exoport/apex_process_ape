package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const addedWave = "### ADDED [greeting] Requirement: Wave\nThe system SHALL wave.\n#### Scenario: wave\n- GIVEN x WHEN y THEN z\n- check: ./wave.sh\n"

func TestCheckChange(t *testing.T) {
	okTasks := "- T1 add wave — check: ./wave.sh\n"
	cases := []struct {
		name string
		id   string
		doc  string
		// mutate runs after the change is written.
		mutate func(t *testing.T, p *Project)
		want   []string // substrings; empty = green
	}{
		{name: "green ADDED", id: "add-wave", doc: changeDoc("add-wave", "greeting", addedWave, okTasks)},
		{name: "green none", id: "tidy-docs", doc: changeDoc("tidy-docs", "greeting", "none — docs only", "- T1 docs — check: human-review\n")},
		{
			name: "green MODIFIED + REMOVED",
			id:   "rework-hello",
			doc: changeDoc("rework-hello", "greeting",
				"### MODIFIED [greeting] Requirement: Say hello\nThe system SHALL print Hello, then Hi.\n#### Scenario: plain run\n- GIVEN nothing WHEN greet runs THEN it prints Hello\n- check: ./greet.sh | grep -q Hello\n#### Scenario: hi\n- check: ./greet.sh | grep -q Hi\n\n### REMOVED [greeting] Requirement: Say name\nReason: unused\n",
				"- T1 hi — check: ./greet.sh | grep -q Hi\n"),
		},
		{
			name: "green new capability",
			id:   "add-cli",
			doc:  changeDoc("add-cli", "cli", "### ADDED [cli] Requirement: Help\nThe system SHALL print help.\n#### Scenario: h\n- check: ./greet.sh --help\n", okTasks),
		},
		{
			name: "MODIFIED names a missing heading",
			id:   "fix-hello",
			doc:  changeDoc("fix-hello", "greeting", "### MODIFIED [greeting] Requirement: Say helo\nx\n#### Scenario: s\n- check: true\n", okTasks),
			want: []string{`MODIFIED names "Say helo"`},
		},
		{
			name: "REMOVED names a missing heading and has no reason",
			id:   "drop-x",
			doc:  changeDoc("drop-x", "greeting", "### REMOVED [greeting] Requirement: Nope\n", okTasks),
			want: []string{`REMOVED names "Nope"`, "needs a `Reason:`"},
		},
		{
			name: "ADDED names an existing heading",
			id:   "add-hello",
			doc:  changeDoc("add-hello", "greeting", "### ADDED [greeting] Requirement: Say hello\nx\n#### Scenario: s\n- check: true\n", okTasks),
			want: []string{"already exists in specs/greeting — use MODIFIED"},
		},
		{
			name: "delta capability not in touches",
			id:   "add-wave",
			doc:  changeDoc("add-wave", "cli", addedWave, okTasks),
			want: []string{"[greeting] is not in frontmatter touches"},
		},
		{
			name: "same requirement twice",
			id:   "add-wave",
			doc:  changeDoc("add-wave", "greeting", addedWave+"\n"+addedWave, okTasks),
			want: []string{"appears twice in the delta"},
		},
		{
			name: "requirement without scenario",
			id:   "add-wave",
			doc:  changeDoc("add-wave", "greeting", "### ADDED [greeting] Requirement: Wave\nThe system SHALL wave.\n", okTasks),
			want: []string{"has no `#### Scenario:`"},
		},
		{
			name: "task without check, checkbox, duplicate id",
			id:   "add-wave",
			doc:  changeDoc("add-wave", "greeting", addedWave, "- T1 add wave\n- [x] T2 done — check: true\n- T2 again — check: true\n- write more\n"),
			want: []string{"task T1 has no `check:", "no checkboxes", "duplicate task T2", "task line must read"},
		},
		{
			name: "empty tasks and delta",
			id:   "add-wave",
			doc:  changeDoc("add-wave", "greeting", "", ""),
			want: []string{"## Delta is empty", "## Tasks has no tasks"},
		},
		{
			name: "frontmatter: id, milestone, touches, decisions",
			id:   "add-wave",
			doc:  strings.Replace(changeDoc("other-id", "", addedWave, okTasks), "milestone: M1", "milestone: M9\ndecisions: [ADR-0009, adr1]", 1),
			want: []string{`frontmatter id "other-id" does not match`, "milestone M9 is not in roadmap.md", "touches: is empty", "ADR-0009 has no file", `"adr1" is not ADR-NNNN`},
		},
		{
			name: "missing sections",
			id:   "add-wave",
			doc:  "---\nid: add-wave\nmilestone: M1\ntouches: [greeting]\n---\n# T\n## Why\n<!-- todo -->\n",
			want: []string{"`## Why` is empty", "missing `## Out of scope`", "missing `## Delta`", "missing `## Tasks`"},
		},
		{
			name: "over the 16 KB cap",
			id:   "add-wave",
			doc:  changeDoc("add-wave", "greeting", addedWave, okTasks) + strings.Repeat("x", 16<<10),
			want: []string{"over the 16 KB cap (caps.change_md) — split this change"},
		},
		{
			name: "design.md over the cap",
			id:   "add-wave",
			doc:  changeDoc("add-wave", "greeting", addedWave, okTasks),
			mutate: func(t *testing.T, p *Project) {
				t.Helper()
				write(t, p, "development/changes/add-wave/design.md", strings.Repeat("y", 16<<10+1))
			},
			want: []string{"design.md is 16.0 KB, over the 16 KB cap (caps.design_md)", "split this change"},
		},
		{
			name: "unknown frontmatter key; x- keys are free",
			id:   "add-wave",
			doc:  strings.Replace(changeDoc("add-wave", "greeting", addedWave, okTasks), "milestone: M1", "milestone: M1\nmilestones: [M2]\nx-jira: ABC-12", 1),
			want: []string{`frontmatter key "milestones" is not one of`},
		},
		{
			name: "no frontmatter",
			id:   "add-wave",
			doc:  "# T\n## Why\nx\n",
			want: []string{"no YAML frontmatter"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := fixture(t, baseFiles())
			addChange(t, p, tc.id, tc.doc)
			if tc.mutate != nil {
				tc.mutate(t, p)
			}
			got := findingMessages(p.CheckChange(tc.id))
			if len(tc.want) == 0 {
				require.Empty(t, got)
				return
			}
			for _, w := range tc.want {
				require.Contains(t, got, w)
			}
		})
	}
}

func TestCheckChange_NoStateIsAFinding(t *testing.T) {
	p := fixture(t, baseFiles())
	write(t, p, "development/changes/add-wave/change.md", changeDoc("add-wave", "greeting", addedWave, "- T1 x — check: true\n"))
	require.Contains(t, findingMessages(p.CheckChange("add-wave")), "no state.json")
}

func TestProjectCheck(t *testing.T) {
	p := fixture(t, baseFiles())
	require.Empty(t, p.Check())

	write(t, p, "CLAUDE.md", "# old instructions\n")
	write(t, p, "AGENTS.md", strings.Repeat("l\n", 8<<10+1))
	write(t, p, "development/specs/Bad_Cap/spec.md", "# x\n")
	write(t, p, "development/specs/empty/.keep", "")
	write(t, p, "development/decisions/0001-x.md", "# ADR-0001: x\n")
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", "", ""))
	got := findingMessages(p.Check())
	for _, w := range []string{
		"CLAUDE.md: must import the map",
		"AGENTS.md is 16.0 KB, over the 16 KB cap (caps.agents_md)",
		"capability folder must be a kebab-case slug",
		"capability folder without spec.md",
		"missing `Status:",
		"development/changes/add-wave/change.md",
	} {
		require.Contains(t, got, w)
	}
}

func TestProjectCheck_ArchivedChangesAreNotChecked(t *testing.T) {
	files := baseFiles()
	files["development/archive/2026-10-01-old-thing/change.md"] = "garbage"
	p := fixture(t, files)
	require.Empty(t, p.Check())
	require.Equal(t, "2026-10-01-old-thing", p.ArchivedIDs()["old-thing"])
}

// Backlog 24: an older ape archived into changes/archive/; this one
// reports the folder with the move to make, and reads nothing in it.
func TestProjectCheck_OldArchiveLocationIsAFinding(t *testing.T) {
	files := baseFiles()
	files["development/changes/archive/2026-10-01-old-thing/change.md"] = "garbage"
	p := fixture(t, files)
	fs := ClassifyFindings(p.Check())
	require.Len(t, fs, 1, findingMessages(fs))
	require.Equal(t, "layout.old-archive", fs[0].Code)
	require.Equal(t, "development/changes/archive", fs[0].File)
	require.Equal(t, "move development/changes/archive/* to development/archive/", fs[0].Fix)
	require.Empty(t, p.ArchivedIDs())
}
