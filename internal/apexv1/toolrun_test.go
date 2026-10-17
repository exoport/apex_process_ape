package apexv1

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const toolRunConfig = `models:
  security-lens: {model: sonnet, effort: low}
tools:
  design:
    - {name: ui-mockup, skill: "frontend-design:mockup", when: "a screen changes", args: "{change} --out {assets}/mock-{task}.png", model: opus}
    - {name: copy-pass, skill: copy-pass, when: "copy changes"}
  review:
    - {name: security, skill: security-lens, when: "auth", effort: high}
`

// D74, D77: a tools: entry is run with its args template filled, else the
// default --change {id} [--task {task}]; its model/effort win over
// models.<skill>.
func TestPrepareTool(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = toolRunConfig
	p := fixture(t, files)
	doc := changeDoc("add-wave", "greeting", addedWave,
		"- T1 add wave — tool: copy-pass — check: ./wave.sh\n- T2 copy reads well — tool: ui-mockup — check: human-review\n")
	addChange(t, p, "add-wave", strings.Replace(doc, "touches: [greeting]\n", "touches: [greeting]\nreview_tools: [security]\n", 1))
	commitAll(t, p, "plan: add-wave")

	plan, err := p.PrepareTool(ctx(), "add-wave", "ui-mockup", "T2")
	require.NoError(t, err)
	require.Equal(t, "design", plan.Slot)
	require.Equal(t, "frontend-design:mockup", plan.Skill)
	require.Equal(t, "development/changes/add-wave/change.md --out development/changes/add-wave/assets/mock-T2.png", plan.Args)
	require.Equal(t, SkillModel{Model: "opus"}, plan.Model)

	plan, err = p.PrepareTool(ctx(), "add-wave", "copy-pass", "T1")
	require.NoError(t, err)
	require.Equal(t, "--change add-wave --task T1", plan.Args)
	plan, err = p.PrepareTool(ctx(), "add-wave", "security", "")
	require.NoError(t, err)
	require.Equal(t, "review", plan.Slot)
	require.Equal(t, "--change add-wave", plan.Args)
	require.Equal(t, SkillModel{Model: "sonnet", Effort: "high"}, plan.Model)

	_, err = p.PrepareTool(ctx(), "add-wave", "nope", "")
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "tool.unknown", fe.Code)
	require.Equal(t, ActionReplan, ActionFor(fe.Code))

	plan, err = p.PrepareTool(ctx(), "add-wave", "ui-mockup", "")
	require.NoError(t, err)
	require.Equal(t, "T2", plan.Task, "a design tool's task is the one whose line names it")

	_, err = p.PrepareTool(ctx(), "add-wave", "copy-pass", "T2")
	fe, ok = errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "tool.unknown", fe.Code, "T2 names ui-mockup, not copy-pass")
	_, err = p.PrepareTool(ctx(), "add-wave", "copy-pass", "T9")
	require.ErrorContains(t, err, "has no task T9")
	_, err = p.PrepareTool(ctx(), "no-such", "copy-pass", "")
	require.ErrorContains(t, err, "no change no-such")
}
