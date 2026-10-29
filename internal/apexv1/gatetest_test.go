package apexv1

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// v1.4 (D543 B2): a session's Bash may not run a gate folder's plain
// gate.sh — in any of the forms the guards parse — but its stub run, and
// any other gate.sh, stay allowed.
func TestGateGuard(t *testing.T) {
	t.Setenv(EnvAllowGate, "")
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath(root), []byte("project_name: x\n"), 0o644))
	gates := filepath.Join(root, "development", "gates", "M4-time-entries")
	require.NoError(t, os.MkdirAll(gates, 0o755))
	for cmd, deny := range map[string]bool{
		"./development/gates/M4-time-entries/gate.sh":                                                    true,
		"timeout 300 ./development/gates/M4-time-entries/gate.sh":                                        true,
		"APE_MILESTONE_BASE=HEAD timeout 300 ./development/gates/M4-time-entries/gate.sh 2>&1 | tail -5": true,
		"bash development/gates/M4-time-entries/gate.sh":                                                 true,
		"sh -x development/gates/M4-time-entries/gate.sh":                                                true,
		`sh -c "./development/gates/M4-time-entries/gate.sh; echo $?"`:                                   true,
		// As a session's shell writes it: forward slashes (Git Bash on
		// Windows reads a backslash as an escape, as the guard does).
		filepath.ToSlash(root) + "/development/gates/M4-time-entries/gate.sh": true,
		"go build ./... && ./development/gates/M4-time-entries/gate.sh":       true,
		// allowed
		"./development/gates/M4-time-entries/gate.sh --stub development/gates/M4-time-entries/stub": false,
		"./development/gates/M4-time-entries/gate.sh --stub=development/gates/M4-time-entries/stub": false,
		"timeout 60 ./development/gates/M4-time-entries/gate.sh --stub ./stub":                      false,
		"ape milestone gate-test development/gates/M4-time-entries":                                 false,
		"ape milestone gate M4":                             false,
		"cat development/gates/M4-time-entries/gate.sh":     false,
		"bash -n development/gates/M4-time-entries/gate.sh": false,
		"sh -xn development/gates/M4-time-entries/gate.sh":  false,
		"./scripts/gate.sh":                                 false,
		"./development/gates/lib/gate.sh":                   false,
	} {
		d := GateGuard(cmd, root)
		require.Equal(t, deny, d.Deny, cmd)
		if deny {
			require.Contains(t, d.Reason, "`ape milestone gate-test development/gates/M4-time-entries`", cmd)
		}
	}
	// From inside the folder.
	require.True(t, GateGuard("./gate.sh", gates).Deny)
	require.False(t, GateGuard("./gate.sh --stub stub", gates).Deny)

	t.Setenv(EnvAllowGate, "1")
	require.False(t, GateGuard("./development/gates/M4-time-entries/gate.sh", root).Deny, "the launching environment's override")
}

// gateTestProject is a v1 project with a gate folder M1-skel whose gate
// runs ./app.sh (or the stub), and a stub that answers.
func gateTestProject(t *testing.T, gate string) (p *Project, folder string) {
	t.Helper()
	p = fixture(t, baseFiles())
	dir := "development/gates/M1-skel"
	write(t, p, dir+"/gate.sh", gate)
	write(t, p, dir+"/stub", "#!/bin/sh\necho \"hi $1\"\n")
	return p, filepath.Join(p.Root, dir)
}

const stubbedGate = "#!/bin/sh\nwork=./app.sh\n[ \"$1\" = --stub ] && work=$2\nout=$($work hello) || exit 1\n[ \"$out\" = \"hi hello\" ]\n"

func TestGateTest(t *testing.T) {
	p, dir := gateTestProject(t, stubbedGate)
	res, err := p.GateTest(ctx(), dir, "", nil)
	require.NoError(t, err)
	require.True(t, res.OK)
	require.Equal(t, "M1", res.Milestone)
	require.Len(t, res.Runs, 2)
	require.Equal(t, "stub", res.Runs[0].Mode)
	require.Equal(t, 0, res.Runs[0].Exit)
	require.Equal(t, "plain", res.Runs[1].Mode)
	require.NotZero(t, res.Runs[1].Exit)
	before, _ := p.LoadMilestoneRecords()

	// The bare folder name, as the long run's worker passes it.
	res, err = p.GateTest(ctx(), "M1-skel", "", nil)
	require.NoError(t, err)
	require.True(t, res.OK)

	// The work exists: the plain gate is green, which proves nothing.
	write(t, p, "app.sh", "#!/bin/sh\necho \"hi $1\"\n")
	_, err = p.GateTest(ctx(), dir, "", nil)
	require.Equal(t, CodeGateGreen, failCode(t, err))
	require.NoError(t, os.Remove(filepath.Join(p.Root, "app.sh")))

	// A stub that answers wrong: the gate or the stub is wrong.
	write(t, p, "development/gates/M1-skel/stub", "#!/bin/sh\necho nope\n")
	res, err = p.GateTest(ctx(), dir, "", nil)
	require.Equal(t, CodeGateStubRed, failCode(t, err))
	require.Equal(t, "fail", res.Runs[0].Verdict)

	// A gate that cannot run.
	write(t, p, "development/gates/M1-skel/gate.sh", "#!/bin/sh\nif then\n")
	_, err = p.GateTest(ctx(), dir, "", nil)
	require.Equal(t, CodeGateBroken, failCode(t, err))

	after, _ := p.LoadMilestoneRecords()
	require.Equal(t, before, after, "a self-test records nothing")

	_, err = p.GateTest(ctx(), filepath.Join(p.Root, "development"), "", nil)
	require.ErrorContains(t, err, "is not a gate folder")
}

// What a plain gate leaves running in its group is ended (F15), as in any
// check ape runs.
func TestGateTest_EndsLeftovers(t *testing.T) {
	p, dir := gateTestProject(t, "#!/bin/sh\nsleep 300 &\nexit 1\n")
	require.NoError(t, os.Remove(filepath.Join(dir, "stub")))
	res, err := p.GateTest(ctx(), dir, "", nil)
	require.NoError(t, err)
	require.Len(t, res.Runs, 1, "no stub: the plain run only")
	require.Len(t, res.Runs[0].Leftovers, 1)
	require.Contains(t, res.Runs[0].Leftovers[0], "sleep 300")
}
