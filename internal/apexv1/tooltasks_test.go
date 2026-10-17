package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const toolsConfig = `project_name: x
tools:
  design:
    - {name: ui-mockup, skill: "kit:mockup", when: "a screen changes"}
    - {name: notes, skill: design-notes, when: "always"}
  review:
    - {name: security, skill: security-lens, when: "auth"}
`

// toolChange is the wave change with two design-tool tasks and a review
// tool.
func toolChange(reviewTools string) string {
	doc := changeDoc("add-wave", "greeting", addedWave,
		"- T1 add wave — check: ./wave.sh\n"+
			"- T2 mock the wave — tool: ui-mockup — check: human-review\n"+
			"- T3 write the design notes — tool: `notes` — check: test -f development/changes/add-wave/assets/notes.md\n")
	return strings.Replace(doc, "touches: [greeting]\n", "touches: [greeting]\nreview_tools: ["+reviewTools+"]\n", 1)
}

func TestParseChange_ToolSegment(t *testing.T) {
	c := ParseChange("change.md", toolChange("security"))
	require.Empty(t, c.UnknownKeys, "review_tools is a change key")
	require.Equal(t, []string{"security"}, c.Meta.ReviewTools)
	require.Len(t, c.Tasks, 3)
	require.Empty(t, c.Tasks[0].Tool)
	require.Equal(t, Task{ID: "T2", What: "mock the wave", Check: HumanReview, Tool: "ui-mockup", Line: c.Tasks[1].Line}, c.Tasks[1])
	require.Equal(t, "notes", c.Tasks[2].Tool)
	require.Equal(t, "write the design notes", c.Tasks[2].What)
	require.Equal(t, "test -f development/changes/add-wave/assets/notes.md", c.Tasks[2].Check)

	// "tool:" inside the task text is not the segment.
	c = ParseChange("change.md", changeDoc("x", "greeting", addedWave, "- T1 pick a tool: any — check: true\n"))
	require.Empty(t, c.Tasks[0].Tool)
	require.Equal(t, "pick a tool: any", c.Tasks[0].What)
}

// change check refuses a tool name tools: does not declare (tools.unknown,
// action replan), in a task or in review_tools:.
func TestCheckChange_UnknownTools(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = toolsConfig
	p := fixture(t, files)
	addChange(t, p, "add-wave", toolChange("security"))
	require.Empty(t, findingMessages(p.CheckChange("add-wave")))

	addChange(t, p, "add-wave", strings.Replace(toolChange("security, perf"), "tool: ui-mockup", "tool: ui-mock", 1))
	fs := p.CheckChange("add-wave")
	msgs := findingMessages(fs)
	require.Contains(t, msgs, "task T2's tool: ui-mock is not declared in tools.design of _apex/config.yaml — name a declared design tool (ui-mockup, notes)")
	require.Contains(t, msgs, "review_tools: perf is not declared in tools.review")
	require.Equal(t, []string{"tools.unknown", "tools.unknown"}, codesOf(fs))
	require.Equal(t, ActionReplan, ActionFor("tools.unknown"))

	// No tools: at all: every tool is unknown.
	write(t, p, "_apex/config.yaml", "project_name: x\n")
	require.Len(t, p.CheckChange("add-wave"), 4)
}

// status --json lists each change's design tools (with done) and review
// tools, resolved to their skills.
func TestStatus_ChangeTools(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = toolsConfig
	p := fixture(t, files)
	addChange(t, p, "add-wave", toolChange("security"))
	commitAll(t, p, "plan: add-wave")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)

	cs := p.Status().Changes[0]
	f := false
	require.Equal(t, []ChangeTool{
		{Task: "T2", Name: "ui-mockup", Skill: "kit:mockup", Done: &f},
		{Task: "T3", Name: "notes", Skill: "design-notes", Done: &f},
	}, cs.Tools)
	require.Equal(t, []ChangeTool{{Name: "security", Skill: "security-lens"}}, cs.ReviewTools)

	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	write(t, p, "development/changes/add-wave/assets/notes.md", "notes\n")
	commitAll(t, p, "feat: wave")
	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	cs = p.Status().Changes[0]
	require.False(t, *cs.Tools[0].Done, "a human-review tool task is done once a person accepts it")
	require.True(t, *cs.Tools[1].Done)

	// A change without tools: empty lists, never null.
	addChange(t, p, "other", changeDoc("other", "greeting", "none — nothing\n", "- T1 x — check: true\n"))
	for _, c := range p.Status().Changes {
		require.NotNil(t, c.Tools, c.ID)
		require.NotNil(t, c.ReviewTools, c.ID)
	}
}

// D74 Q23: commits that touch only the change's assets/ after verify do
// not stale the verify or the review; review tools' reports
// (assets/review-<name>.md), committed by the review session with
// review.md or left uncommitted, do not void its verdict.
func TestReviewRun_ToolAssetsDoNotStale(t *testing.T) {
	p := verifiedWave(t)
	write(t, p, "development/changes/add-wave/assets/mockup.svg", "<svg/>\n")
	commitAll(t, p, "design: add-wave mockup")
	_, err := p.PrepareReviewRun(ctx(), "add-wave")
	require.NoError(t, err, "an assets/ commit after verify is not code")

	// A review tool's report, uncommitted when the session starts.
	write(t, p, "development/changes/add-wave/assets/review-security.md", "no findings\n")
	_, err = p.PrepareReviewRun(ctx(), "add-wave")
	require.NoError(t, err)

	run := &ReviewRun{Change: "add-wave", StartedAt: Stamp(), HeadSHA: HeadSHA(ctx(), p.Root), DetachedID: "d1"}
	write(t, p, "development/changes/add-wave/assets/review-perf.md", "left uncommitted\n")
	run2 := []string{"add", "development/changes/add-wave/review.md", "development/changes/add-wave/assets/review-security.md"}
	gitRun(t, p, run2...)
	gitRun(t, p, "commit", "-q", "-m", "review: add-wave")
	_, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.NoError(t, err)
	out, err := p.SettleReviewRun(ctx(), run)
	require.NoError(t, err)
	require.Empty(t, out.Violations)
	require.Equal(t, StatusReviewed, out.Status)

	// After the review, another assets/ commit leaves it fresh.
	write(t, p, "development/changes/add-wave/assets/review-perf.md", "committed later\n")
	commitAll(t, p, "assets")
	stale, _ := p.reviewStale(ctx(), state(t, p, "add-wave"))
	require.False(t, stale)

	// Only review-*.md directly under assets/ is the review's.
	require.False(t, p.reviewMayTouch("add-wave", "development/changes/add-wave/assets/mockup.svg"))
	require.False(t, p.reviewMayTouch("add-wave", "development/changes/add-wave/assets/x/review-a.md"))
	require.False(t, p.reviewMayTouch("add-wave", "development/changes/other/assets/review-a.md"))
	require.True(t, p.reviewMayTouch("add-wave", "development/changes/add-wave/assets/review-a.md"))
}

func gitRun(t *testing.T, p *Project, args ...string) {
	t.Helper()
	run(t, p.Root, args...)
}
