package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/effort"
	"github.com/stretchr/testify/require"
)

// envDumpShim is claudeREPLShim that first records the environment it was
// spawned with, so a test can see exactly which effort reached "claude".
func envDumpShim(t *testing.T) (shim, dump string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	dir := t.TempDir()
	dump = filepath.Join(dir, "env.txt")
	shim = filepath.Join(dir, "claude-shim.sh")
	body := "#!/bin/sh\nenv > '" + dump + "'\nPS1='❯ '\nexport PS1\nexec bash --noprofile --norc\n"
	require.NoError(t, os.WriteFile(shim, []byte(body), 0o755))
	return shim, dump
}

func effortEnvOf(t *testing.T, dump string) (level string, set bool) {
	t.Helper()
	data, err := os.ReadFile(dump)
	require.NoError(t, err)
	for line := range strings.SplitSeq(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "CLAUDE_CODE_EFFORT_LEVEL="); ok {
			return v, true
		}
	}
	return "", false
}

func runEffortSpec(t *testing.T, specBody string, table *effort.Defaults) (m Manifest, envDump string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "_apex", "pipelines")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fx.yaml"), []byte(specBody), 0o644))
	spec, err := LoadSpec("fx", root)
	require.NoError(t, err)
	stubSpecSkills(t, root, spec)
	shim, dump := envDumpShim(t)
	require.NoError(t, Run(context.Background(), spec, RunOptions{
		ProjectRoot:  root,
		ClaudeBin:    shim,
		NoCommit:     true,
		WaitStepDone: fastStepDone,
		EffortTable:  table,
	}))
	return loadLatestManifest(t, root, "fx"), dump
}

var fwTable = &effort.Defaults{
	Version:  1,
	Defaults: map[string]string{"opus": "medium", "sonnet": "high", "haiku": "medium"},
	Fallback: "high",
}

// With the table and nothing explicit, the stage gets NO process-wide
// effort: CLAUDE_CODE_EFFORT_LEVEL outranks the per-model table, so
// exporting it would flatten an Opus parent and its Sonnet sub-agents to
// one level — the defect the table exists to end.
func TestRun_TableGovernsWithNoProcessWideEffort(t *testing.T) {
	m, dump := runEffortSpec(t,
		"name: fx\nstages:\n  only:\n    model: sonnet\n    chain:\n      - skill: apex-fake\n", fwTable)
	_, set := effortEnvOf(t, dump)
	require.False(t, set, "no CLAUDE_CODE_EFFORT_LEVEL when the table governs")

	step := m.Stages[0].Steps[0]
	require.Equal(t, "high", step.Effort, "the sonnet row, resolved")
	require.Equal(t, effort.SourceTable, step.EffortSource)
}

// An explicit effort is an override, and process-wide by design.
func TestRun_ExplicitEffortIsProcessWide(t *testing.T) {
	m, dump := runEffortSpec(t,
		"name: fx\nstages:\n  only:\n    effort: low\n    chain:\n      - skill: apex-fake\n", fwTable)
	level, set := effortEnvOf(t, dump)
	require.True(t, set)
	require.Equal(t, "low", level)
	require.Equal(t, "low", m.Stages[0].Steps[0].Effort)
	require.Equal(t, effort.SourceStage, m.Stages[0].Steps[0].EffortSource)
}

// No table: exactly what ape did before the table existed.
func TestRun_NoTableKeepsTheLegacyDefault(t *testing.T) {
	m, dump := runEffortSpec(t,
		"name: fx\nstages:\n  only:\n    chain:\n      - skill: apex-fake\n", nil)
	level, set := effortEnvOf(t, dump)
	require.True(t, set)
	require.Equal(t, effort.LegacyDefault, level)
	require.Equal(t, effort.SourceLegacy, m.Stages[0].Steps[0].EffortSource)
}

// A later step's own effort never reaches the running session. The record
// says what ran, and keeps the declaration beside it — as for the model.
func TestRun_LaterStepEffortIsRecordedAsDeclared(t *testing.T) {
	m, _ := runEffortSpec(t, "name: fx\nstages:\n  only:\n    chain:\n"+
		"      - skill: apex-fake\n        effort: medium\n"+
		"      - skill: apex-fake\n        effort: low\n", nil)
	steps := m.Stages[0].Steps
	require.Len(t, steps, 2)
	require.Equal(t, "medium", steps[1].Effort, "step 2 ran at the launch effort")
	require.Equal(t, "low", steps[1].EffortDeclared)
	require.Empty(t, steps[0].EffortDeclared)
}

func TestSpec_EffortHelpers(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "_apex", "pipelines")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fx.yaml"), []byte("name: fx\neffort: high\nstages:\n"+
		"  a:\n    chain:\n      - skill: s0\n      - skill: s1\n        effort: low\n"+
		"  b:\n    effort: xtreme\n    chain:\n      - skill: s2\n"), 0o644))
	spec, err := LoadSpec("fx", root)
	require.NoError(t, err)

	level, source, err := spec.EffectiveEffort("a", 0, "medium")
	require.NoError(t, err)
	require.Equal(t, []string{"high", effort.SourcePipeline}, []string{level, source}, "the spec outranks --effort")
	level, source, _ = spec.EffectiveEffort("a", 1, "")
	require.Equal(t, []string{"low", effort.SourceStep}, []string{level, source})

	conflicts := spec.StageEffortConflicts()
	require.Len(t, conflicts, 1)
	require.Equal(t, "a", conflicts[0].Stage)
	require.Equal(t, "high", conflicts[0].Launch)
	require.Equal(t, "low", conflicts[0].Steps[0].Declared)

	errs := spec.EffortErrors()
	require.Len(t, errs, 1)
	require.Contains(t, errs[0], `stage "b"`)
	require.Contains(t, errs[0], "xtreme")
}

// Every stage's session carries APE_SESSION=<kind>/<run-id>, so an ape a
// skill shells out to inside it knows it is nested and refuses.
func TestRun_StampsTheSessionMarker(t *testing.T) {
	m, dump := runEffortSpec(t, "name: fx\nstages:\n  only:\n    chain:\n      - skill: apex-fake\n", nil)
	data, err := os.ReadFile(dump)
	require.NoError(t, err)
	require.Contains(t, string(data), "APE_SESSION=pipeline/"+m.RunID+"\n")
}

// With no --model, ape can only assume the table's fallback at launch.
// Neither record may present that as what ran: step-start says nothing,
// and step-end and the manifest carry the row of the model the step's own
// telemetry names. Found by the framework eval: an unpinned opus session
// ran every turn at medium while both records said high.
func TestRun_UnpinnedModelRecordsTheObservedRow(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "_apex", "pipelines")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fx.yaml"),
		[]byte("name: fx\nstages:\n  only:\n    chain:\n      - skill: apex-fake\n"), 0o644))
	spec, err := LoadSpec("fx", root)
	require.NoError(t, err)
	stubSpecSkills(t, root, spec)

	tele := &StepTelemetry{
		NumTurns: 5,
		Sessions: []SessionUsage{
			{SessionID: "main", ModelUsage: map[string]ModelUsage{"claude-opus-5-5": {NumTurns: 5}}},
			{SessionID: "sub", ParentSessionID: "main", ModelUsage: map[string]ModelUsage{"claude-sonnet-5": {NumTurns: 9}}},
		},
	}
	shim, _ := envDumpShim(t)
	require.NoError(t, Run(context.Background(), spec, RunOptions{
		ProjectRoot:     root,
		ClaudeBin:       shim,
		NoCommit:        true,
		WaitStepDone:    fastStepDone,
		EffortTable:     fwTable,
		StepTelemetryFn: func(string, int) *StepTelemetry { return tele },
	}))

	m := loadLatestManifest(t, root, "fx")
	step := m.Stages[0].Steps[0]
	require.Empty(t, step.Model, "unpinned")
	require.Equal(t, "medium", step.Effort, "the opus row, from the main session — not the sub-agent's sonnet")
	require.Equal(t, effort.SourceTable, step.EffortSource)

	runDir, err := filepath.EvalSymlinks(filepath.Join(root, "_output", "ape", "pipelines", "fx", "latest"))
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(runDir, step.EventsPath))
	require.NoError(t, err)
	events := map[string]map[string]any{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var ev map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &ev))
		kind, _ := ev["type"].(string)
		events[kind] = ev
	}
	require.Contains(t, events, "step-start")
	require.NotContains(t, events["step-start"], "effort", "the fallback must not pose as what ran")
	require.Equal(t, effort.SourceTable, events["step-start"]["effort_source"])
	require.Equal(t, "claude-opus-5-5", events["step-end"]["model_observed"])
	require.Equal(t, "medium", events["step-end"]["effort"])
}
