package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func refs(cs []PlannedCheck) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Ref)
	}
	return out
}

// Backlog item 2: the effective spec is every current requirement the
// change leaves alone, plus the scenarios of its own ADDED/MODIFIED blocks.
func TestEffectiveSpecChecks(t *testing.T) {
	p := fixture(t, baseFiles())
	write(t, p, "development/specs/cli/spec.md", "# Cli\nPurpose: p\n\n### Requirement: Help\nx\n#### Scenario: help\n- check: ./greet.sh --help\n#### Scenario: looks nice\n- check: human-review\n")
	c := ParseChange("", changeDoc("rework", "greeting",
		"### MODIFIED [greeting] Requirement: Say hello\nThe system SHALL print Hello, loudly.\n#### Scenario: plain run\n- check: ./greet.sh | grep -q Hello\n#### Scenario: loud\n- check: ./greet.sh | grep -q '!'\n\n"+
			"### REMOVED [greeting] Requirement: Say name\nReason: unused\n\n"+addedWave,
		"- T1 x — check: true\n"))
	require.Equal(t, []string{
		"cli/Help/help",
		"greeting/Say hello/plain run", "greeting/Say hello/loud", "greeting/Wave/wave",
	}, refs(p.EffectiveSpecChecks(c)), "Say name is removed, Say hello comes from the delta, human-review is not run")
	all := p.EffectiveSpecChecks(c)
	require.False(t, all[0].Delta)
	require.True(t, all[1].Delta)

	require.Equal(t, []string{
		"cli/Help/help", "greeting/Say hello/plain run", "greeting/Say name/named",
	}, refs(p.EffectiveSpecChecks(nil)), "no change: the whole current spec")

	checks := p.ChangeChecks(c)
	require.Equal(t, "T1", checks[0].Ref)
	require.Equal(t, KindTask, checks[0].Kind)
}

// A command shared by several checks runs once per run.
func TestRunChecks_SharedCommandRunsOnce(t *testing.T) {
	p := fixture(t, baseFiles())
	counter := filepath.Join(t.TempDir(), "count")
	cmd := "echo x >> " + counter
	out := p.RunChecks(ctx(), p.LogDir("x"), "", []PlannedCheck{
		{Ref: "T1", Kind: KindTask, Cmd: cmd},
		{Ref: "greeting/A/a", Kind: KindScenario, Cmd: cmd},
		{Ref: "greeting/A/b", Kind: KindScenario, Cmd: "true"},
	}, RunOptions{})
	require.Len(t, out, 3)
	data, err := os.ReadFile(counter)
	require.NoError(t, err)
	require.Equal(t, "x\n", string(data))
	require.Equal(t, "greeting/A/a", out[1].Task)
	require.Equal(t, KindScenario, out[1].Kind)
}

// A regression in a requirement the change never touched fails its verify.
func TestVerify_RunsTheEffectiveSpec(t *testing.T) {
	p := startedWave(t)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	write(t, p, "greet.sh", "#!/bin/sh\necho Hi $1\n") // breaks "Say hello"
	commitAll(t, p, "feat: wave, and a regression")
	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.False(t, v.Green, "every task check is green, the spec is not")
	failing := v.Failing()
	require.Len(t, failing, 1)
	require.Equal(t, "greeting/Say hello/plain run", failing[0].Task)
	require.False(t, failing[0].Delta)
	require.FileExists(t, filepath.Join(p.Root, filepath.FromSlash(failing[0].Log)))
	require.True(t, strings.HasPrefix(failing[0].Log, "_apex/state/logs/add-wave/S-"))
}

// The milestone gate runs the whole spec as its regression suite: a green
// gate command does not close a milestone whose spec has a red scenario.
func TestGate_RunsTheSpecAsRegressionSuite(t *testing.T) {
	files := baseFiles()
	files["gate.sh"] = "#!/bin/sh\nexit 0\n"
	p := fixture(t, files)
	write(t, p, "greet.sh", "#!/bin/sh\necho Hi $1\n")
	commitAll(t, p, "break hello")
	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, 0, g.Run.Exit)
	require.Equal(t, 2, g.Run.SpecChecks)
	require.False(t, g.Run.Green())
	require.Contains(t, g.Run.Why(), "1 spec scenario(s) red: greeting/Say hello/plain run")
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.ErrorContains(t, err, "spec scenario(s) red")

	write(t, p, "greet.sh", "#!/bin/sh\necho Hello $1\n")
	commitAll(t, p, "fix hello")
	g, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.True(t, g.Run.Green())
	require.Equal(t, MilestoneDone, g.Status)
}
