package apexv1

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// RK-6 R1 (Q146): the approval records the gate's own files — what its
// scripts source, its gate folders, the files a script names under its
// own folder — and drift compares them; the files a gate checks (its
// work products) stay out, as since rc.2 (D53).
func TestGateOwnFiles_DriftOnTheGatesOwnFilesOnly(t *testing.T) {
	t.Run("atomix's layout: scripts/ with a sourced lib and a checks file", func(t *testing.T) {
		p := selfCheckProject(t, "./scripts/g.sh", map[string]string{
			"scripts/g.sh":            "#!/bin/sh\n. \"$(dirname \"$0\")/lib/gate.sh\"\n# the checks are in scripts/checks/g.checks.\nrun_checks scripts/checks/g.checks\ngrep -q wave README.md && test -f docs/a.md\n",
			"scripts/lib/gate.sh":     "run_checks() { while read -r c; do sh -c \"$c\" || return 1; done < \"$1\"; }\n",
			"scripts/checks/g.checks": "grep -q wave greet.sh\n",
			"scripts/other.sh":        "#!/bin/sh\necho not named by the gate\n",
			"README.md":               "# r\n",
			"docs/a.md":               "a\n",
		})
		m := p.roadmap().Find("M1")
		a := p.newApproval(m, "ann")
		require.Equal(t, []string{"scripts/g.sh"}, sortedKeys(keysOf(a.GateFiles)), "gate_files: the scripts, as before")
		require.Equal(t, []string{"scripts/checks/g.checks", "scripts/lib/gate.sh"}, sortedKeys(keysOf(a.GateOwnFiles)))
		require.Empty(t, p.gateDrift(m, a))
		write(t, p, "README.md", "# r\nwave\n")
		write(t, p, "docs/a.md", "b\n")
		write(t, p, "scripts/other.sh", "#!/bin/sh\necho changed\n")
		require.Empty(t, p.gateDrift(m, a), "the work products a gate checks, and a script it does not name, are not drift")
		write(t, p, "scripts/checks/g.checks", "true\n")
		require.Equal(t, []string{"scripts/checks/g.checks changed"}, p.gateDrift(m, a))
		write(t, p, "scripts/checks/g.checks", "grep -q wave greet.sh\n")
		write(t, p, "scripts/lib/gate.sh", "run_checks() { :; }\n")
		require.Equal(t, []string{"scripts/lib/gate.sh changed"}, p.gateDrift(m, a))
	})
	t.Run("a gate folder and gates/lib", func(t *testing.T) {
		p := selfCheckProject(t, "bash development/gates/M1-skel/gate.sh", map[string]string{
			"development/gates/M1-skel/gate.sh": "#!/bin/bash\nexit 1\n",
			"development/gates/M1-skel/checks":  "a\n",
			"development/gates/M1-skel/stub/x":  "s\n",
			"development/gates/lib/common.sh":   "x=1\n",
			"development/gates/M2-next/gate.sh": "#!/bin/sh\nexit 1\n",
		})
		m := p.roadmap().Find("M1")
		a := p.newApproval(m, "ann")
		require.Equal(t, []string{"development/gates/M1-skel/checks", "development/gates/M1-skel/stub/x", "development/gates/lib/common.sh"}, sortedKeys(keysOf(a.GateOwnFiles)),
			"its folder and lib/, not another milestone's, and not the script gate_files holds")
		require.NoError(t, os.Remove(filepath.Join(p.Root, "development", "gates", "M1-skel", "stub", "x")))
		require.Equal(t, []string{"development/gates/M1-skel/stub/x is gone"}, p.gateDrift(m, a))
	})
	t.Run("a gate script at the root names nothing of its own", func(t *testing.T) {
		p := selfCheckProject(t, "./gate.sh", map[string]string{"gate.sh": "#!/bin/sh\ngrep -q x tools/data.txt\n", "tools/data.txt": "x\n"})
		require.Empty(t, p.gateOwnFiles(p.roadmap().Find("M1")), "the root is every file's folder: its words are work products")
	})
	t.Run("an approval recorded before rc.7o", func(t *testing.T) {
		p := selfCheckProject(t, "./scripts/g.sh", map[string]string{"scripts/g.sh": "#!/bin/sh\nrun_checks scripts/checks/g.checks\n", "scripts/checks/g.checks": "true\n"})
		m := p.roadmap().Find("M1")
		a := p.newApproval(m, "ann")
		a.GateOwnFiles = nil
		write(t, p, "scripts/checks/g.checks", "false\n")
		require.Empty(t, p.gateDrift(m, a), "not drift: never recorded")
		require.True(t, p.gateScriptUnrecorded(m, a), "the next re-approval records it")
	})
}

// RK-6 R2: a change whose own commits edit its milestone's approved gate —
// a script gate_files holds, or an own file gate_own_files holds — is
// refused by verify (and archive, which checks it the same way before
// it rebases; action replan); other code is not.
func TestVerify_RefusesAChangeThatEditsItsGate(t *testing.T) {
	files := baseFiles()
	files["development/gates/lib/common.sh"] = "x=1\n"
	files["gate.sh"] = "#!/bin/sh\ntest -f done.flag\n"
	p := fixture(t, files)
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Contains(t, recs["M1"].Approval.GateOwnFiles, "development/gates/lib/common.sh")
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)

	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err, "code that is not the gate")

	for _, f := range []string{"gate.sh", "development/gates/lib/common.sh"} {
		before := read(t, p, f)
		write(t, p, f, before+"# weakened\n")
		commitAll(t, p, "feat: touch "+f)
		_, err = p.Verify(ctx(), "add-wave", RunOptions{})
		require.Equal(t, CodeTouchesGate, failCode(t, err), f)
		require.ErrorContains(t, err, "change add-wave edits a gate a person approved: "+f+" (M1's gate)")
		require.Equal(t, ActionReplan, Diagnostics(err)[0].Action)
		write(t, p, f, before)
		commitAll(t, p, "fix: restore "+f)
	}
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err, "restored: the diff from base no longer touches the gate")
}
