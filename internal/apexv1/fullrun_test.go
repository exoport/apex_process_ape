package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fullRunProject is an approved M1 whose spec has one full scenario and a
// regression command, each counting its runs in a file outside the tree.
func fullRunProject(t *testing.T) (p *Project, runs func() (full, suite int)) {
	t.Helper()
	counts := t.TempDir()
	t.Setenv("FULL_COUNT", filepath.Join(counts, "full"))
	t.Setenv("SUITE_COUNT", filepath.Join(counts, "suite"))
	files := baseFiles()
	files["_apex/config.yaml"] = "project_name: greeter\nregression: ./suite.sh\n"
	files["suite.sh"] = "#!/bin/sh\necho run >> \"$SUITE_COUNT\"\n"
	files["svc.sh"] = "#!/bin/sh\necho run >> \"$FULL_COUNT\"\n"
	files["gate.sh"] = "#!/bin/sh\n[ ! -f gate.red ]\n"
	files["development/specs/greeting/spec.md"] = liveSpec
	p = fixture(t, files)
	n := func(name string) int {
		data, _ := os.ReadFile(filepath.Join(counts, name))
		return strings.Count(string(data), "run")
	}
	return p, func() (int, int) { return n("full"), n("suite") }
}

// v1.4 (D563): the gate runs the full checks and the regression command
// once its Gate: command is green, records them as the full run, and takes
// a current one the next time; a commit, --rerun or a red Gate: command
// change that; close takes it too; `ape milestone full` records one.
func TestGate_FullRunIsRecordedAndReused(t *testing.T) {
	p, runs := fullRunProject(t)
	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.True(t, g.Run.Green(), g.Run.Why())
	require.Empty(t, g.Run.FullRunReused)
	full, suite := runs()
	require.Equal(t, 1, full)
	require.Equal(t, 1, suite)
	fr := p.LoadFullRun("M1")
	require.NotNil(t, fr)
	require.Equal(t, "gate", fr.By)

	g, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.True(t, g.Run.Green())
	require.Equal(t, fr.At, g.Run.FullRunReused, "the same tree: taken, not run")
	full, suite = runs()
	require.Equal(t, 1, full)
	require.Equal(t, 1, suite)
	require.NotNil(t, g.Run.Regression, "the reused regression verdict is reported")

	_, err = p.RunGate(ctx(), "M1", RunOptions{Rerun: true})
	require.NoError(t, err)
	full, _ = runs()
	require.Equal(t, 2, full, "--rerun runs them")

	write(t, p, "README.md", "# greeter\n")
	commitAll(t, p, "docs: readme")
	g, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Empty(t, g.Run.FullRunReused, "a commit that changed a file: run again")
	full, _ = runs()
	require.Equal(t, 3, full)

	write(t, p, "notes.txt", "x\n")
	_, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	full, _ = runs()
	require.Equal(t, 4, full, "an untracked file changes the tree: run again")
	require.NoError(t, os.Remove(filepath.Join(p.Root, "notes.txt")))

	// A red Gate: command skips the full run and records none.
	write(t, p, "gate.red", "")
	commitAll(t, p, "red")
	before := p.LoadFullRun("M1").At
	g, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.False(t, g.Run.Green())
	require.Equal(t, 1, g.Run.FullSkipped)
	require.Equal(t, before, p.LoadFullRun("M1").At)
	require.NoError(t, os.Remove(filepath.Join(p.Root, "gate.red")))
	commitAll(t, p, "green")

	// On demand, then a gate takes it.
	fr, err = p.RunFull(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "full", fr.By)
	full, _ = runs()
	g, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, fr.At, g.Run.FullRunReused)
	again, _ := runs()
	require.Equal(t, full, again)
}

// A red full run is never taken: it may be red for what is not in the
// tree, so the next gate runs the full checks again.
func TestRunFull_RedIsMilestoneFullRedAndNotReused(t *testing.T) {
	p, runs := fullRunProject(t)
	write(t, p, "svc.sh", "#!/bin/sh\necho run >> \"$FULL_COUNT\"\n[ -f svc.ok ]\n")
	commitAll(t, p, "svc needs svc.ok")
	fr, err := p.RunFull(ctx(), "M1", RunOptions{})
	require.Equal(t, CodeFullRed, failCode(t, err))
	require.False(t, fr.Green())
	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.False(t, g.Run.Green())
	require.Empty(t, g.Run.FullRunReused, "a red full run is run again")
	full, _ := runs()
	require.Equal(t, 2, full)
	require.Len(t, g.Run.SpecFailed, 1)
}
