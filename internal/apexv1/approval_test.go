package apexv1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// H2: an approval is ape's record, not the Status line. A milestone that
// says approved with no record — a hand edit, or one approved before ape
// recorded approvals — starts no change and is reported by project check,
// until a person approves it through ape.
func TestApproval_StatusLineAloneIsNotTrusted(t *testing.T) {
	files := baseFiles()
	files["_apex/state/milestones.json"] = "{}\n" // no approval record
	p := fixture(t, files)
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", "none — no spec change", "- T1 wave — check: true"))
	commitAll(t, p, "plan")

	fs := ClassifyFindings(p.Check())
	require.Len(t, fs, 1, findingMessages(fs))
	require.Equal(t, CodeStatusUnrecorded, fs[0].Code)
	require.Equal(t, ActionPerson, ActionFor(fs[0].Code))
	require.Equal(t, "a person approves it: `ape milestone approve M1 --by <who>`", fs[0].Fix)

	head := HeadSHA(ctx(), p.Root)
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodeStatusUnrecorded, fe.Code)
	require.Equal(t, head, HeadSHA(ctx(), p.Root))
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root), "a refused start leaves no branch")
	_, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.ErrorContains(t, err, "ape has no record of a person approving it")
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.ErrorContains(t, err, "ape has no record of a person approving it")

	// A person re-approves: recorded, the Status line kept. The gate runs
	// first and must be red (D70 B): ./gate.sh exists and fails.
	write(t, p, "gate.sh", "#!/bin/sh\nexit 1\n")
	commitAll(t, p, "test: the gate")
	before := p.roadmap().Find("M1").StatusText
	_, err = p.ApproveMilestone(ctx(), "M1", "ann", nil)
	require.NoError(t, err)
	require.Equal(t, before, p.roadmap().Find("M1").StatusText)
	require.Equal(t, "apex: M1 re-approved", subjects(t, p)[0])
	require.Empty(t, p.Check())
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
}

// The gate is frozen at approval: its command and the files it names.
func TestApproval_GateChange(t *testing.T) {
	files := baseFiles()
	files["development/roadmap.md"] = strings.Replace(roadmapApproved, "Gate: ./gate.sh", "Gate: sh scripts/e2e.sh --suite=checks/m1.txt m1", 1)
	files["scripts/e2e.sh"] = "#!/bin/sh\nexit 1\n"
	files["checks/m1.txt"] = "greet\n"
	p := fixture(t, files)
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	a := recs["M1"].Approval
	require.Equal(t, []string{"scripts/e2e.sh"}, sortedKeys(keysOf(a.GateFiles)), "sh runs the script; checks/m1.txt is an argument")
	require.Empty(t, p.Check())

	// A gate script weakened after approval.
	write(t, p, "scripts/e2e.sh", "#!/bin/sh\nexit 0\n")
	commitAll(t, p, "weaken")
	fs := ClassifyFindings(p.Check())
	require.Len(t, fs, 1, findingMessages(fs))
	require.Equal(t, CodeGateChanged, fs[0].Code)
	require.Contains(t, fs[0].Message, "scripts/e2e.sh changed")
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.ErrorContains(t, err, "M1's gate changed since eval approved it: scripts/e2e.sh changed")

	// The Gate: line edited.
	write(t, p, "scripts/e2e.sh", "#!/bin/sh\nexit 1\n")
	write(t, p, "development/roadmap.md", strings.Replace(read(t, p, "development/roadmap.md"), "m1.txt m1", "m1.txt m0", 1))
	commitAll(t, p, "edit gate")
	_, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.ErrorContains(t, err, "the command was `sh scripts/e2e.sh --suite=checks/m1.txt m1`, is `sh scripts/e2e.sh --suite=checks/m1.txt m0`")

	// Start does not judge the gate: the approval record is enough.
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", "none — no spec change", "- T1 wave — check: true"))
	commitAll(t, p, "plan")
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
}

// A done Status ape did not write is reported; one ape closed is not.
func TestApproval_DoneUnrecorded(t *testing.T) {
	files := baseFiles()
	files["development/roadmap.md"] = strings.Replace(roadmapApproved, "Status: approved (by eval 2026-10-15T12:00:00Z)", "Status: done (2026-10-16T00:00:00Z abcdef1)", 1)
	p := fixture(t, files)
	fs := ClassifyFindings(p.Check())
	require.Len(t, fs, 1, findingMessages(fs))
	require.Equal(t, CodeStatusUnrecorded, fs[0].Code)
	require.Contains(t, fs[0].Message, "M1 says done, but ape recorded no close of it")
}

func keysOf(m map[string]string) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

func TestGateFiles(t *testing.T) {
	p := fixture(t, map[string]string{"a.sh": "a", "dir/b.yaml": "b", "c": "c", "s/g.py": "g", "x.sh": "x"})
	got := p.gateFiles(`FOO=dir/b.yaml ./a.sh "c" --x=c -v $HOME/a.sh /etc/passwd ../a.sh *.sh dir && true; echo done | cat`)
	require.Equal(t, []string{"a.sh"}, sortedKeys(keysOf(got)), "only the executable word")
	require.Equal(t, gateFilesDigest(got), gateFilesDigest(p.gateFiles("./a.sh c dir/b.yaml")))
	require.NotEqual(t, gateFilesDigest(got), gateFilesDigest(p.gateFiles("./x.sh")))
	require.Equal(t, []string{"c", "s/g.py"}, sortedKeys(keysOf(p.gateFiles("python3 -W ignore s/g.py dir/b.yaml && bash -e c x.sh"))))
	require.Empty(t, p.gateFiles("a.sh"), "a bare command word is looked up on PATH, not in the repo")
}

func TestGateScripts(t *testing.T) {
	for gate, want := range map[string][]string{
		`python3 -m unittest discover -s tests && test "$(python3 greet.py)" = "Hello" && grep -q polite CHANGELOG.md`: nil,
		`./scripts/gate.sh`:                                      {"scripts/gate.sh"},
		`bash scripts/gate.sh --fast data.txt`:                   {"scripts/gate.sh"},
		`sh -eu x.sh; zsh -o pipefail y.sh`:                      {"x.sh", "y.sh"},
		`dash +o errexit z.sh`:                                   {"z.sh"},
		`bash -c './a.sh' && sh -s < in.sh`:                      nil,
		`python3.12 -c 'print(1)' && python -m x.y`:              nil,
		`/usr/bin/python3 -- t/run.py arg.py`:                    {"t/run.py"},
		`node --require ts-node/register e2e.js && node -e 1`:    {"e2e.js"},
		`ruby -I lib t.rb; perl -e 'x' a.pl; perl b.pl`:          {"b.pl", "t.rb"},
		`CI=1 FOO="a b" ./scripts/e2e.sh 2>&1 | tee out/log.txt`: {"scripts/e2e.sh"},
		`env -i PATH=/bin scripts/x.sh > out.sh`:                 {"scripts/x.sh"},
		`( cd sub && ../up.sh ) || ! scripts/neg.sh`:             {"scripts/neg.sh"},
		"`./a.sh` $(./b.sh) && ./c.sh\n./d.sh # ./e.sh":          {"c.sh", "d.sh"},
		`make e2e && npm run test && $GATE ./arg.sh`:             nil,
		`if ./t.sh; then echo ok; fi`:                            {"t.sh"},
		`./scripts/gate.sh & wait`:                               {"scripts/gate.sh"},
	} {
		require.Equal(t, want, nilIfEmpty(gateScripts(gate)), gate)
	}
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

// Q15: an approval freezes the scripts the gate runs, not what it tests.
// The framework's M1 gate runs no script of the project: the changes that
// edit greet.py and CHANGELOG.md — the milestone's own work — leave it
// approved.
func TestApproval_GateDataFilesDoNotDrift(t *testing.T) {
	gate := `python3 -m unittest discover -s tests && test "$(python3 greet.py)" = "Hello" && grep -q polite CHANGELOG.md`
	files := baseFiles()
	files["development/roadmap.md"] = strings.Replace(roadmapApproved, "Gate: ./gate.sh", "Gate: "+gate, 1)
	files["greet.py"] = "print('Hi')\n"
	files["CHANGELOG.md"] = "# Changelog\n"
	p := fixture(t, files)
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Empty(t, recs["M1"].Approval.GateFiles, "nothing hashed")
	require.Equal(t, gate, recs["M1"].Approval.GateCmd, "the command is recorded exactly")
	require.Equal(t, gateFilesDigest(nil), recs["M1"].Approval.GateFilesSHA256)

	write(t, p, "greet.py", "print('Hello')\n")
	write(t, p, "CHANGELOG.md", "# Changelog\n- polite\n")
	commitAll(t, p, "the milestone's work")
	require.Empty(t, p.gateDrift(p.roadmap().Find("M1"), recs["M1"].Approval))
	require.Empty(t, p.Check())
}

// Q15: a script gate is frozen; a script created after approval is not
// drift (it was never approved), one removed or changed is.
func TestApproval_ScriptGateDrift(t *testing.T) {
	for _, gate := range []string{"./scripts/gate.sh", "bash scripts/gate.sh"} {
		t.Run(gate, func(t *testing.T) {
			files := baseFiles()
			files["development/roadmap.md"] = strings.Replace(roadmapApproved, "Gate: ./gate.sh", "Gate: "+gate+" && ./later.sh", 1)
			files["scripts/gate.sh"] = "#!/bin/sh\nexit 1\n"
			p := fixture(t, files)
			recs, err := p.LoadMilestoneRecords()
			require.NoError(t, err)
			a := recs["M1"].Approval
			require.Equal(t, []string{"scripts/gate.sh"}, sortedKeys(keysOf(a.GateFiles)))
			m := p.roadmap().Find("M1")

			write(t, p, "later.sh", "#!/bin/sh\nexit 0\n")
			require.Empty(t, p.gateDrift(m, a), "a script created after approval")

			write(t, p, "scripts/gate.sh", "#!/bin/sh\nexit 0\n")
			require.Equal(t, []string{"scripts/gate.sh changed"}, p.gateDrift(m, a))
			require.NoError(t, os.Remove(filepath.Join(p.Root, "scripts", "gate.sh")))
			require.Equal(t, []string{"scripts/gate.sh is gone"}, p.gateDrift(m, a))
		})
	}
}

// Q15: an approval rc.1 recorded holds every word that named a file. The
// ones the gate does not run as a script are ignored, the script is still
// held — compared file by file, not by the recorded digest.
func TestApproval_RC1RecordExtraFiles(t *testing.T) {
	gate := `./scripts/gate.sh && test "$(python3 greet.py)" = "Hello" && grep -q polite CHANGELOG.md`
	files := baseFiles()
	files["development/roadmap.md"] = strings.Replace(roadmapApproved, "Gate: ./gate.sh", "Gate: "+gate, 1)
	files["scripts/gate.sh"] = "#!/bin/sh\nexit 1\n"
	files["greet.py"] = "print('Hi')\n"
	files["CHANGELOG.md"] = "# Changelog\n"
	rc1 := map[string]string{}
	for _, n := range []string{"scripts/gate.sh", "greet.py", "CHANGELOG.md"} {
		sum := sha256.Sum256([]byte(files[n]))
		rc1[n] = hex.EncodeToString(sum[:])
	}
	rec, err := json.Marshal(map[string]any{"M1": map[string]any{"approval": MilestoneApproval{By: "eval", At: "2026-10-15T12:00:00Z", GateCmd: gate, GateFiles: rc1, GateFilesSHA256: gateFilesDigest(rc1)}}})
	require.NoError(t, err)
	files["_apex/state/milestones.json"] = string(rec)
	p := fixture(t, files)
	require.Empty(t, p.Check())

	write(t, p, "greet.py", "print('Hello')\n")
	write(t, p, "CHANGELOG.md", "# Changelog\n- polite\n")
	commitAll(t, p, "the milestone's work")
	require.Empty(t, p.Check(), "data files the rc.1 record holds are ignored")
	head := HeadSHA(ctx(), p.Root)
	_, err = p.ApproveMilestone(ctx(), "M1", "ann", nil)
	require.NoError(t, err)
	require.Equal(t, head, HeadSHA(ctx(), p.Root), "nothing to re-approve")

	write(t, p, "scripts/gate.sh", "#!/bin/sh\nexit 0\n")
	commitAll(t, p, "weaken")
	fs := ClassifyFindings(p.Check())
	require.Len(t, fs, 1, findingMessages(fs))
	require.Equal(t, CodeGateChanged, fs[0].Code)
	require.Contains(t, fs[0].Message, "(scripts/gate.sh changed)")
}
