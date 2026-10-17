package apexv1

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// H4, D45: only a person archives past a pending human-review task.
func TestArchive_AllowPendingHumanIsPersonOnly(t *testing.T) {
	p := reviewedWave(t)
	t.Setenv("CLAUDECODE", "1")
	head := HeadSHA(ctx(), p.Root)
	_, err := p.Archive(ctx(), "add-wave", true, RunOptions{})
	ue, ok := errors.AsType[*UsageError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodePersonOnly, ue.Code)
	require.Equal(t, head, HeadSHA(ctx(), p.Root), "nothing written")
	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.ErrorContains(t, err, "human-review tasks not accepted: T2", "without the flag a model session is told as before")
	require.Contains(t, err.Error(), "a person only")
}

// An archived change's unaccepted human-review task stays visible, and
// stays acceptable — on the base branch, from any branch, with evidence.
func TestAccept_ArchivedChange(t *testing.T) {
	p := reviewedWave(t)
	now = func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = time.Now })
	r := p.Status()
	require.Equal(t, []string{"T2"}, r.Changes[0].AwaitingHuman, "a change in flight awaits its person too")
	require.Contains(t, r.Next, "a person reviews add-wave: `ape change accept add-wave --task T2 --by <who>`",
		"a reviewed change awaiting a person is not archived next (D45)")
	res, err := p.Archive(ctx(), "add-wave", true, RunOptions{})
	require.NoError(t, err)

	r = p.Status()
	require.Len(t, r.Changes, 1)
	c := r.Changes[0]
	require.Equal(t, StatusArchived, c.Status)
	require.Equal(t, []string{"T2"}, c.AwaitingHuman)
	require.Empty(t, r.Fixes)
	brief := r.Brief()
	require.Contains(t, brief, "Changes: none open (1 archived)")
	require.Contains(t, brief, "Archived, human review not accepted")
	require.Contains(t, brief, "add-wave")
	data, err := json.Marshal(r)
	require.NoError(t, err)
	require.Contains(t, string(data), `"awaiting_human":["T2"]`)

	// From another branch: committed on main, HEAD and tree untouched.
	run(t, p.Root, "switch", "-q", "-c", "side")
	write(t, p, "evidence/screen.png", "png\n")
	commitAll(t, p, "evidence")
	sideHead := HeadSHA(ctx(), p.Root)
	_, err = p.Accept(ctx(), "add-wave", "T1", "ann")
	require.ErrorContains(t, err, "proven by `./wave.sh`")
	_, err = p.Accept(ctx(), "add-wave", "T2", "ann", "evidence/nope.png")
	ue, ok := errors.AsType[*UsageError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "usage.evidence", ue.Code)
	_, err = p.Accept(ctx(), "add-wave", "T2", "ann", "/etc/hostname")
	require.ErrorContains(t, err, "nor a file in the repository")

	st, err := p.Accept(ctx(), "add-wave", "T2", "ann", "evidence/screen.png", "https://ci.example.com/run/7")
	require.NoError(t, err)
	require.Equal(t, sideHead, HeadSHA(ctx(), p.Root))
	require.Equal(t, "side", CurrentBranch(ctx(), p.Root))
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
	require.Equal(t, "apex: add-wave accepted T2 (archived)", run(t, p.Root, "log", "-1", "--format=%s", "main"))
	require.Equal(t, "Change: add-wave", run(t, p.Root, "log", "-1", "--format=%b", "main"))
	require.Len(t, st.Accepted, 1)
	ev := st.Accepted[0].Evidence
	require.Len(t, ev, 2)
	require.Equal(t, "evidence/screen.png", ev[0].Path)
	require.Len(t, ev[0].SHA256, 64)
	require.Equal(t, "https://ci.example.com/run/7", ev[1].URL)

	run(t, p.Root, "switch", "-q", "main")
	archived, err := LoadState(filepath.Join(p.Root, res.ArchivedTo, "state.json"))
	require.NoError(t, err)
	require.Equal(t, StatusArchived, archived.Status)
	require.Equal(t, "ann", archived.Accepted[0].By)
	r = p.Status()
	require.Empty(t, r.Changes, "nothing awaits a person any more")

	// Accepting again on main is an ordinary commit there.
	_, err = p.Accept(ctx(), "add-wave", "T2", "bob")
	require.NoError(t, err)
	require.Equal(t, "apex: add-wave accepted T2 (archived)", subjects(t, p)[0])
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
}

// A change in flight is accepted on its branch, as before, with its
// evidence recorded.
func TestAccept_InFlightWithEvidence(t *testing.T) {
	p := verifiedWave(t)
	st, err := p.Accept(ctx(), "add-wave", "T2", "ann", "https://example.com/review")
	require.NoError(t, err)
	require.Equal(t, []Evidence{{URL: "https://example.com/review"}}, st.Accepted[0].Evidence)
	require.Empty(t, st.Verify.PendingHuman)
	require.True(t, strings.HasPrefix(subjects(t, p)[0], "apex: add-wave "))
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root))
}

// D49 Q12: closing past a change that waits for a person is the person's
// to unblock (action person); past one the orchestrator can move, apply.
func TestClose_ChangesInFlightAction(t *testing.T) {
	p := startedWave(t)
	run(t, p.Root, "switch", "-q", "main")
	_, err := p.CloseMilestone(ctx(), "M1", RunOptions{})
	d := Diagnostics(err)
	require.Equal(t, "milestone.changes-in-flight", d[0].Code)
	require.Equal(t, ActionApply, d[0].Action, "in-progress with T2 pending is still the orchestrator's")
	require.Empty(t, p.Status().Changes[0].AwaitingHuman)

	p = reviewedWave(t)
	run(t, p.Root, "switch", "-q", "main")
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	d = Diagnostics(err)
	require.Equal(t, "milestone.changes-in-flight", d[0].Code)
	require.Equal(t, ActionPerson, d[0].Action)
	require.Contains(t, d[0].Message, "awaiting a person's acceptance: add-wave (T2)")
	require.Equal(t, ActionApply, ActionFor("milestone.changes-in-flight"), "the published action stays apply")
}

func TestDiagnostics_ActionOverride(t *testing.T) {
	require.Equal(t, ActionStop, Diagnostics(&UsageError{Code: "usage.by", Msg: "x"})[0].Action)
	require.Equal(t, ActionPerson, Diagnostics(&UsageError{Code: "usage.by", Msg: "x", Action: ActionPerson})[0].Action)
	d := Diagnostics(&ConflictError{Code: CodeDeltaConflict, Msg: "x", Conflicts: []string{"a"}, Action: ActionStop})
	require.Equal(t, ActionStop, d[0].Action)
	require.Equal(t, ActionStop, d[1].Action)
}
