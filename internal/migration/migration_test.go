package migration

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeRunner answers by exact command line: a code, or an error meaning
// "could not run at all". The distinction between the two is what the
// cannot-tell state is made of, so the fake has to be able to express it.
type fakeRunner struct {
	codes map[string]int
	errs  map[string]error
	ran   []string
}

func (f *fakeRunner) Run(_ context.Context, _, line string) (int, error) {
	f.ran = append(f.ran, line)
	if err, ok := f.errs[line]; ok {
		return -1, err
	}
	return f.codes[line], nil
}

func newRunner() *fakeRunner {
	return &fakeRunner{codes: map[string]int{}, errs: map[string]error{}}
}

func writeMigration(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
}

// v016 is the framework's own first entry, verbatim in shape.
const v016 = `---
id: v0.16.0_seq-01
version: 0.16.0
seq: 1
kind: derivable
blocking: false
after: []
supersedes: []
check: "ape sprint check --output-format json | jq -e '.summary.retrospective_rows >= .summary.epic_rows'"
command: "ape task apex-sprint-sync --agent apex-agent-sm"
summary: Add release_slices/active_slice keys and mint a retrospective row per epic.
---

# v0.16.0 seq 01
`

const v017judged = `---
id: v0.17.0_seq-02
version: 0.17.0
seq: 2
kind: judged
blocking: false
after: [v0.17.0_seq-01]
supersedes: []
check: "ape story verify --output-format json | jq -e 'true'"
skill: apex-upgrade-project
summary: Resolve requirement_ids where the body names none.
---

## Instructions

Read the story.
`

func TestLoad_AbsentFolderIsNormal(t *testing.T) {
	entries, err := Load(filepath.Join(t.TempDir(), "migrations"))
	require.NoError(t, err)
	require.Nil(t, entries, "a framework that ships no list is a normal state, not a finding")
}

func TestLoad_ParsesTheFrameworkShape(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "migrations")
	writeMigration(t, dir, "v0.16.0_seq-01_slice-keys-and-retro-rows.md", v016)

	entries, err := Load(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	e := entries[0]
	require.Equal(t, "v0.16.0_seq-01", e.ID)
	require.Equal(t, "0.16.0", e.Version)
	require.Equal(t, 1, e.Seq)
	require.Equal(t, KindDerivable, e.Kind)
	require.False(t, e.Blocking)
	require.Contains(t, e.Command, "apex-sprint-sync")
	require.Empty(t, e.Findings, "the framework's own entry parses clean")
	require.False(t, e.Unrunnable)
}

// TestOrder_SemanticNotLexical is the ordering rule's whole point:
// v0.9.0 sorts AFTER v0.10.0 lexically and BEFORE it as semver.
func TestOrder_SemanticNotLexical(t *testing.T) {
	entries := []Entry{
		{ID: "b", Version: "0.10.0", Seq: 1, Kind: KindDerivable, Command: "x"},
		{ID: "a", Version: "0.9.0", Seq: 1, Kind: KindDerivable, Command: "x"},
	}
	ordered, cycle := Order(entries)
	require.Empty(t, cycle)
	require.Equal(t, []string{"a", "b"}, ids(ordered))
}

func TestOrder_SeqBreaksAVersionTie(t *testing.T) {
	entries := []Entry{
		{ID: "c", Version: "0.16.0", Seq: 10, Kind: KindDerivable, Command: "x"},
		{ID: "a", Version: "0.16.0", Seq: 2, Kind: KindDerivable, Command: "x"},
	}
	ordered, _ := Order(entries)
	require.Equal(t, []string{"a", "c"}, ids(ordered), "seq compares as an integer, not a string")
}

func TestOrder_AfterMovesAnEntryBehindItsDependency(t *testing.T) {
	entries := []Entry{
		{ID: "early", Version: "0.16.0", Seq: 1, Kind: KindDerivable, Command: "x", After: []string{"late"}},
		{ID: "late", Version: "0.18.0", Seq: 1, Kind: KindDerivable, Command: "x"},
	}
	ordered, cycle := Order(entries)
	require.Empty(t, cycle)
	require.Equal(t, []string{"late", "early"}, ids(ordered))
}

// TestOrder_CycleIsReportedNotBroken: with the order undefined the runner
// has no basis to act, and inventing one is how a migration runs before
// what it depends on.
func TestOrder_CycleIsReportedNotBroken(t *testing.T) {
	entries := []Entry{
		{ID: "a", Version: "0.16.0", Seq: 1, Kind: KindDerivable, Command: "x", After: []string{"b"}},
		{ID: "b", Version: "0.16.0", Seq: 2, Kind: KindDerivable, Command: "x", After: []string{"a"}},
	}
	ordered, cycle := Order(entries)
	require.ElementsMatch(t, []string{"a", "b"}, cycle)
	require.Len(t, ordered, 2, "the entries are still reported")
	for _, e := range ordered {
		require.True(t, e.Unrunnable, "an entry with an undefined position is never run")
	}
}

func TestOrder_DanglingAfterIsAFindingNotACycle(t *testing.T) {
	entries := []Entry{
		{ID: "a", Version: "0.16.0", Seq: 1, Kind: KindDerivable, Command: "x", After: []string{"nowhere"}},
	}
	ordered, cycle := Order(entries)
	require.Empty(t, cycle)
	require.Len(t, ordered, 1)
	require.Contains(t, ordered[0].Findings[0], "nowhere")
	require.False(t, ordered[0].Unrunnable, "a dangling after: does not make the entry unrunnable")
}

func TestValidate_Findings(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "migrations")
	writeMigration(t, dir, "v0.16.0_seq-09_wrong-seq.md", `---
id: v0.16.0_seq-09
version: not-semver
seq: 1
kind: derivable
---

body
`)
	entries, err := Load(dir)
	require.NoError(t, err)
	joined := entries[0].Findings
	require.Contains(t, joinAll(joined), "not semver")
	require.Contains(t, joinAll(joined), "filename says seq 9, frontmatter says 1")
	require.Contains(t, joinAll(joined), "no command")
	require.True(t, entries[0].Unrunnable, "derivable with no command runs nothing")
}

func TestValidate_UnknownKindIsTreatedAsJudged(t *testing.T) {
	e := Entry{ID: "a", Version: "0.16.0", Seq: 1, Kind: "automatic", Path: "a_seq-1_x.md"}
	e.validate()
	require.Contains(t, joinAll(e.Findings), "unknown kind")
	require.True(t, e.IsJudged(), "ape not understanding an entry means ape does not run it")
}

// TestBuildPlan_LedgerDecidesApplied — the check reports and never gates,
// so an entry in the ledger is applied even while its check is silent.
func TestBuildPlan_LedgerDecidesApplied(t *testing.T) {
	dir, entries := twoEntries(t)
	r := newRunner()
	r.codes["c1"] = 1 // unsatisfied

	p := BuildPlan(t.Context(), dir, "/p", entries,
		[]Applied{{ID: "a", Version: "0.16.0", AppliedAt: "20260905120000"}}, r, true)

	row := rowByID(t, p, "a")
	require.Equal(t, StateHalfApplied, row.State,
		"the ledger says it ran and the check says the post-condition does not hold")
	require.Equal(t, SourceLedger, row.Source)
	require.Equal(t, "20260905120000", row.AppliedAt)
	require.False(t, row.Runnable)
}

func TestBuildPlan_LedgerPlusSatisfiedCheckIsApplied(t *testing.T) {
	dir, entries := twoEntries(t)
	r := newRunner()
	r.codes["c1"] = 0

	p := BuildPlan(t.Context(), dir, "/p", entries, []Applied{{ID: "a"}}, r, true)
	row := rowByID(t, p, "a")
	require.Equal(t, StateApplied, row.State)
	require.Equal(t, SourceLedger, row.Source)
}

// TestBuildPlan_SatisfiedCheckWithNoLedgerRow: a project upgraded by hand
// is already in the shape. Reported applied with the check named as the
// source; the ledger is NOT backfilled, because ape records what ape did.
func TestBuildPlan_SatisfiedCheckWithNoLedgerRow(t *testing.T) {
	dir, entries := twoEntries(t)
	r := newRunner()
	r.codes["c1"] = 0

	p := BuildPlan(t.Context(), dir, "/p", entries, nil, r, true)
	row := rowByID(t, p, "a")
	require.Equal(t, StateApplied, row.State)
	require.Equal(t, SourceCheck, row.Source)
	require.Empty(t, row.AppliedAt, "nothing is invented for a row ape did not write")
	require.False(t, row.Runnable)
}

// TestBuildPlan_CheckThatCannotRunIsCannotTell is the ruling's core: an
// errored check leaves the entry unapplied AND unverifiable. Collapsing
// it into pending builds a runner that re-applies things.
func TestBuildPlan_CheckThatCannotRunIsCannotTell(t *testing.T) {
	dir, entries := twoEntries(t)
	r := newRunner()
	r.errs["c1"] = errors.New("exec: \"jq\": executable file not found in $PATH")

	p := BuildPlan(t.Context(), dir, "/p", entries, nil, r, true)
	row := rowByID(t, p, "a")
	require.Equal(t, StateCannotTell, row.State)
	require.Equal(t, CheckCannotRun, row.Check)
	require.Contains(t, row.CheckDetail, "jq")
	require.False(t, row.Runnable, "acting here is the re-application the state exists to prevent")
}

func TestBuildPlan_UnsatisfiedCheckWithNoLedgerRowIsPending(t *testing.T) {
	dir, entries := twoEntries(t)
	r := newRunner()
	r.codes["c1"] = 1

	p := BuildPlan(t.Context(), dir, "/p", entries, nil, r, true)
	row := rowByID(t, p, "a")
	require.Equal(t, StatePending, row.State)
	require.True(t, row.Runnable)
}

// TestBuildPlan_NoCheckDeclaredIsPending — `check:` reports and never
// gates, so its ABSENCE must not stop the runner either.
func TestBuildPlan_NoCheckDeclaredIsPending(t *testing.T) {
	entries := []Entry{{ID: "a", Version: "0.16.0", Seq: 1, Kind: KindDerivable, Command: "cmd1"}}
	p := BuildPlan(t.Context(), "/d", "/p", entries, nil, newRunner(), true)
	row := rowByID(t, p, "a")
	require.Equal(t, CheckNone, row.Check)
	require.Equal(t, StatePending, row.State)
	require.True(t, row.Runnable)
}

// TestBuildPlan_NoCheckRunIsNotCannotRun: nobody asked is not the same as
// asked-and-unanswerable. The row falls back to the ledger alone.
func TestBuildPlan_NoCheckRunIsNotCannotRun(t *testing.T) {
	dir, entries := twoEntries(t)
	p := BuildPlan(t.Context(), dir, "/p", entries, nil, NoCheckRunner(), false)
	row := rowByID(t, p, "a")
	require.Equal(t, CheckNotRun, row.Check)
	require.Equal(t, StatePending, row.State)
}

func TestBuildPlan_JudgedIsNeverRunnable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "migrations")
	writeMigration(t, dir, "v0.17.0_seq-02_requirement-ids-judged.md", v017judged)
	entries, err := Load(dir)
	require.NoError(t, err)

	r := newRunner()
	r.codes[entries[0].Check] = 1
	p := BuildPlan(t.Context(), dir, "/p", entries, nil, r, true)
	row := rowByID(t, p, "v0.17.0_seq-02")
	require.Equal(t, StatePending, row.State)
	require.False(t, row.Runnable, "judged is listed and never executed, under any flag")
	require.Equal(t, "apex-upgrade-project", row.Skill)
}

func TestBuildPlan_Superseded(t *testing.T) {
	entries := []Entry{
		{ID: "old", Version: "0.16.0", Seq: 1, Kind: KindDerivable, Command: "x"},
		{ID: "new", Version: "0.18.0", Seq: 1, Kind: KindDerivable, Command: "y", Supersedes: []string{"old"}},
	}
	p := BuildPlan(t.Context(), "/d", "/p", entries, nil, newRunner(), true)
	old := rowByID(t, p, "old")
	require.Equal(t, StateSuperseded, old.State)
	require.Equal(t, "new", old.SupersededBy)
	require.False(t, old.Runnable)
}

func TestApply_RunsDerivablePendingInOrder(t *testing.T) {
	entries := []Entry{
		{ID: "b", Version: "0.18.0", Seq: 1, Kind: KindDerivable, Command: "cmd-b"},
		{ID: "a", Version: "0.16.0", Seq: 1, Kind: KindDerivable, Command: "cmd-a"},
	}
	r := newRunner()
	p := BuildPlan(t.Context(), "/d", "/p", entries, nil, r, true)

	res := Apply(t.Context(), io.Discard, "/p", p, r, fixedStamp)
	require.Equal(t, []string{"cmd-a", "cmd-b"}, r.ran, "semantic order, not file order")
	require.Len(t, res.Applied, 2)
	require.Equal(t, "a", res.Applied[0].ID)
	require.Equal(t, "20260905120000", res.Applied[0].AppliedAt)
	require.Empty(t, res.Failed)
}

// TestApply_StopsAtAFailure — `after:` means a later entry may depend on
// an earlier one, so continuing past a failure would run an entry whose
// precondition is known not to hold.
func TestApply_StopsAtAFailure(t *testing.T) {
	entries := []Entry{
		{ID: "a", Version: "0.16.0", Seq: 1, Kind: KindDerivable, Command: "cmd-a"},
		{ID: "b", Version: "0.18.0", Seq: 1, Kind: KindDerivable, Command: "cmd-b"},
	}
	r := newRunner()
	r.codes["cmd-a"] = 3
	p := BuildPlan(t.Context(), "/d", "/p", entries, nil, r, true)

	res := Apply(t.Context(), io.Discard, "/p", p, r, fixedStamp)
	require.Equal(t, "a", res.Failed)
	require.Equal(t, []string{"cmd-a"}, r.ran, "b was never attempted")
	require.Empty(t, res.Applied, "a failed entry is not recorded as applied")
	require.Len(t, res.Outcomes, 2)
	require.Contains(t, res.Outcomes[1].Skipped, "not attempted")
}

// TestApply_AppliedEntriesAreNotReRun is the idempotency the ledger
// provides, independent of what any command does.
func TestApply_AppliedEntriesAreNotReRun(t *testing.T) {
	entries := []Entry{{ID: "a", Version: "0.16.0", Seq: 1, Kind: KindDerivable, Command: "cmd-a"}}
	r := newRunner()
	p := BuildPlan(t.Context(), "/d", "/p", entries, []Applied{{ID: "a"}}, r, true)

	res := Apply(t.Context(), io.Discard, "/p", p, r, fixedStamp)
	require.Empty(t, r.ran)
	require.Empty(t, res.Applied)
}

func TestApply_JudgedIsListedWithItsSkill(t *testing.T) {
	entries := []Entry{
		{ID: "j", Version: "0.17.0", Seq: 1, Kind: KindJudged, Skill: "apex-upgrade-project"},
	}
	r := newRunner()
	p := BuildPlan(t.Context(), "/d", "/p", entries, nil, r, true)

	res := Apply(t.Context(), io.Discard, "/p", p, r, fixedStamp)
	require.Empty(t, r.ran, "never executed, under any flag")
	require.Len(t, res.Judged, 1)
	require.Contains(t, res.Judged[0].Skipped, "apex-upgrade-project")
}

func TestApply_CannotTellIsNotRun(t *testing.T) {
	entries := []Entry{
		{ID: "a", Version: "0.16.0", Seq: 1, Kind: KindDerivable, Command: "cmd-a", Check: "c1"},
	}
	r := newRunner()
	r.errs["c1"] = errors.New("no such command")
	p := BuildPlan(t.Context(), "/d", "/p", entries, nil, r, true)

	res := Apply(t.Context(), io.Discard, "/p", p, r, fixedStamp)
	require.NotContains(t, r.ran, "cmd-a")
	require.Empty(t, res.Applied)
}

func TestCounts(t *testing.T) {
	entries := []Entry{
		{ID: "p", Version: "0.16.0", Seq: 1, Kind: KindDerivable, Command: "x", Blocking: true},
		{ID: "j", Version: "0.16.0", Seq: 2, Kind: KindJudged, Skill: "s"},
		{ID: "a", Version: "0.16.0", Seq: 3, Kind: KindDerivable, Command: "y"},
	}
	p := BuildPlan(t.Context(), "/d", "/p", entries, []Applied{{ID: "a"}}, newRunner(), true)
	c := p.Counts()
	require.Equal(t, 2, c.Pending)
	require.Equal(t, 1, c.Runnable)
	require.Equal(t, 1, c.JudgedToRun)
	require.Equal(t, 1, c.Applied)
	require.Equal(t, 1, c.BlockingOpen)
}

func fixedStamp() string { return "20260905120000" }

func twoEntries(t *testing.T) (dir string, entries []Entry) {
	t.Helper()
	return "/d", []Entry{
		{ID: "a", Version: "0.16.0", Seq: 1, Kind: KindDerivable, Command: "cmd1", Check: "c1"},
		{ID: "b", Version: "0.18.0", Seq: 1, Kind: KindDerivable, Command: "cmd2", Check: "c2"},
	}
}

func rowByID(t *testing.T, p *Plan, id string) *Row {
	t.Helper()
	for i := range p.Rows {
		if p.Rows[i].ID == id {
			return &p.Rows[i]
		}
	}
	t.Fatalf("no row %q in plan", id)
	return nil
}

func ids(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for i := range entries {
		out = append(out, entries[i].ID)
	}
	return out
}

func joinAll(findings []string) string {
	all := ""
	var allSb425 strings.Builder
	for _, f := range findings {
		allSb425.WriteString(f + "\n")
	}
	all += allSb425.String()
	return all
}
