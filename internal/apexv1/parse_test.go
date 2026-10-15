package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseChange_TasksAndDelta(t *testing.T) {
	doc := changeDoc("add-x", "greeting, cli",
		"### ADDED [greeting] Requirement: Wave\nThe system SHALL wave.\n#### Scenario: wave\n- GIVEN x WHEN y THEN z\n- check: ./wave.sh\n\n"+
			"### MODIFIED [greeting] Requirement: Say hello\nThe system SHALL print Hi.\n#### Scenario: hi\n- check: human-review\n\n"+
			"### REMOVED [greeting] Requirement: Say name\nReason: nobody uses it\n",
		"- T1 write wave — check: ./wave.sh\n- T2 copy is friendly — check: human-review\n- T3 a dash - in the text – check: `go test ./...`\n")
	c := ParseChange("change.md", doc)
	require.NoError(t, c.MetaErr)
	require.Equal(t, []string{"greeting", "cli"}, c.Meta.Touches)
	require.Equal(t, "Add x", c.Title)
	require.Len(t, c.Deltas, 3)
	require.Equal(t, OpAdded, c.Deltas[0].Op)
	require.Equal(t, "Wave", c.Deltas[0].Name)
	require.True(t, strings.HasPrefix(c.Deltas[0].Req.Text, "### Requirement: Wave\n"), c.Deltas[0].Req.Text)
	require.Len(t, c.Deltas[0].Req.Scenarios, 1)
	require.Equal(t, []string{"./wave.sh"}, c.Deltas[0].Req.Scenarios[0].Checks)
	require.Equal(t, OpRemoved, c.Deltas[2].Op)
	require.Equal(t, "nobody uses it", c.Deltas[2].Reason)
	require.NotContains(t, c.Deltas[2].Req.Text, "Reason:")

	require.Len(t, c.Tasks, 3)
	require.Equal(t, Task{ID: "T1", What: "write wave", Check: "./wave.sh", Line: c.Tasks[0].Line}, c.Tasks[0])
	require.True(t, c.Tasks[1].Human())
	require.Equal(t, "a dash - in the text", c.Tasks[2].What)
	require.Equal(t, "go test ./...", c.Tasks[2].Check)
	require.Equal(t, []string{"T2"}, c.HumanTasks())
	require.Len(t, c.CommandTasks(), 2)
	require.Empty(t, c.parse)
}

func TestParseChange_LineNumbersCountTheFrontmatter(t *testing.T) {
	doc := changeDoc("add-x", "greeting", "none — chore", "- [ ] T1 do — check: true\n")
	c := ParseChange("change.md", doc)
	require.Len(t, c.parse, 1)
	lines := strings.Split(doc, "\n")
	require.Equal(t, "- [ ] T1 do — check: true", lines[c.parse[0].Line-1])
}

func TestParseChange_DeltaNoneAndJunk(t *testing.T) {
	for name, tc := range map[string]struct {
		delta    string
		none     string
		problems int
	}{
		"em dash":         {"none — refactor only", "refactor only", 0},
		"hyphen":          {"none - docs", "docs", 0},
		"colon":           {"none: chore", "chore", 0},
		"comment ignored": {"<!-- hint -->\nnone — chore", "chore", 0},
		"junk":            {"we will add things", "", 1},
		"none plus junk":  {"none — chore\nmore text", "chore", 1},
		"bad heading":     {"### Added greeting stuff\nbody", "", 1},
	} {
		t.Run(name, func(t *testing.T) {
			c := ParseChange("c.md", changeDoc("add-x", "greeting", tc.delta, "- T1 x — check: true"))
			require.Equal(t, tc.none, c.DeltaNone)
			require.Len(t, c.parse, tc.problems, findingMessages(c.parse))
		})
	}
}

func TestParseChange_CodeFencesHideHeadings(t *testing.T) {
	delta := "### ADDED [greeting] Requirement: Wave\nThe system SHALL wave.\n```\n### REMOVED [x] Requirement: not a delta\n- check: not a check\n```\n#### Scenario: s\n- check: true\n"
	c := ParseChange("c.md", changeDoc("add-x", "greeting", delta, "- T1 x — check: true"))
	require.Len(t, c.Deltas, 1)
	require.Equal(t, []string{"true"}, c.Deltas[0].Req.Scenarios[0].Checks)
	require.Contains(t, c.Deltas[0].Req.Text, "### REMOVED [x] Requirement: not a delta", "fenced text is kept verbatim")
}

func TestParseSpec_RoundTrip(t *testing.T) {
	s := ParseSpec(greetingSpec)
	require.Equal(t, "Greeting", s.Title)
	require.Equal(t, "Say things to people.", s.Purpose)
	require.Len(t, s.Requirements, 2)
	require.Equal(t, "Say name", s.Requirements[1].Name)
	require.Equal(t, greetingSpec, s.Render())
	require.Empty(t, ValidateSpec("spec.md", greetingSpec))
}

func TestValidateSpec(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  string
		want string
	}{
		"no title":    {"Purpose: p\n### Requirement: R\n#### Scenario: s\n- check: true\n", "missing `# <Capability title>`"},
		"no purpose":  {"# T\n### Requirement: R\n#### Scenario: s\n- check: true\n", "missing `Purpose:`"},
		"no scenario": {"# T\nPurpose: p\n### Requirement: R\nThe system SHALL.\n", "has no `#### Scenario:`"},
		"no check":    {"# T\nPurpose: p\n### Requirement: R\n#### Scenario: s\n- GIVEN\n", "has no `- check:"},
		"two checks":  {"# T\nPurpose: p\n### Requirement: R\n#### Scenario: s\n- check: a\n- check: b\n", "more than one `check:`"},
		"stray check": {"# T\nPurpose: p\n### Requirement: R\n- check: a\n#### Scenario: s\n- check: b\n", "outside any scenario"},
		"duplicate":   {"# T\nPurpose: p\n### Requirement: R\n#### Scenario: s\n- check: a\n### Requirement: R\n#### Scenario: s\n- check: a\n", "duplicate requirement"},
		"other h3":    {"# T\nPurpose: p\n### Notes\n", "not `### Requirement:"},
		"empty check": {"# T\nPurpose: p\n### Requirement: R\n#### Scenario: s\n- check:\n", "empty `check:`"},
		"over 20KB":   {"# T\nPurpose: p\n" + strings.Repeat("x", SpecMaxBytes), "split into two capabilities"},
	} {
		t.Run(name, func(t *testing.T) {
			require.Contains(t, findingMessages(ValidateSpec("spec.md", tc.doc)), tc.want)
		})
	}
}

func TestValidateRoadmap(t *testing.T) {
	require.Empty(t, ValidateRoadmap("r.md", roadmapApproved))
	for name, tc := range map[string]struct {
		doc  string
		want string
	}{
		"title":       {"# Plan\n## M1 — x\nStatus: proposed\nGate: g\nGoal: x\n", "`# Roadmap`"},
		"none":        {"# Roadmap\n", "no milestones"},
		"bad heading": {"# Roadmap\n## Milestone one\n", "`## M<n> — <title>`"},
		"bad status":  {"# Roadmap\n## M1 — x\nStatus: started\nGate: g\nGoal: x\n", "only ape writes it"},
		"no status":   {"# Roadmap\n## M1 — x\nGate: g\nGoal: x\n", "no `Status:`"},
		"no gate":     {"# Roadmap\n## M1 — x\nStatus: proposed\nGoal: x\n", "no `Gate:`"},
		"no goal":     {"# Roadmap\n## M1 — x\nStatus: proposed\nGate: g\n", "no `Goal:`"},
		"goal hint":   {"# Roadmap\n## M1 — x\nStatus: proposed\nGate: g\nGoal: <!-- fill -->\n", "no `Goal:`"},
		"budget":      {"# Roadmap\n## M1 — x\nStatus: proposed\nGate: g\nGoal: x\nBudget: lots\n", "not a number"},
		"duplicate":   {"# Roadmap\n## M1 — x\nStatus: proposed\nGate: g\nGoal: x\n## M1 — y\nStatus: proposed\nGate: g\nGoal: x\n", "duplicate milestone"},
	} {
		t.Run(name, func(t *testing.T) {
			require.Contains(t, findingMessages(ValidateRoadmap("r.md", tc.doc)), tc.want)
		})
	}
}

func TestRoadmap_StatusForms(t *testing.T) {
	doc := "# Roadmap\n## M1 — a\nStatus: done (2026-10-15T12:00:00Z abc1234)\nGate: g\nGoal: x\n" +
		"## M2 — b\nStatus: approved (by Ann Lee 2026-10-15T12:00:00Z)   # ape writes this\nGate: g\nBudget: $12.5\nGoal: x\n" +
		"## M3 — c\nStatus: proposed\nGate: g\nGoal: x\n"
	r := ParseRoadmap("r.md", doc)
	require.Equal(t, MilestoneDone, r.Milestones[0].Status)
	require.Equal(t, MilestoneApproved, r.Milestones[1].Status)
	require.Equal(t, "2026-10-15T12:00:00Z", r.Milestones[1].ApprovedAt)
	require.InDelta(t, 12.5, *r.Milestones[1].Budget, 0.001)
	require.Equal(t, "M2", r.Current().ID)
	require.Equal(t, "M4", r.NextID())
	out, err := r.WithStatus("M3", "approved (by x 2026-10-15T13:00:00Z)")
	require.NoError(t, err)
	require.Equal(t, MilestoneApproved, ParseRoadmap("", out).Find("M3").Status)
	require.Equal(t, strings.Count(doc, "\n"), strings.Count(out, "\n"), "the status line is replaced, not added")
}

func TestValidateProjectMD(t *testing.T) {
	require.Empty(t, ValidateProjectMD("p.md", projectMD))
	skeleton := strings.ReplaceAll(projectSkeleton, "%s", "X")
	require.Contains(t, findingMessages(ValidateProjectMD("p.md", skeleton)), "`## Vision` is empty")
	require.Contains(t, findingMessages(ValidateProjectMD("p.md", "# X\n## Vision\nv\n")), "missing `## Users`")
	long := projectMD + strings.Repeat("line\n", ProjectMaxLines)
	require.Contains(t, findingMessages(ValidateProjectMD("p.md", long)), "over the 150-line cap")
	big := projectMD + strings.Repeat("x", ProjectMaxBytes)
	require.Contains(t, findingMessages(ValidateProjectMD("p.md", big)), "over the 8 KB cap")
}

func TestValidateDecision(t *testing.T) {
	good := "# ADR-0001: Use sh\nStatus: accepted\nDate: 2026-10-15\n## Context\nc\n## Decision\nd\n## Consequences\nx\nEnforced by: review\n"
	require.Empty(t, ValidateDecision("d.md", "0001-use-sh.md", good))
	require.Contains(t, findingMessages(ValidateDecision("d.md", "use-sh.md", good)), "NNNN-<slug>.md")
	require.Contains(t, findingMessages(ValidateDecision("d.md", "0002-use-sh.md", good)), "ADR-0002")
	bad := strings.Replace(good, "Status: accepted", "Status: maybe", 1)
	require.Contains(t, findingMessages(ValidateDecision("d.md", "0001-use-sh.md", bad)), "missing `Status:")
	require.Empty(t, ValidateDecision("d.md", "0001-use-sh.md", strings.Replace(good, "accepted", "superseded by ADR-0007", 1)))
}

func TestHasProse(t *testing.T) {
	for text, want := range map[string]bool{
		"":                          false,
		"<!-- hint -->":             false,
		"<!-- multi\nline -->":      false,
		"text":                      true,
		"<!-- hint --> text":        true,
		"<!-- a -->\n<!-- b -->\nc": true,
	} {
		require.Equal(t, want, hasProse(splitLines(text)), "%q", text)
	}
}
