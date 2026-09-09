package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/bridge/config"
	"github.com/exoport/apex_process_ape/internal/outputstyles"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func writeStyleTable(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, outputstyles.TableFile), []byte(body), 0o644))
	return root
}

// The precedence that matters most: an operator who types the flag must
// outrank the framework's file. Getting this backwards would take the
// override away from the person at the terminal and hand it to a
// checked-in file, which is the one direction nobody could work around.
func TestResolveSkillOutputStyle_FlagBeatsTable(t *testing.T) {
	root := writeStyleTable(t, "apex-x,Concise\n")
	got := resolveSkillOutputStyle(root, "apex-x", "Explanatory", true, nil)
	require.Equal(t, "Explanatory", got)
}

// `--output-style Default` is an explicit choice, and it looks exactly
// like no flag at all once it reaches a string. Only Changed can tell
// them apart, and if it could not, the table would silently win.
func TestResolveSkillOutputStyle_ExplicitDefaultBeatsTable(t *testing.T) {
	root := writeStyleTable(t, "apex-x,Concise\n")
	got := resolveSkillOutputStyle(root, "apex-x", config.DefaultOutputStyle, true, nil)
	require.Equal(t, config.DefaultOutputStyle, got)
}

func TestResolveSkillOutputStyle_TableAppliesWhenFlagAbsent(t *testing.T) {
	root := writeStyleTable(t, "apex-x,Concise\napex-y,Explanatory\n")
	require.Equal(t, "Concise", resolveSkillOutputStyle(root, "apex-x", "", false, nil))
	require.Equal(t, "Explanatory", resolveSkillOutputStyle(root, "apex-y", "", false, nil))
}

// An unenrolled skill and a project with no table at all both land on
// the pinned default — the behaviour of every dispatch before the table
// existed.
func TestResolveSkillOutputStyle_UnenrolledAndAbsentAreUnchanged(t *testing.T) {
	root := writeStyleTable(t, "apex-x,Concise\n")
	require.Empty(t, resolveSkillOutputStyle(root, "apex-other", "", false, nil))
	require.Empty(t, resolveSkillOutputStyle(t.TempDir(), "apex-x", "", false, nil))
}

// A table that cannot be parsed must not take the dispatch down with
// it: the file expresses a preference about narration, not a contract
// about what the run must produce. It has to say so, though.
func TestResolveSkillOutputStyle_UnreadableTableWarnsAndFallsBack(t *testing.T) {
	root := writeStyleTable(t, "apex-x,\"unterminated\n")
	var warnings []string
	got := resolveSkillOutputStyle(root, "apex-x", "", false, func(m string) { warnings = append(warnings, m) })
	require.Empty(t, got)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], outputstyles.TableFile)
}

func TestResolveSkillOutputStyle_RowWarningsReachTheOperator(t *testing.T) {
	root := writeStyleTable(t, "apex-x,concise\n")
	var warnings []string
	got := resolveSkillOutputStyle(root, "apex-x", "", false, func(m string) { warnings = append(warnings, m) })
	require.Equal(t, "concise", got, "the declared value still reaches claude verbatim")
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "Concise")
}

func TestOutputStyleFlagSet(t *testing.T) {
	newCmd := func() *cobra.Command {
		var style string
		cmd := &cobra.Command{Use: "x", Run: func(*cobra.Command, []string) {}}
		addOutputStyleFlag(cmd, &style)
		return cmd
	}
	cmd := newCmd()
	require.NoError(t, cmd.Flags().Parse([]string{}))
	require.False(t, outputStyleFlagSet(cmd))

	cmd = newCmd()
	require.NoError(t, cmd.Flags().Parse([]string{"--output-style", "Default"}))
	require.True(t, outputStyleFlagSet(cmd), "an explicit Default is still a choice")

	// A command with no such flag must answer false rather than panic:
	// the helper is called from every spawn path and they do not all
	// register it.
	require.False(t, outputStyleFlagSet(&cobra.Command{Use: "y"}))
}

// styleOf digs the pinned output style back out of a built prepend
// slice, so the assertions below test what claude would actually be
// launched with rather than an intermediate variable.
func styleOf(t *testing.T, flags []string) string {
	t.Helper()
	for i := range len(flags) - 1 {
		if flags[i] != "--settings" {
			continue
		}
		var settings map[string]any
		require.NoError(t, json.Unmarshal([]byte(flags[i+1]), &settings))
		if v, ok := settings["outputStyle"]; ok {
			s, _ := v.(string)
			return s
		}
		return ""
	}
	t.Fatalf("no --settings in %v", flags)
	return ""
}

func loadSpecFromYAML(t *testing.T, body string) *pipeline.Spec {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "_apex", "pipelines")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "demo.yaml"), []byte(body), 0o600))
	spec, err := pipeline.LoadSpec("demo", root)
	require.NoError(t, err)
	return spec
}

// Phase 1: a pipeline-level declaration reaches every stage through the
// run-level flags, with no per-stage map needed at all.
func TestBuildSpecPrepends_PipelineLevelStyle(t *testing.T) {
	spec := loadSpecFromYAML(t, `
name: demo
output-style: Concise
stages:
  a:
    chain:
      - skill: one
  b:
    chain:
      - skill: two
`)
	run, perStage, err := buildSpecPrepends("/usr/bin/ape", 4242, config.ModeTUI, spec, runConfig{})
	require.NoError(t, err)
	require.Equal(t, "Concise", styleOf(t, run))
	require.Empty(t, perStage, "one style for the whole run needs no per-stage override")
}

// Phase 2: a stage-level declaration produces an override for that
// stage only, and every other stage stays on the run-level flags.
func TestBuildSpecPrepends_StageLevelStyle(t *testing.T) {
	spec := loadSpecFromYAML(t, `
name: demo
stages:
  plain:
    chain:
      - skill: one
  terse:
    output-style: Concise
    chain:
      - skill: two
`)
	run, perStage, err := buildSpecPrepends("/usr/bin/ape", 4242, config.ModeTUI, spec, runConfig{})
	require.NoError(t, err)
	require.Equal(t, config.DefaultOutputStyle, styleOf(t, run))
	require.Len(t, perStage, 1)
	require.Equal(t, "Concise", styleOf(t, perStage["terse"]))
	require.NotContains(t, perStage, "plain")
}

// Stage overrides pipeline, including back to Default: a mixed pipeline
// is the case the framework actually ships.
func TestBuildSpecPrepends_StageOverridesPipelineBothWays(t *testing.T) {
	spec := loadSpecFromYAML(t, `
name: demo
output-style: Concise
stages:
  inherits:
    chain:
      - skill: one
  reverts:
    output-style: Default
    chain:
      - skill: two
`)
	run, perStage, err := buildSpecPrepends("/usr/bin/ape", 4242, config.ModeTUI, spec, runConfig{})
	require.NoError(t, err)
	require.Equal(t, "Concise", styleOf(t, run))
	require.Len(t, perStage, 1)
	require.Equal(t, config.DefaultOutputStyle, styleOf(t, perStage["reverts"]))
}

// The operator's flag flattens the whole cascade — no stage may keep a
// declared style once --output-style is typed.
func TestBuildSpecPrepends_FlagFlattensEveryStage(t *testing.T) {
	spec := loadSpecFromYAML(t, `
name: demo
output-style: Concise
stages:
  a:
    output-style: Explanatory
    chain:
      - skill: one
`)
	cfg := runConfig{outputStyle: "Learning", outputStyleSet: true}
	run, perStage, err := buildSpecPrepends("/usr/bin/ape", 4242, config.ModeTUI, spec, cfg)
	require.NoError(t, err)
	require.Equal(t, "Learning", styleOf(t, run))
	require.Empty(t, perStage)
}

// `--output-style inherit` writes no key at all; a stage declaration
// must not smuggle one back in when the operator asked for the
// machine's own style.
func TestBuildSpecPrepends_InheritFlagWritesNoKeyAnywhere(t *testing.T) {
	spec := loadSpecFromYAML(t, `
name: demo
stages:
  a:
    output-style: Concise
    chain:
      - skill: one
`)
	cfg := runConfig{outputStyle: config.InheritOutputStyle, outputStyleSet: true}
	run, perStage, err := buildSpecPrepends("/usr/bin/ape", 4242, config.ModeTUI, spec, cfg)
	require.NoError(t, err)
	require.Empty(t, styleOf(t, run))
	require.Empty(t, perStage)
}

// REGRESSION. The run-level style can come from OUTSIDE the spec — the
// `--output-style` flag, or `ape task`'s per-skill table. Every stage is
// then undeclared, and the first cut treated "undeclared" as "differs
// from runStyle" and built an override pinning Default onto each one.
// The run flags said Concise, the stage flags said Default, and the
// stage flags are the ones that reach claude: the style was silently
// discarded on every `ape task` dispatch, with every other gate green.
//
// Asserted through prependForStage — what the runner actually spawns
// with — because asserting on the run-level slice alone is precisely
// what let this through.
func TestBuildSpecPrepends_RunLevelStyleFromOutsideTheSpecReachesEveryStage(t *testing.T) {
	spec := loadSpecFromYAML(t, `
name: demo
stages:
  a:
    chain:
      - skill: one
  b:
    chain:
      - skill: two
`)
	cfg := runConfig{outputStyle: "Concise"} // resolved from the CSV, flag untyped
	run, perStage, err := buildSpecPrepends("/usr/bin/ape", 4242, config.ModeTUI, spec, cfg)
	require.NoError(t, err)
	require.Equal(t, "Concise", styleOf(t, run))
	require.Empty(t, perStage, "an undeclared stage inherits; it must not be given an override")

	opts := pipeline.RunOptions{PrependFlags: run, StagePrependFlags: perStage}
	for _, stage := range []string{"a", "b"} {
		require.Equal(t, "Concise", styleOf(t, pipeline.PrependForStage(opts, stage)),
			"stage %q must spawn with the resolved style", stage)
	}
}

// The `ape task` shape end to end: a single-step spec that declares
// nothing, with the style resolved before the runner is reached.
func TestBuildSpecPrepends_SingleStepTaskSpecKeepsItsResolvedStyle(t *testing.T) {
	step := pipeline.Step{Skill: "apex-pattern-survey"}
	spec := pipeline.NewSingleStepSpec("apex-pattern-survey", step, nil)

	cfg := runConfig{outputStyle: "Concise", outputStyleSet: true}
	run, perStage, err := buildSpecPrepends("/usr/bin/ape", 4242, config.ModeTUI, spec, cfg)
	require.NoError(t, err)
	require.Empty(t, perStage)

	opts := pipeline.RunOptions{PrependFlags: run, StagePrependFlags: perStage}
	require.Equal(t, "Concise", styleOf(t, pipeline.PrependForStage(opts, "apex-pattern-survey")))
}

// A spec declaring nothing must keep pinning Default and produce no
// per-stage map — the no-change path for every pipeline shipping today.
func TestBuildSpecPrepends_UndeclaredSpecIsUnchanged(t *testing.T) {
	spec := loadSpecFromYAML(t, `
name: demo
stages:
  a:
    chain:
      - skill: one
`)
	run, perStage, err := buildSpecPrepends("/usr/bin/ape", 4242, config.ModeTUI, spec, runConfig{})
	require.NoError(t, err)
	require.Equal(t, config.DefaultOutputStyle, styleOf(t, run))
	require.Nil(t, perStage)
	require.Contains(t, strings.Join(run, " "), "--strict-mcp-config")
}
