package pipeline

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The cascade a pipeline actually has: stage overrides pipeline, and a
// stage that declares nothing inherits. There is no step level, by
// design — see Spec.OutputStyle.
func TestEffectiveOutputStyle_StageOverridesPipeline(t *testing.T) {
	root := t.TempDir()
	writeSpec(t, root, `
name: demo
output-style: Concise
stages:
  inherits:
    chain:
      - skill: a
  overrides:
    output-style: Default
    chain:
      - skill: b
`)
	spec, err := LoadSpec("demo", root)
	require.NoError(t, err)

	style, err := spec.EffectiveOutputStyle("inherits")
	require.NoError(t, err)
	require.Equal(t, "Concise", style)

	style, err = spec.EffectiveOutputStyle("overrides")
	require.NoError(t, err)
	require.Equal(t, "Default", style)

	_, err = spec.EffectiveOutputStyle("nope")
	require.Error(t, err)
}

// A spec that declares no style must resolve to empty, not to a literal
// "Default" — empty is what lets BuildSettings own the default and keeps
// an undeclared pipeline byte-identical to before this key existed.
func TestEffectiveOutputStyle_UndeclaredIsEmpty(t *testing.T) {
	root := t.TempDir()
	writeSpec(t, root, `
name: demo
stages:
  only:
    chain:
      - skill: a
`)
	spec, err := LoadSpec("demo", root)
	require.NoError(t, err)
	style, err := spec.EffectiveOutputStyle("only")
	require.NoError(t, err)
	require.Empty(t, style)
}

// The defect: a stage is ONE claude process launched with the first
// step's model, and nothing switches models mid-chain. A later step's
// `model:` is unhonourable, and it used to be reported as though it had
// been applied.
func TestStageModelConflicts_ReportsUnhonourableStepModels(t *testing.T) {
	root := t.TempDir()
	writeSpec(t, root, `
name: demo
stages:
  governance:
    model: sonnet
    chain:
      - skill: apex-adr-adoption
      - skill: apex-adr-survey
        model: opus
      - skill: apex-adr-close
  clean:
    model: sonnet
    chain:
      - skill: one
      - skill: two
`)
	spec, err := LoadSpec("demo", root)
	require.NoError(t, err)

	conflicts := spec.StageModelConflicts()
	require.Len(t, conflicts, 1, "only the stage with divergent models conflicts")
	require.Equal(t, "governance", conflicts[0].Stage)
	require.Equal(t, "claude-sonnet-5", conflicts[0].Launch)
	require.Len(t, conflicts[0].Steps, 1)
	require.Equal(t, 1, conflicts[0].Steps[0].Index)
	require.Equal(t, "apex-adr-survey", conflicts[0].Steps[0].Skill)
	require.Equal(t, "claude-opus-5", conflicts[0].Steps[0].Declared)
}

// The other direction of the same defect, and the one that is easiest to
// miss: the stage launches with NO model (claude's default) while a
// later step names one.
func TestStageModelConflicts_UndeclaredLaunchStillConflicts(t *testing.T) {
	root := t.TempDir()
	writeSpec(t, root, `
name: demo
stages:
  mixed:
    chain:
      - skill: first
      - skill: second
        model: opus
`)
	spec, err := LoadSpec("demo", root)
	require.NoError(t, err)

	conflicts := spec.StageModelConflicts()
	require.Len(t, conflicts, 1)
	require.Empty(t, conflicts[0].Launch, "no --model was passed at launch")
	require.Equal(t, "claude-opus-5", conflicts[0].Steps[0].Declared)
}

// A single-step stage can never conflict: the launch model IS the step's.
func TestStageModelConflicts_SingleStepStageIsNeverAConflict(t *testing.T) {
	root := t.TempDir()
	writeSpec(t, root, `
name: demo
stages:
  solo:
    chain:
      - skill: only
        model: opus
`)
	spec, err := LoadSpec("demo", root)
	require.NoError(t, err)
	require.Empty(t, spec.StageModelConflicts())
}

// Unknown keys are dropped in silence by yaml.Unmarshal, which is how a
// declaration can look installed and do nothing. Every level has to be
// scanned, because a mistyped key is as likely on a step as on the root.
func TestUnknownKeyWarnings_AllThreeLevels(t *testing.T) {
	root := t.TempDir()
	writeSpec(t, root, `
name: demo
outputstyle: Concise
stages:
  alpha:
    output_style: Concise
    chain:
      - skill: a
        noclear: true
`)
	spec, err := LoadSpec("demo", root)
	require.NoError(t, err)

	got := map[string]string{}
	for _, k := range spec.UnknownKeyWarnings() {
		got[k.Key] = k.Location
		require.Positive(t, k.Line, "a warning without a line number cannot be acted on")
	}
	require.Equal(t, map[string]string{
		"outputstyle":  "pipeline",
		"output_style": `stage "alpha"`,
		"noclear":      `stage "alpha" step 0`,
	}, got)
}

// The keys ape does read must never be reported as unknown — a false
// warning here would train the reader to ignore the true ones.
func TestUnknownKeyWarnings_KnownKeysAreSilent(t *testing.T) {
	root := t.TempDir()
	writeSpec(t, root, `
name: demo
model: sonnet
effort: high
agent: apex-agent-dev
output-style: Concise
commit: false
requires:
  files:
    - README.md
stages:
  alpha:
    model: opus
    effort: max
    agent: apex-agent-pm
    output-style: Default
    commit: false
    chain:
      - skill: a
        agent: apex-agent-dev
        model: sonnet
        effort: low
        args: "--flag"
        prompt_flag: --prompt
        commit: false
        no-clear: true
`)
	spec, err := LoadSpec("demo", root)
	require.NoError(t, err)
	require.Empty(t, spec.UnknownKeyWarnings())
}
