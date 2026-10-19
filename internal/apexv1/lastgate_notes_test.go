package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// D170 A9: ape status --json milestones[] carries last_gate {sha, exit, at,
// log} once the gate ran, and omits it before.
func TestStatus_MilestoneLastGate(t *testing.T) {
	files := baseFiles()
	files["gate.sh"] = "#!/bin/sh\nexit 0\n"
	p := fixture(t, files)
	r := p.Status()
	require.NotEmpty(t, r.Milestones)
	require.Nil(t, r.Milestones[0].LastGate, "no gate run yet")

	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	r = p.Status()
	lg := r.Milestones[0].LastGate
	require.NotNil(t, lg)
	require.Equal(t, g.Run.SHA, lg.SHA, "the commit the gate ran at (its result commit comes after)")
	require.NotEmpty(t, lg.SHA)
	require.Equal(t, g.Run.Exit, lg.Exit)
	require.Equal(t, g.Run.At, lg.At)
	require.Equal(t, g.Run.Log, lg.Log)
	require.NotEmpty(t, lg.Log)
}

// D170 A10: free-form `Gate checks:` / `Gate left:` lines in a change's
// ## Notes pass change check and survive the archive unchanged.
func TestChange_NotesGateLinesPassCheckAndArchive(t *testing.T) {
	notes := "Gate checks: gate-M1 greets a name (scripts/gate.sh line 3)\nGate left: the farewell check, M2\n"
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", waveChange("add-wave")+notes)
	commitAll(t, p, "plan: add-wave")
	require.Empty(t, findingMessages(p.CheckChange("add-wave")))
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "development/changes/add-wave/review.md", "fine\n")
	_, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.NoError(t, err)
	_, err = p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(res.ArchivedTo), "change.md"))
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(string(data), "## Notes\n"+notes), "the Notes lines are archived as written:\n%s", data)
}
