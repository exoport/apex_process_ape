package apexv1

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

const toolsConfig = `models:
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
	files["_apex/config.yaml"] = toolsConfig
	p := fixture(t, files)
	addChange(t, p, "add-wave", waveChange("add-wave"))
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

	_, err = p.PrepareTool(ctx(), "add-wave", "ui-mockup", "")
	ue, ok := errors.AsType[*UsageError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "usage.task", ue.Code, "its args use {task}")
	_, err = p.PrepareTool(ctx(), "add-wave", "copy-pass", "T9")
	require.ErrorContains(t, err, "has no task T9")
	_, err = p.PrepareTool(ctx(), "no-such", "copy-pass", "")
	require.ErrorContains(t, err, "no change no-such")
}
