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

// D319, the D313a residual: a gate script that reads a check list outside
// the gate's own files — eval's toy, `while read … < scripts/checks/m1.checks`
// from a gate folder — is the notice gate.data-outside; an input from the
// gate's own folder, the work products a gate only names, a here-doc, a
// process substitution and an absolute path are not.
func TestGateDataOutsideNotice(t *testing.T) {
	notices := func(p *Project) []Diagnostic {
		var out []Diagnostic
		for _, n := range p.Notices() {
			if n.Code == CodeGateDataOutside {
				out = append(out, n)
			}
		}
		return out
	}
	p := selfCheckProject(t, "sh development/gates/M1-skel/g.sh", map[string]string{
		"development/gates/M1-skel/g.sh": "#!/bin/sh\n# reads < scripts/checks/ignored.checks in a comment\n" +
			"while read -r c; do sh -c \"$c\" || exit 1; done < scripts/checks/m1.checks\n" +
			"while read -r c; do :; done < \"$(dirname \"$0\")/own.checks\"\n" +
			"grep -q wave README.md < /dev/null\ncat <<EOF\nx\nEOF\ndiff <(sort a) b\n" +
			"while read -r c; do :; done <'scripts/checks/m1.checks'\n",
		"development/gates/M1-skel/own.checks": "true\n",
		"scripts/checks/m1.checks":             "grep -q wave greet.sh\n",
		"scripts/checks/ignored.checks":        "true\n",
		"README.md":                            "# r\n",
	})
	got := notices(p)
	require.Len(t, got, 1, "one notice per file, the own check list and the work product not named")
	n := got[0]
	require.Equal(t, ActionPerson, n.Action)
	require.Equal(t, "development/gates/M1-skel/g.sh", n.File)
	require.Equal(t, 3, n.Line)
	require.Equal(t, "M1's gate script development/gates/M1-skel/g.sh reads scripts/checks/m1.checks (`< scripts/checks/m1.checks`), which is not one of the gate's own files, "+
		"so the approval does not record it: an edit to it is no gate drift and no change.touches-gate (D313a)", n.Message)
	require.Equal(t, "if it is the gate's own check list, move it into development/gates/M1-<slug>/ and read it from there, "+
		"or declare it in the script (`# ape: gate-files scripts/checks/m1.checks`), before it is approved; "+
		"if it is a work product the gate checks, leave it", n.Fix)

	// Moved into the gate's folder, it is the gate's own: recorded, no notice.
	q := selfCheckProject(t, "sh development/gates/M1-skel/g.sh", map[string]string{
		"development/gates/M1-skel/g.sh":      "#!/bin/sh\nwhile read -r c; do :; done < development/gates/M1-skel/m1.checks\n",
		"development/gates/M1-skel/m1.checks": "true\n",
	})
	require.Empty(t, notices(q))
	require.Contains(t, keysOf(q.gateOwnFiles(q.roadmap().Find("M1"))), "development/gates/M1-skel/m1.checks")

	// A script in its own folder reading a list beside it is recorded (D313a
	// (iii)).
	r := selfCheckProject(t, "./scripts/g.sh", map[string]string{
		"scripts/g.sh": "#!/bin/sh\nwhile read -r c; do :; done < scripts/checks/g.checks\n", "scripts/checks/g.checks": "true\n",
	})
	require.Empty(t, notices(r))
}

// D581 (FINDING-C1-1): a gate's scratch — a file git ignores under its own
// folder, ledger's <gate>/.work/io/err — is not one of its own files: the
// green run that rewrites it is no drift. What can change what the gate
// does stays frozen though ignored: a file it sources or declares, and a
// script (an executable bit, a #! line, a script's extension); a tracked
// file is frozen as before.
func TestGateOwnFiles_IgnoredScratchIsNotDrift(t *testing.T) {
	plain := func(t *testing.T, p *Project, rel, content string) {
		t.Helper()
		write(t, p, rel, content)
		require.NoError(t, os.Chmod(filepath.Join(p.Root, filepath.FromSlash(rel)), 0o644))
	}
	const dir = "development/gates/M1-skel/"
	setup := func(t *testing.T) (*Project, *Milestone) {
		t.Helper()
		p := selfCheckProject(t, "./"+dir+"gate.sh", map[string]string{
			".gitignore": "development/gates/*/.work/\n",
			dir + "gate.sh": "#!/bin/bash\nhere=$(cd \"$(dirname \"$0\")\" && pwd)\n. \"$here/../lib/check.sh\"\n" +
				". \"$here/.work/env\"\nbash \"$here/.work/helper\"\n\"$here/.work/run\" >\"$here/.work/io/out\" 2>\"$here/.work/io/err\"\nexit 1\n",
			"development/gates/lib/check.sh": "W=$1/.work\n",
			dir + "checks":                   "a\n",
		})
		plain(t, p, "development/gates/lib/check.sh", "W=$1/.work\n")
		plain(t, p, dir+"checks", "a\n")
		plain(t, p, dir+".work/io/err", "red\n")
		plain(t, p, dir+".work/io/out", "")
		plain(t, p, dir+".work/j/example.journal", "j\n")
		plain(t, p, dir+".work/env", "X=1\n")                // sourced: frozen
		plain(t, p, dir+".work/helper", "#!/bin/sh\ntrue\n") // a #! line: frozen
		write(t, p, dir+".work/run", "true\n")               // executable: frozen
		plain(t, p, dir+".work/lib.sh", "true\n")            // a script's extension: frozen
		commitAll(t, p, "gate")
		return p, p.roadmap().Find("M1")
	}
	t.Run("the field case: the gate's own output under an ignored .work/", func(t *testing.T) {
		p, m := setup(t)
		a := p.newApproval(m, "ann")
		require.Equal(t, []string{
			dir + ".work/env", dir + ".work/helper", dir + ".work/lib.sh", dir + ".work/run", dir + "checks", "development/gates/lib/check.sh",
		}, sortedKeys(keysOf(a.GateOwnFiles)), "the scratch is not recorded; what can run, and what is tracked, is")
		plain(t, p, dir+".work/io/err", "")
		plain(t, p, dir+".work/io/out", "ok\t1\n")
		require.NoError(t, os.Remove(filepath.Join(p.Root, filepath.FromSlash(dir+".work/j/example.journal"))))
		plain(t, p, dir+".work/io/new", "x\n")
		require.Empty(t, p.gateDrift(m, a), "a green run rewrote its scratch")
		require.False(t, p.gateScriptUnrecorded(m, a), "new scratch is not an unrecorded own file")
	})
	t.Run("an approval recorded before v1.4 that froze the scratch", func(t *testing.T) {
		p, m := setup(t)
		a := p.newApproval(m, "ann")
		a.ScratchExcluded = false
		a.GateOwnFiles[dir+".work/io/err"] = "66bb1ab403006ec3c01ed55317731f8a3f7d2a22eae18c0187a48f2a2e67bef0"
		a.GateOwnFiles[dir+".work/j/example.journal"] = "0"
		require.Empty(t, p.gateDrift(m, a), "judged now: ignored, not a script, not sourced")
		plain(t, p, dir+"checks", "weakened\n")
		plain(t, p, dir+".work/env", "X=2\n")
		require.Equal(t, []string{dir + ".work/env changed", dir + "checks changed"}, p.gateDrift(m, a), "what it tracks and sources is still compared")
	})
	t.Run("a tracked file under the gate folder stays frozen", func(t *testing.T) {
		p, m := setup(t)
		a := p.newApproval(m, "ann")
		plain(t, p, dir+"checks", "weakened\n")
		plain(t, p, "development/gates/lib/check.sh", "W=/tmp\n")
		require.Equal(t, []string{dir + "checks changed", "development/gates/lib/check.sh changed"}, p.gateDrift(m, a))
	})
	t.Run("a tracked file git's rules would ignore stays frozen", func(t *testing.T) {
		p, m := setup(t)
		run(t, p.Root, "add", "-f", dir+".work/io/err")
		run(t, p.Root, "commit", "-q", "-m", "track it")
		a := p.newApproval(m, "ann")
		require.Contains(t, a.GateOwnFiles, dir+".work/io/err")
		plain(t, p, dir+".work/io/err", "")
		require.Equal(t, []string{dir + ".work/io/err changed"}, p.gateDrift(m, a))
	})
	t.Run("an ignored file the gate runs or sources stays frozen", func(t *testing.T) {
		for _, f := range []string{".work/env", ".work/helper", ".work/run", ".work/lib.sh"} {
			p, m := setup(t)
			a := p.newApproval(m, "ann")
			plain(t, p, dir+f, "exit 0\n")
			require.Equal(t, []string{dir + f + " changed"}, p.gateDrift(m, a), f+": its x bit or #! line removed in the same edit")
		}
	})
	t.Run("a declared ignored file stays frozen", func(t *testing.T) {
		p, m := setup(t)
		write(t, p, dir+"gate.sh", "#!/bin/bash\n# ape: gate-files "+dir+".work/expected\nexit 1\n")
		plain(t, p, dir+".work/expected", "ok\n")
		a := p.newApproval(m, "ann")
		require.Contains(t, a.GateOwnFiles, dir+".work/expected")
		plain(t, p, dir+".work/expected", "anything\n")
		require.Equal(t, []string{dir + ".work/expected changed"}, p.gateDrift(m, a))
	})
	t.Run("an ignored file a recorded glob matches later is new gate input all the same", func(t *testing.T) {
		p, m := setup(t)
		write(t, p, dir+"gate.sh", "#!/bin/bash\nfor c in "+dir+"*.checks; do :; done\nexit 1\n")
		plain(t, p, dir+"a.checks", "a\n")
		commitAll(t, p, "checks")
		a := p.newApproval(m, "ann")
		require.Equal(t, []string{dir + "*.checks"}, a.GateGlobs)
		require.NoError(t, os.WriteFile(filepath.Join(p.Root, ".gitignore"), []byte("development/gates/*/.work/\n*.local.checks\n"), 0o644))
		commitAll(t, p, "ignore local checks")
		a = p.newApproval(m, "ann")
		plain(t, p, dir+"mine.local.checks", "x\n")
		plain(t, p, dir+"b.checks", "b\n")
		require.Equal(t, []string{dir + "b.checks is new (it matches " + dir + "*.checks)", dir + "mine.local.checks is new (it matches " + dir + "*.checks)"}, p.gateDrift(m, a))
	})
}
