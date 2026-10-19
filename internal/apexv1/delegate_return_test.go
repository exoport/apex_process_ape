package apexv1

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// delegateFail runs one delegate session of add-wave's T2 that records a
// fail with defects, and settles it.
func delegateFail(t *testing.T, p *Project, runID, reason string, defects ...string) *DelegateOutcome {
	t.Helper()
	dr := startDelegate(t, p, runID)
	_, err := p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "fail", reason, nil, defects)
	require.NoError(t, err)
	leaveSession(t)
	out, err := p.SettleDelegateRun(ctx(), dr)
	require.NoError(t, err)
	require.Equal(t, "fail", out.Verdict)
	require.Nil(t, out.Accepted)
	return out
}

// reReview takes a returned add-wave through verify and review again.
func reReview(t *testing.T, p *Project) {
	t.Helper()
	_, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "development/changes/add-wave/review.md", "fine again\n")
	commitAll(t, p, "review: add-wave again")
	_, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.NoError(t, err)
}

// D188: a delegate fail naming a defect in the change's own diff goes back
// to the change, as a review block does — in-progress, its review
// cleared, the return recorded and committed — and ape status shows the
// reason, with next naming apex-change.
func TestDelegate_FailOnAnInDiffDefectReturnsTheChange(t *testing.T) {
	p := delegatedWave(t, "human_review: {}\n")
	out := delegateFail(t, p, "run-r1", "wave.sh prints wave in lower case, T2 asks for Wave", "wave.sh", "./wave.sh")
	require.NotNil(t, out.Returned, "%+v", out)
	require.Empty(t, out.ToPerson)
	require.Equal(t, []string{"wave.sh"}, out.Defects, "the defects are project-relative, repeats dropped")
	require.Equal(t, DelegateReturn{Run: "run-r1", Reason: "wave.sh prints wave in lower case, T2 asks for Wave", Defects: []string{"wave.sh"}, At: out.Returned.At}, *out.Returned)

	require.Equal(t, "apex: add-wave returned by delegate", subjects(t, p)[0])
	require.Equal(t, "Change: add-wave", run(t, p.Root, "log", "-1", "--format=%b"))
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
	st := state(t, p, "add-wave")
	require.Equal(t, StatusInProgress, st.Status)
	require.Nil(t, st.Review, "the review is cleared: verify and review run again")
	require.Equal(t, []DelegateReturn{*out.Returned}, st.DelegateReturns["T2"])
	require.Equal(t, []string{"wave.sh"}, p.LoadDelegateVerdict("run-r1").Defects)

	r := p.Status()
	c := r.Changes[0]
	require.Empty(t, c.AwaitingHuman)
	require.True(t, c.Delegate["T2"].Returned)
	require.Equal(t, []string{"wave.sh"}, c.Delegate["T2"].Defects)
	require.Equal(t, st.DelegateReturns, c.DelegateReturns)
	require.Contains(t, r.Next, "returned by the delegate: add-wave T2 — wave.sh prints wave in lower case, T2 asks for Wave (defects: wave.sh)")
	require.Contains(t, r.Next, "(apex-change)")
	require.Contains(t, r.Brief(), "returned by delegate (T2): wave.sh prints wave in lower case")
	data, err := json.Marshal(r)
	require.NoError(t, err)
	require.Contains(t, string(data), `"delegate_returns":{"T2":[{"run":"run-r1","reason":"wave.sh prints wave in lower case, T2 asks for Wave","defects":["wave.sh"],"at":`)

	// Verified and reviewed again, the task is the delegate's again.
	reReview(t, p)
	r = p.Status()
	require.Equal(t, []DelegableTask{{Task: "T2", Reason: DelegableConfigured}}, r.Changes[0].Delegable)
	require.Contains(t, r.Next, "delegate T2 of add-wave")
	require.Contains(t, r.Brief(), "(T2 delegate returned it: wave.sh prints wave")
}

// D188 from the base branch: the return is committed on change/<id>
// without a checkout, where the change's state lives.
func TestDelegate_ReturnFromTheBaseBranch(t *testing.T) {
	p := delegatedWave(t, "human_review: {}\n")
	run(t, p.Root, "switch", "-q", "main")
	mainHead := HeadSHA(ctx(), p.Root)
	out := delegateFail(t, p, "run-r2", "wave.sh is wrong", "wave.sh")
	require.NotNil(t, out.Returned, "%+v", out)
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root))
	require.Equal(t, mainHead, HeadSHA(ctx(), p.Root), "nothing committed on main")
	require.Equal(t, "apex: add-wave returned by delegate", run(t, p.Root, "log", "-1", "--format=%s", "change/add-wave"))
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
	require.Equal(t, StatusInProgress, p.Status().Changes[0].Status)
}

// D188 Q106: after a return, the task's next fail goes to a person, even
// with an in-diff defect; delegate.already-failed still guards the same
// tip.
func TestDelegate_SecondFailGoesToAPerson(t *testing.T) {
	p := delegatedWave(t, "human_review: {}\n")
	first := delegateFail(t, p, "run-s1", "first", "wave.sh")
	require.NotNil(t, first.Returned)
	reReview(t, p)
	head := HeadSHA(ctx(), p.Root)

	out := delegateFail(t, p, "run-s2", "still wrong", "wave.sh")
	require.Nil(t, out.Returned)
	require.Equal(t, ToPersonSecondFail, out.ToPerson)
	require.Equal(t, []DelegateReturn{*first.Returned}, out.Returns)
	require.Equal(t, head, HeadSHA(ctx(), p.Root), "nothing committed")
	st := state(t, p, "add-wave")
	require.Equal(t, StatusReviewed, st.Status)
	require.Len(t, st.DelegateReturns["T2"], 1)

	r := p.Status()
	c := r.Changes[0]
	require.Equal(t, []string{"T2"}, c.AwaitingHuman)
	require.False(t, c.Delegate["T2"].Returned)
	require.Empty(t, c.Delegable, "a person's now")
	require.Contains(t, r.Next, "a person reviews add-wave")
	_, err := p.PrepareDelegate(ctx(), "add-wave", "T2")
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "delegate.already-failed", fe.Code)
}

// D188 Q104: a fail with no --defect, or whose defects are all outside the
// change's diff, stays a person's; "no verdict" names none.
func TestDelegate_FailWithoutAnInDiffDefectGoesToAPerson(t *testing.T) {
	t.Run("no defect", func(t *testing.T) {
		p := delegatedWave(t, "human_review: {}\n")
		head := HeadSHA(ctx(), p.Root)
		out := delegateFail(t, p, "run-n1", "the copy is truncated")
		require.Nil(t, out.Returned)
		require.Equal(t, ToPersonNoDefect, out.ToPerson)
		require.Equal(t, head, HeadSHA(ctx(), p.Root))
		require.Equal(t, StatusReviewed, state(t, p, "add-wave").Status)
	})
	t.Run("outside the diff", func(t *testing.T) {
		p := delegatedWave(t, "human_review: {}\n")
		head := HeadSHA(ctx(), p.Root)
		out := delegateFail(t, p, "run-n2", "the spec disagrees", "development/project.md", "nowhere.md")
		require.Nil(t, out.Returned)
		require.Equal(t, ToPersonNoDefect, out.ToPerson)
		require.Empty(t, out.Defects)
		require.Equal(t, []string{"development/project.md", "nowhere.md"}, out.DefectsOutside, "recorded, not counted")
		require.Equal(t, []string{"development/project.md", "nowhere.md"}, p.LoadDelegateVerdict("run-n2").Defects)
		require.Equal(t, head, HeadSHA(ctx(), p.Root))
		require.Equal(t, []string{"T2"}, p.Status().Changes[0].AwaitingHuman)
	})
	t.Run("no verdict", func(t *testing.T) {
		p := delegatedWave(t, "human_review: {}\n")
		dr := startDelegate(t, p, "run-n3")
		leaveSession(t)
		out, err := p.SettleDelegateRun(ctx(), dr)
		require.NoError(t, err)
		require.Equal(t, NoVerdictReason, out.Reason)
		require.Nil(t, out.Returned)
		require.Equal(t, ToPersonNoDefect, out.ToPerson)
	})
}

// D188 Q107: ape cannot reopen an archived change, so its fail is a
// person's whatever it names.
func TestDelegate_FailOnAnArchivedChangeGoesToAPerson(t *testing.T) {
	p := delegatedWave(t, "human_review: {}\n")
	_, err := p.Archive(ctx(), "add-wave", true, RunOptions{})
	require.NoError(t, err)
	head := HeadSHA(ctx(), p.Root)
	out := delegateFail(t, p, "run-a1", "wave.sh is wrong", "wave.sh")
	require.Nil(t, out.Returned)
	require.Equal(t, ToPersonArchived, out.ToPerson)
	require.Equal(t, head, HeadSHA(ctx(), p.Root))
}

// --defect goes only with a fail.
func TestDelegate_DefectOnlyWithAFail(t *testing.T) {
	p := delegatedWave(t, "human_review: {}\n")
	startDelegate(t, p, "run-d1")
	_, err := p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "pass", "fine", nil, []string{"wave.sh"})
	ue, ok := errors.AsType[*UsageError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "usage.delegate", ue.Code)
	require.Nil(t, p.LoadDelegateVerdict("run-d1"), "nothing recorded")
}

// D188 Q106: the counter resets only when a person answers the change or
// accepts the task: a return before either does not make the next fail a
// second one.
func TestDelegate_ReturnCounterResetsAtAPersonsAct(t *testing.T) {
	ret := DelegateReturn{Run: "r1", Reason: "x", Defects: []string{"a.md"}, At: "2026-10-19T10:00:00Z"}
	st := &State{DelegateReturns: map[string][]DelegateReturn{"T2": {ret}}}
	require.Equal(t, []DelegateReturn{ret}, returnsSinceReset(st, "T2"))
	require.Empty(t, returnsSinceReset(st, "T3"))

	st.Accepted = []Accept{{Task: "T2", By: "delegate:apex-accept", At: "2026-10-19T11:00:00Z", Run: "r9"}}
	require.Len(t, returnsSinceReset(st, "T2"), 1, "a delegate's acceptance is no person's")
	st.Accepted = []Accept{{Task: "T3", By: "diego", At: "2026-10-19T11:00:00Z"}}
	require.Len(t, returnsSinceReset(st, "T2"), 1, "another task's acceptance")
	st.Accepted = []Accept{{Task: "T2", By: "diego", At: "2026-10-19T11:00:00Z"}}
	require.Empty(t, returnsSinceReset(st, "T2"), "a person accepted the task")

	st.Accepted = nil
	st.Answer = &Answer{By: "diego", At: "2026-10-19T09:00:00Z", Text: "earlier"}
	require.Len(t, returnsSinceReset(st, "T2"), 1, "an answer before the return")
	st.Answer.At = "2026-10-19T12:00:00Z"
	require.Empty(t, returnsSinceReset(st, "T2"), "a person answered the change since")

	// End to end: returned, then blocked and answered by a person; reviewed
	// again, a fail with an in-diff defect goes back to the change again.
	p := delegatedWave(t, "human_review: {}\n")
	require.NotNil(t, delegateFail(t, p, "run-c1", "first", "wave.sh").Returned)
	_, err := p.Block(ctx(), "add-wave", "a question for a person")
	require.NoError(t, err)
	_, err = p.AnswerChange(ctx(), "add-wave", "go on", "diego")
	require.NoError(t, err)
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	reReview(t, p)
	out := delegateFail(t, p, "run-c2", "second, after the answer", "wave.sh")
	require.NotNil(t, out.Returned, "%+v", out)
	require.Len(t, state(t, p, "add-wave").DelegateReturns["T2"], 2, "history is kept")
}

// D188 Q108: the D152 docs default returns a docs change as a configured
// delegate does; the docs change is still the delegate's once reviewed
// again.
func TestDocsDelegate_FailOnAnInDiffDefectReturnsTheChange(t *testing.T) {
	p := docsWave(t, "", true, nil)
	plan, err := p.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.NoError(t, err)
	require.Equal(t, DelegableDocsDefault, plan.Reason)
	dr := &DelegateRun{
		Change: "docs-wave", Task: "T2", Skill: plan.Skill, Args: plan.Args, Folder: plan.Folder, DetachedID: "run-dr1",
		StartedAt: Stamp(), HeadSHA: HeadSHA(ctx(), p.Root), Branch: CurrentBranch(ctx(), p.Root), ChangeTip: plan.ChangeTip,
	}
	require.NoError(t, p.SaveDelegateRun(dr))
	t.Setenv(EnvDelegate, "docs-wave/T2")
	t.Setenv(envDetachedRun, "run-dr1")
	_, err = p.RecordDelegateVerdict(ctx(), "docs-wave", "T2", "fail", "docs/wave.md shows <TAB> where T2 asks for a literal tab", nil, []string{"docs/wave.md"})
	require.NoError(t, err)
	leaveSession(t)
	out, err := p.SettleDelegateRun(ctx(), dr)
	require.NoError(t, err)
	require.NotNil(t, out.Returned, "%+v", out)
	require.Equal(t, []string{"docs/wave.md"}, out.Returned.Defects)
	require.Equal(t, StatusInProgress, state(t, p, "docs-wave").Status)
	require.Contains(t, p.Status().Next, "returned by the delegate: docs-wave T2")

	_, err = p.Verify(ctx(), "docs-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "development/changes/docs-wave/review.md", "fine again\n")
	commitAll(t, p, "review: docs-wave again")
	_, err = p.Review(ctx(), "docs-wave", "pass", "development/changes/docs-wave/review.md")
	require.NoError(t, err)
	require.Equal(t, []DelegableTask{{Task: "T2", Reason: DelegableDocsDefault}}, p.Status().Changes[0].Delegable)
}
