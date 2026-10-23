package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// v1.2 F4 (D433, D434): the forms of a gate's own files v1.1 missed —
// a script-dir variable, BASH_SOURCE, a sourced file read in turn, a glob,
// a declaration — are recorded; the files a gate checks still are not
// (D53).

// ownOf is the own files and globs newApproval records for M1 of p.
func ownOf(t *testing.T, p *Project) (own, globs []string) {
	t.Helper()
	a := p.newApproval(p.roadmap().Find("M1"), "ann")
	return sortedKeys(keysOf(a.GateOwnFiles)), a.GateGlobs
}

func TestGateOwnFiles_ScriptDirVariable(t *testing.T) {
	cases := []struct {
		name, script string
		want         []string
	}{
		{
			"cd dirname pwd", "here=$(cd \"$(dirname \"$0\")\" && pwd)\n. \"$here/lib/gate.sh\"\nrun_checks \"$here\"/checks/one.checks\n",
			[]string{"scripts/checks/one.checks", "scripts/lib/gate.sh"},
		},
		{
			"braced, pwd -P", "here=\"$(cd \"$(dirname \"$0\")\" && pwd -P)\"\nsource \"${here}/lib/gate.sh\"\n",
			[]string{"scripts/lib/gate.sh"},
		},
		{
			"dirname alone, exported", "export GATE_DIR=$(dirname \"$0\")\nrun < \"$GATE_DIR/checks/one.checks\"\n",
			[]string{"scripts/checks/one.checks"},
		},
		{
			"realpath", "here=$(dirname \"$(realpath \"$0\")\")\ncat \"$here/checks/one.checks\"\n",
			[]string{"scripts/checks/one.checks"},
		},
		{
			"readlink -f, then cd", "here=$(cd \"$(dirname \"$(readlink -f \"$0\")\")\" && pwd)\ncat $here/checks/two.checks\n",
			[]string{"scripts/checks/two.checks"},
		},
		{"${0%/*}", "d=${0%/*}\ncat \"$d/checks/one.checks\"\n", []string{"scripts/checks/one.checks"}},
		{"a variable built from one", "here=$(dirname \"$0\")\nchecks=$here/checks\ncat \"$checks/one.checks\"\n", []string{"scripts/checks/one.checks"}},
		{"assigned and used on one line", "GATE_DIR=$(dirname \"$0\"); while read -r l; do :; done < \"$GATE_DIR/checks/one.checks\"\n", []string{"scripts/checks/one.checks"}},
		{"reassigned to something else", "here=$(dirname \"$0\")\nhere=/tmp\ncat \"$here/checks/one.checks\"\n", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := selfCheckProject(t, "bash scripts/gate.sh", map[string]string{
				"scripts/gate.sh":           "#!/bin/bash\n" + c.script + "exit 1\n",
				"scripts/lib/gate.sh":       "run_checks() { :; }\n",
				"scripts/checks/one.checks": "a\ttrue\n",
				"scripts/checks/two.checks": "b\ttrue\n",
			})
			own, globs := ownOf(t, p)
			require.Equal(t, c.want, nilIfEmpty(own))
			require.Empty(t, globs)
		})
	}

	t.Run("$V/.. is the parent folder, under the folder rule", func(t *testing.T) {
		p := selfCheckProject(t, "bash tools/gate/m1.sh", map[string]string{
			"tools/gate/m1.sh":      "#!/bin/bash\nhere=$(cd \"$(dirname \"$0\")\" && pwd)\ncat \"$here/../checks/a.checks\"\ngrep -q x \"$here/../../README.md\"\n",
			"tools/checks/a.checks": "true\n",
			"README.md":             "# r\n",
		})
		own, _ := ownOf(t, p)
		require.Equal(t, []string{"tools/checks/a.checks"}, own, "tools/ is no root: its file is the gate's; the root's README.md is a work product")
	})
}

func TestGateOwnFiles_BashSource(t *testing.T) {
	for name, script := range map[string]string{
		"${BASH_SOURCE%/*}":                  ". \"${BASH_SOURCE%/*}/lib/gate.sh\"\n",
		"${BASH_SOURCE[0]%/*}":               ". \"${BASH_SOURCE[0]%/*}/lib/gate.sh\"\n",
		"dirname ${BASH_SOURCE[0]}":          ". \"$(dirname \"${BASH_SOURCE[0]}\")/lib/gate.sh\"\n",
		"cd dirname BASH_SOURCE &>/dev/null": "DIR=\"$(cd -- \"$(dirname -- \"${BASH_SOURCE[0]}\")\" &> /dev/null && pwd)\"\n. \"$DIR/lib/gate.sh\"\n",
		"$BASH_SOURCE":                       "source $(dirname $BASH_SOURCE)/lib/gate.sh\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := selfCheckProject(t, "bash scripts/gate.sh", map[string]string{
				"scripts/gate.sh":     "#!/bin/bash\n" + script + "exit 1\n",
				"scripts/lib/gate.sh": "run_checks() { :; }\n",
			})
			own, _ := ownOf(t, p)
			require.Equal(t, []string{"scripts/lib/gate.sh"}, own)
		})
	}
}

func TestGateOwnFiles_SourcedLibReadInTurn(t *testing.T) {
	gate := "#!/bin/bash\n. \"$(dirname \"$0\")/lib/gate.sh\"\nexit 1\n"
	t.Run("the lib reads a list by a variable: $0 there is the gate script", func(t *testing.T) {
		p := selfCheckProject(t, "bash scripts/gate.sh", map[string]string{
			"scripts/gate.sh":           gate,
			"scripts/lib/gate.sh":       "GATE_DIR=$(dirname \"$0\")\nwhile read -r l; do :; done < \"$GATE_DIR/checks/one.checks\"\n",
			"scripts/checks/one.checks": "a\ttrue\n",
			"scripts/checks/two.checks": "b\ttrue\n",
		})
		own, _ := ownOf(t, p)
		require.Equal(t, []string{"scripts/checks/one.checks", "scripts/lib/gate.sh"}, own)
	})
	t.Run("the lib names a list literally", func(t *testing.T) {
		p := selfCheckProject(t, "bash scripts/gate.sh", map[string]string{
			"scripts/gate.sh":           gate,
			"scripts/lib/gate.sh":       "cat scripts/checks/one.checks >/dev/null\n",
			"scripts/checks/one.checks": "a\ttrue\n",
		})
		own, _ := ownOf(t, p)
		require.Equal(t, []string{"scripts/checks/one.checks", "scripts/lib/gate.sh"}, own)
	})
	t.Run("BASH_SOURCE in the lib is the lib", func(t *testing.T) {
		p := selfCheckProject(t, "bash scripts/gate.sh", map[string]string{
			"scripts/gate.sh":      gate,
			"scripts/lib/gate.sh":  ". \"${BASH_SOURCE%/*}/more.sh\"\n",
			"scripts/lib/more.sh":  "cat \"$(dirname \"${BASH_SOURCE[0]}\")/data.txt\"\n",
			"scripts/lib/data.txt": "x\n",
		})
		own, _ := ownOf(t, p)
		require.Equal(t, []string{"scripts/lib/data.txt", "scripts/lib/gate.sh", "scripts/lib/more.sh"}, own)
	})
	t.Run("a variable set in the gate is seen in the lib, and one set in the lib in the gate", func(t *testing.T) {
		p := selfCheckProject(t, "bash scripts/gate.sh", map[string]string{
			"scripts/gate.sh":           "#!/bin/bash\nhere=$(dirname \"$0\")\n. \"$here/lib/gate.sh\"\ncat \"$LISTS/two.checks\"\nexit 1\n",
			"scripts/lib/gate.sh":       "cat \"$here/checks/one.checks\"\nLISTS=$here/checks\n",
			"scripts/checks/one.checks": "a\n",
			"scripts/checks/two.checks": "b\n",
		})
		own, _ := ownOf(t, p)
		require.Equal(t, []string{"scripts/checks/one.checks", "scripts/checks/two.checks", "scripts/lib/gate.sh"}, own)
	})
}

func TestGateOwnFiles_GlobFrozenAndNewMatchRefused(t *testing.T) {
	p := selfCheckProject(t, "bash scripts/gate.sh", map[string]string{
		"scripts/gate.sh":           "#!/bin/bash\nfor f in scripts/checks/*.checks; do cat \"$f\"; done\nexit 1\n",
		"scripts/checks/one.checks": "a\n",
		"scripts/checks/two.checks": "b\n",
		"scripts/checks/notes.md":   "not a list\n",
	})
	m := p.roadmap().Find("M1")
	a := p.newApproval(m, "ann")
	require.Equal(t, []string{"scripts/checks/one.checks", "scripts/checks/two.checks"}, sortedKeys(keysOf(a.GateOwnFiles)))
	require.Equal(t, []string{"scripts/checks/*.checks"}, a.GateGlobs)
	require.Empty(t, p.gateDrift(m, a))
	write(t, p, "scripts/checks/three.checks", "c\n")
	require.Equal(t, []string{"scripts/checks/three.checks is new (it matches scripts/checks/*.checks)"}, p.gateDrift(m, a))
	require.True(t, p.gateScriptUnrecorded(m, a))

	t.Run("a glob that matches nothing is no glob", func(t *testing.T) {
		q := selfCheckProject(t, "bash scripts/gate.sh", map[string]string{
			"scripts/gate.sh": "#!/bin/bash\ngrep -qE 'scripts/[a-z]+' x\nls scripts/none/*.x\nexit 1\n",
		})
		_, globs := ownOf(t, q)
		require.Empty(t, globs)
	})
	t.Run("an approval before v1.2 has no globs and loads as before", func(t *testing.T) {
		a := p.newApproval(m, "ann")
		a.GateGlobs = nil
		write(t, p, "scripts/checks/four.checks", "d\n")
		require.Empty(t, p.gateDrift(m, a), "never recorded: not drift")
		require.True(t, p.gateScriptUnrecorded(m, a), "the next re-approval records it")
	})
}

// A change that adds a file under a recorded glob is refused like one that
// edits a recorded file (change.touches-gate); a declared glob counts.
func TestVerify_RefusesAChangeThatAddsUnderAGateGlob(t *testing.T) {
	files := baseFiles()
	files["gate.sh"] = "#!/bin/sh\n# ape: gate-files scripts/checks/*.checks\ntest -f done.flag\n"
	files["scripts/checks/one.checks"] = "true\n"
	p := fixture(t, files)
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Equal(t, []string{"scripts/checks/*.checks"}, recs["M1"].Approval.GateGlobs)
	require.Contains(t, recs["M1"].Approval.GateOwnFiles, "scripts/checks/one.checks")
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "scripts/checks/easy.checks", "true\n")
	commitAll(t, p, "feat: an easier list")
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.Equal(t, CodeTouchesGate, failCode(t, err))
	require.ErrorContains(t, err, "scripts/checks/easy.checks (M1's gate: it matches scripts/checks/*.checks)")
}

func TestGateOwnFiles_Declared(t *testing.T) {
	p := selfCheckProject(t, "./gate.sh", map[string]string{
		"gate.sh": "#!/bin/sh\n# ape: gate-files docs/gate-input.txt checks/*.list\n" +
			"#   ape:  gate-files ../outside.txt /etc/passwd $HOME/x\nexit 1\n",
		"docs/gate-input.txt": "x\n",
		"docs/other.md":       "work\n",
	})
	own, globs := ownOf(t, p)
	require.Equal(t, []string{"docs/gate-input.txt"}, own, "declared: frozen wherever it lives; nothing outside the repo")
	require.Equal(t, []string{"checks/*.list"}, globs, "a declared glob is recorded with no match yet")
	m := p.roadmap().Find("M1")
	a := p.newApproval(m, "ann")
	write(t, p, "checks/a.list", "x\n")
	require.Equal(t, []string{"checks/a.list is new (it matches checks/*.list)"}, p.gateDrift(m, a))

	t.Run("in a sourced file", func(t *testing.T) {
		q := selfCheckProject(t, "bash scripts/gate.sh", map[string]string{
			"scripts/gate.sh":     "#!/bin/bash\n. scripts/lib/gate.sh\nexit 1\n",
			"scripts/lib/gate.sh": "# ape: gate-files data/expected.json\n",
			"data/expected.json":  "{}\n",
		})
		own, _ := ownOf(t, q)
		require.Equal(t, []string{"data/expected.json", "scripts/lib/gate.sh"}, own)
	})
}

func TestGateOwnFiles_CycleSafe(t *testing.T) {
	p := selfCheckProject(t, "bash scripts/gate.sh", map[string]string{
		"scripts/gate.sh":   "#!/bin/bash\n. \"$(dirname \"$0\")/lib/a.sh\"\nexit 1\n",
		"scripts/lib/a.sh":  ". \"${BASH_SOURCE%/*}/b.sh\"\n. \"${BASH_SOURCE%/*}/a.sh\"\n",
		"scripts/lib/b.sh":  ". scripts/lib/a.sh\n. scripts/lib/c.sh\n",
		"scripts/lib/c.sh":  ". scripts/lib/d.sh\n",
		"scripts/lib/d.sh":  ". scripts/lib/e.sh\n",
		"scripts/lib/e.sh":  "x=1\n",
		"scripts/other.sh":  "x=1\n",
		"scripts/unrelated": "x\n",
	})
	own, _ := ownOf(t, p)
	require.Equal(t, []string{"scripts/lib/a.sh", "scripts/lib/b.sh", "scripts/lib/c.sh", "scripts/lib/d.sh"}, own,
		"a cycle ends; three files deep are read (a, b, c), so d is frozen and what d sources is not")
}

func TestGateOwnFiles_RootWorkProductsStillExcluded(t *testing.T) {
	p := selfCheckProject(t, "bash scripts/gate.sh", map[string]string{
		"scripts/gate.sh": "#!/bin/bash\nhere=$(cd \"$(dirname \"$0\")\" && pwd)\n. \"$here/lib/gate.sh\"\n" +
			"grep -q wave \"$here/../README.md\"\ntest -f \"${0%/*}/../docs/a.md\"\nfor f in docs/*.md; do :; done\n" +
			"cat \"$here/../development/roadmap.md\"\nexit 1\n",
		"scripts/lib/gate.sh": "cat docs/a.md README.md\nls \"$(dirname \"$0\")\"/../docs/*.md\n",
		"README.md":           "# r\n",
		"docs/a.md":           "a\n",
	})
	own, globs := ownOf(t, p)
	require.Equal(t, []string{"scripts/lib/gate.sh"}, own, "D53: README.md, docs/, development/ are the milestone's work")
	require.Empty(t, globs)

	t.Run("a gate at the root names nothing of its own, even through $0", func(t *testing.T) {
		q := selfCheckProject(t, "./gate.sh", map[string]string{
			"gate.sh":   "#!/bin/sh\nhere=$(dirname \"$0\")\ngrep -q x \"$here/docs/a.md\"\nfor f in docs/*.md; do :; done\nexit 1\n",
			"docs/a.md": "a\n",
		})
		own, globs := ownOf(t, q)
		require.Empty(t, own)
		require.Empty(t, globs)
	})
}

// atomix's real M5 layout (v1.1 already recorded it; kept as the
// regression): scripts/release-v0-2-0.sh sources its lib through
// $(dirname "$0") and runs a literal checks file; the lib is a large shared
// seam whose comments and helpers name many paths outside scripts/.
func TestGateOwnFiles_AtomixLayout(t *testing.T) {
	lib := `#!/usr/bin/env bash
# scripts/lib/gate.sh -- the shared seam of the milestone gates (scripts/<name>.sh).
# A gate is a list of named checks (scripts/checks/<name>.checks, one
# "<id><TAB><command>" per line; '#' lines quote the source).
set -u
GATE_STUB=""
if [ "${1-}" = "--stub" ]; then GATE_STUB="${2-}"; fi
GATE_ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || exit 1
cd "$GATE_ROOT" || exit 1
# (briefs/2026-10-18-atomix-v0-2-0/2026-10-18-plan-review-decisions.md, finding 8)
GATE_ON_EXIT=()
gate_on_exit() { local i; for ((i = ${#GATE_ON_EXIT[@]} - 1; i >= 0; i--)); do ${GATE_ON_EXIT[i]}; done; }
trap gate_on_exit EXIT
# test/harness/evidence.go writes a dated record each run
gate_compose() { local p="$1"; shift; docker compose -f "deploy/compose/$p.yml" -f deploy/compose/client.yml "$@"; }
run_checks() {
	local file="$1" id cmd
	while IFS=$'\t' read -r id cmd; do
		case "$id" in '' | '#'*) continue ;; esac
		bash -c "$cmd" || exit 1
	done <"$file"
}
`
	p := selfCheckProject(t, "bash scripts/release-v0-2-0.sh", map[string]string{
		"scripts/release-v0-2-0.sh": "#!/usr/bin/env bash\n# the checks it runs are in scripts/checks/release-v0-2-0.checks.\n" +
			". \"$(dirname \"$0\")/lib/gate.sh\"\ndocs_check README.md -- v0.2.0\nrun_checks scripts/checks/release-v0-2-0.checks\n",
		"scripts/lib/gate.sh":                   "" + lib,
		"scripts/checks/release-v0-2-0.checks":  "readme-status\tgrep -q v0.2.0 README.md\nx\tls evidence/verification/*_19-1-pg15.md\n",
		"scripts/workflows-sagas.sh":            "#!/usr/bin/env bash\nrun_checks scripts/checks/workflows-sagas.checks\n",
		"scripts/checks/workflows-sagas.checks": "a\ttrue\n",
		"scripts/stubs/release-v0-2-0":          "#!/bin/sh\nexit 1\n",
		"deploy/compose/client.yml":             "x: 1\n",
		"test/harness/evidence.go":              "package harness\n",
		"README.md":                             "> **Status: release `v0.1.2`**\n",
	})
	own, globs := ownOf(t, p)
	require.Equal(t, []string{"scripts/checks/release-v0-2-0.checks", "scripts/lib/gate.sh"}, own)
	require.Empty(t, globs)
}

// TestApprove_AmendedCheckListReapproves: a check list the gate's lib reads
// by a variable is frozen; amending it is gate drift (gate refused), and a
// person's re-approval records it again; a second approve records nothing.
func TestApprove_AmendedCheckListReapproves(t *testing.T) {
	p := selfCheckProject(t, "bash scripts/gate.sh", map[string]string{
		"scripts/gate.sh":           "#!/bin/bash\nhere=$(cd \"$(dirname \"$0\")\" && pwd)\n. \"$here/lib/gate.sh\"\nexit 1\n",
		"scripts/lib/gate.sh":       "GATE_DIR=$(dirname \"$0\")\nwhile read -r l; do :; done < \"$GATE_DIR/checks/one.checks\"\n",
		"scripts/checks/one.checks": "a\ttrue\n",
	})
	m, recorded, err := p.ApproveMilestoneOutcome(ctx(), "M1", "ann", nil, nil)
	require.NoError(t, err)
	require.True(t, recorded)
	require.Equal(t, MilestoneApproved, m.Status)
	frozen, _ := p.ApprovalFrozen("M1")
	require.Equal(t, &FrozenGate{GateFiles: []string{"scripts/gate.sh"}, GateOwnFiles: []string{"scripts/checks/one.checks", "scripts/lib/gate.sh"}, GateGlobs: []string{}}, frozen)

	write(t, p, "scripts/checks/one.checks", "a\ttrue\nb\ttrue\n")
	commitAll(t, p, "chore: amend the list")
	_, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.Equal(t, CodeGateChanged, failCode(t, err))
	require.ErrorContains(t, err, "scripts/checks/one.checks changed")

	_, recorded, err = p.ApproveMilestoneOutcome(ctx(), "M1", "ann", nil, nil)
	require.NoError(t, err)
	require.True(t, recorded)
	require.Equal(t, "apex: M1 re-approved", subjects(t, p)[0])
	head := HeadSHA(ctx(), p.Root)
	_, recorded, err = p.ApproveMilestoneOutcome(ctx(), "M1", "ann", nil, nil)
	require.NoError(t, err)
	require.False(t, recorded)
	require.Equal(t, head, HeadSHA(ctx(), p.Root))
}

func TestApprove_UnchangedSaysSo(t *testing.T) {
	p := selfCheckProject(t, "bash scripts/gate.sh", map[string]string{
		"scripts/gate.sh":           "#!/bin/bash\nfor f in scripts/checks/*.checks; do :; done\nexit 1\n",
		"scripts/checks/one.checks": "a\n",
	})
	_, recorded, err := p.ApproveMilestoneOutcome(ctx(), "M1", "ann", nil, nil)
	require.NoError(t, err)
	require.True(t, recorded)
	_, at := p.ApprovalFrozen("M1")
	head := HeadSHA(ctx(), p.Root)
	_, recorded, err = p.ApproveMilestoneOutcome(ctx(), "M1", "ann", nil, nil)
	require.NoError(t, err)
	require.False(t, recorded, "nothing changed: a no-op")
	frozen, at2 := p.ApprovalFrozen("M1")
	require.Equal(t, at, at2)
	require.Equal(t, []string{"scripts/checks/*.checks"}, frozen.GateGlobs)
	require.Equal(t, head, HeadSHA(ctx(), p.Root))

	// A new match under the recorded glob: drift, and approve records again.
	write(t, p, "scripts/checks/two.checks", "b\n")
	commitAll(t, p, "chore: a second list")
	_, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.ErrorContains(t, err, "scripts/checks/two.checks is new (it matches scripts/checks/*.checks)")
	_, recorded, err = p.ApproveMilestoneOutcome(ctx(), "M1", "ann", nil, nil)
	require.NoError(t, err)
	require.True(t, recorded)
	frozen, _ = p.ApprovalFrozen("M1")
	require.Equal(t, []string{"scripts/checks/one.checks", "scripts/checks/two.checks"}, frozen.GateOwnFiles)
}
