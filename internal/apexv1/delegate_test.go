package apexv1

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// delegatedWave is reviewedWave in a project whose config delegates human
// review (cfg, committed before the v1 epoch, so it is baseline), with the
// tree clean.
func delegatedWave(t *testing.T, cfg string) *Project {
	t.Helper()
	files := baseFiles()
	if cfg != "" {
		files["_apex/config.yaml"] = cfg
	}
	p := fixture(t, files)
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "development/changes/add-wave/review.md", "fine\n")
	commitAll(t, p, "review: add-wave")
	_, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.NoError(t, err)
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
	return p
}

// startDelegate records a delegate run of T2 as `ape change accept
// --delegate` would after starting its session, and makes the test that
// session.
func startDelegate(t *testing.T, p *Project, runID string) *DelegateRun {
	t.Helper()
	plan, err := p.PrepareDelegate(ctx(), "add-wave", "T2")
	require.NoError(t, err)
	r := &DelegateRun{
		Change: "add-wave", Task: "T2", Skill: plan.Skill, Args: plan.Args, Folder: plan.Folder, DetachedID: runID,
		StartedAt: Stamp(), HeadSHA: HeadSHA(ctx(), p.Root), Branch: CurrentBranch(ctx(), p.Root), ChangeTip: plan.ChangeTip,
	}
	require.NoError(t, p.SaveDelegateRun(r))
	t.Setenv(EnvDelegate, "add-wave/T2")
	t.Setenv(envDetachedRun, runID)
	return r
}

// leaveSession is the delegate session ending: ape settles from outside.
func leaveSession(t *testing.T) {
	t.Helper()
	t.Setenv(EnvDelegate, "")
	t.Setenv(envDetachedRun, "")
}

func TestPrepareDelegate(t *testing.T) {
	t.Run("no human_review: delegation is off, only a person accepts", func(t *testing.T) {
		p := delegatedWave(t, "")
		_, err := p.PrepareDelegate(ctx(), "add-wave", "T2")
		fe, ok := errors.AsType[*FailError](err)
		require.True(t, ok, "%v", err)
		require.Equal(t, "delegate.off", fe.Code)
		require.Equal(t, ActionPerson, ActionFor(fe.Code))
	})
	t.Run("the default delegate and args", func(t *testing.T) {
		p := delegatedWave(t, "human_review: {}\n")
		plan, err := p.PrepareDelegate(ctx(), "add-wave", "T2")
		require.NoError(t, err)
		require.Equal(t, "apex-accept", plan.Skill)
		require.Equal(t, "--task T2 --change add-wave", plan.Args)
		require.Equal(t, "development/changes/add-wave", plan.Folder)

		_, err = p.PrepareDelegate(ctx(), "add-wave", "T1")
		require.ErrorContains(t, err, "proven by `./wave.sh`")
		_, err = p.PrepareDelegate(ctx(), "add-wave", "T9")
		require.ErrorContains(t, err, "has no task T9")

		t.Setenv(EnvDelegate, "add-wave/T2")
		_, err = p.PrepareDelegate(ctx(), "add-wave", "T2")
		ue, ok := errors.AsType[*UsageError](err)
		require.True(t, ok, "%v", err)
		require.Equal(t, "delegate.nested", ue.Code)
	})
	t.Run("a skill, args template, model and effort of its own", func(t *testing.T) {
		p := delegatedWave(t, "models:\n  ui-judge: {model: sonnet, effort: low}\n"+
			"human_review: {delegate: ui-judge, args: \"{change} --out {assets}/accept-{task}.md\", model: haiku}\n")
		plan, err := p.PrepareDelegate(ctx(), "add-wave", "T2")
		require.NoError(t, err)
		require.Equal(t, "ui-judge", plan.Skill)
		require.Equal(t, "development/changes/add-wave/change.md --out development/changes/add-wave/assets/accept-T2.md", plan.Args)
		require.Equal(t, SkillModel{Model: "haiku", Effort: "low"}, plan.Model)
	})
	t.Run("an unknown placeholder is refused, and project check says so", func(t *testing.T) {
		p := delegatedWave(t, "human_review: {args: \"--task {tsk}\"}\n")
		_, err := p.PrepareDelegate(ctx(), "add-wave", "T2")
		fe, ok := errors.AsType[*FailError](err)
		require.True(t, ok, "%v", err)
		require.Equal(t, "delegate.invalid-config", fe.Code)
		require.Contains(t, findingMessages(p.humanReviewFindings()), "human_review config: args \"--task {tsk}\": unknown placeholder {tsk}")
	})
	t.Run("a change not reviewed yet is not awaiting a person", func(t *testing.T) {
		p := delegatedWave(t, "human_review: {}\n")
		write(t, p, "wave.sh", "#!/bin/sh\necho wave2\n")
		commitAll(t, p, "feat: more")
		_, err := p.Verify(ctx(), "add-wave", RunOptions{})
		require.NoError(t, err)
		_, err = p.PrepareDelegate(ctx(), "add-wave", "T2")
		fe, ok := errors.AsType[*FailError](err)
		require.True(t, ok, "%v", err)
		require.Equal(t, "delegate.not-awaiting", fe.Code)
		require.Contains(t, fe.Msg, "the change is verified")
	})
	t.Run("an accepted task is not awaiting a person", func(t *testing.T) {
		p := delegatedWave(t, "human_review: {}\n")
		_, err := p.Accept(ctx(), "add-wave", "T2", "ann")
		require.NoError(t, err)
		_, err = p.PrepareDelegate(ctx(), "add-wave", "T2")
		require.ErrorContains(t, err, "it is accepted already")
	})
	t.Run("an uncommitted human_review is not a person's", func(t *testing.T) {
		p := delegatedWave(t, "human_review: {}\n")
		write(t, p, "_apex/config.yaml", "human_review: {delegate: rubber-stamp}\n")
		_, err := p.PrepareDelegate(ctx(), "add-wave", "T2")
		fe, ok := errors.AsType[*FailError](err)
		require.True(t, ok, "%v", err)
		require.Equal(t, "delegate.unaccepted-config", fe.Code)
		require.Contains(t, fe.Msg, "uncommitted")
	})
}

// D75: a model cannot add the human_review block and accept its own work.
// While a commit no person accepted changed it (config.changed), --delegate
// is refused; a person's accept-config clears it.
func TestPrepareDelegate_RefusedWhileConfigChangedNamesHumanReview(t *testing.T) {
	p := fixture(t, baseFiles())
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")
	write(t, p, "_apex/config.yaml", "human_review: {}\n")
	commitAll(t, p, "chore: let a skill accept")
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	require.Contains(t, findingMessages(p.provenanceFindings(ctx())), "guarded config keys (human_review)")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)

	_, err = p.PrepareDelegate(ctx(), "add-wave", "T2")
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "delegate.unaccepted-config", fe.Code, "checked before the task: nothing about the change matters")
	require.Contains(t, fe.Msg, "(human_review)")

	run(t, p.Root, "switch", "-q", "main")
	_, err = p.AcceptConfig(ctx(), "ann")
	require.NoError(t, err)
	_, err = p.PrepareDelegate(ctx(), "add-wave", "T2")
	fe, ok = errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "delegate.not-awaiting", fe.Code, "accepted: now the task is what is judged")
}

// D75/D76: a pass is recorded by ape as a person's acceptance is — by
// delegate:<skill>, with the run — the session's note committed with it;
// ape status shows it, and the archive squash names it.
func TestDelegate_PassIsAcceptedByTheDelegate(t *testing.T) {
	p := delegatedWave(t, "human_review: {}\n")
	r := p.Status()
	require.Contains(t, r.Next, "delegate T2 of add-wave to apex-accept: `ape change accept add-wave T2 --delegate --wait 9m`")

	dr := startDelegate(t, p, "20261017-120000-abc123")
	write(t, p, "development/changes/add-wave/assets/accept-T2.md", "the copy reads well\n")
	v, err := p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "pass", "the copy reads well",
		[]string{"development/changes/add-wave/assets/accept-T2.md"})
	require.NoError(t, err)
	require.Equal(t, "20261017-120000-abc123", v.Run)
	require.FileExists(t, filepath.Join(p.Root, "_apex", "state", "runs", "delegate", "20261017-120000-abc123.json"))
	require.Len(t, v.Evidence, 1)
	leaveSession(t)

	out, err := p.SettleDelegateRun(ctx(), dr)
	require.NoError(t, err)
	require.False(t, out.Voided)
	require.Equal(t, []string{"development/changes/add-wave/assets/accept-T2.md"}, out.Notes)
	require.NotNil(t, out.Accepted)
	require.Equal(t, "delegate:apex-accept", out.Accepted.By)
	require.Equal(t, "20261017-120000-abc123", out.Accepted.Run)
	require.Equal(t, "development/changes/add-wave/assets/accept-T2.md", out.Accepted.Evidence[0].Path)

	// One ape commit on the change's branch: the state and the note, with
	// the trailer.
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root))
	require.Equal(t, "apex: add-wave reviewed", subjects(t, p)[0])
	require.Equal(t, "Accepted-By: delegate:apex-accept T2 run 20261017-120000-abc123\nChange: add-wave", run(t, p.Root, "log", "-1", "--format=%b"))
	require.Contains(t, run(t, p.Root, "show", "--name-only", "--format=", "HEAD"), "assets/accept-T2.md")
	st := state(t, p, "add-wave")
	require.Equal(t, "20261017-120000-abc123", st.Accepted[0].Run)

	r = p.Status()
	c := r.Changes[0]
	require.Empty(t, c.AwaitingHuman)
	require.Equal(t, "delegate:apex-accept", c.Accepted[0].By)
	require.Equal(t, "pass", c.Delegate["T2"].Verdict)
	data, err := json.Marshal(r)
	require.NoError(t, err)
	require.Contains(t, string(data), `"by":"delegate:apex-accept"`)
	require.Contains(t, string(data), `"run":"20261017-120000-abc123"`)
	require.Contains(t, r.Next, "archive add-wave")

	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	body := run(t, p.Root, "log", "-1", "--format=%b", "main")
	require.Contains(t, body, "Accepted-By: delegate:apex-accept T2 run 20261017-120000-abc123\nChange: add-wave")
	archived, err := LoadState(filepath.Join(p.Root, res.ArchivedTo, "state.json"))
	require.NoError(t, err)
	require.Equal(t, "delegate:apex-accept", archived.Accepted[0].By)
	require.Equal(t, "20261017-120000-abc123", archived.Accepted[0].Run)
	require.FileExists(t, filepath.Join(p.Root, res.ArchivedTo, "assets", "accept-T2.md"))
}

// With no evidence of its own, a pass rests on the run's verdict file.
func TestDelegate_PassWithoutEvidenceRestsOnTheVerdictFile(t *testing.T) {
	p := delegatedWave(t, "human_review: {}\n")
	dr := startDelegate(t, p, "run-1")
	_, err := p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "pass", "fine", nil)
	require.NoError(t, err)
	leaveSession(t)
	out, err := p.SettleDelegateRun(ctx(), dr)
	require.NoError(t, err)
	require.Equal(t, "_apex/state/runs/delegate/run-1.json", out.Accepted.Evidence[0].Path)
	require.Len(t, out.Accepted.Evidence[0].SHA256, 64)
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
}

// A fail accepts nothing: the task waits for a person, and ape status
// shows why.
func TestDelegate_FailLeavesTheTaskAwaiting(t *testing.T) {
	p := delegatedWave(t, "human_review: {}\n")
	head := HeadSHA(ctx(), p.Root)
	dr := startDelegate(t, p, "run-2")
	_, err := p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "fail", "the greeting is truncated on narrow screens", nil)
	require.NoError(t, err)
	leaveSession(t)
	out, err := p.SettleDelegateRun(ctx(), dr)
	require.NoError(t, err)
	require.Equal(t, "fail", out.Verdict)
	require.Nil(t, out.Accepted)
	require.Equal(t, head, HeadSHA(ctx(), p.Root), "nothing committed")

	r := p.Status()
	c := r.Changes[0]
	require.Equal(t, []string{"T2"}, c.AwaitingHuman)
	require.Equal(t, DelegateStatus{Verdict: "fail", Reason: "the greeting is truncated on narrow screens", Run: "run-2", At: c.Delegate["T2"].At}, c.Delegate["T2"])
	require.Contains(t, r.Brief(), "(T2 delegate fail: the greeting is truncated on narrow screens)")
	require.Contains(t, r.Next, "a person reviews add-wave", "a failed delegate hands the task to a person")
}

// D78: a session that records no verdict failed the task — recorded as a
// fail, "no verdict" — and a fail is not delegated again until the
// change's tip moves.
func TestDelegate_NoVerdictIsAFailNotRetriedOnTheSameTip(t *testing.T) {
	p := delegatedWave(t, "human_review: {}\n")
	dr := startDelegate(t, p, "run-8")
	require.Equal(t, run(t, p.Root, "rev-parse", "change/add-wave"), dr.ChangeTip)
	leaveSession(t)
	out, err := p.SettleDelegateRun(ctx(), dr)
	require.NoError(t, err)
	require.False(t, out.Voided)
	require.Equal(t, "fail", out.Verdict)
	require.Equal(t, NoVerdictReason, out.Reason)
	require.Nil(t, out.Accepted)
	d := p.Status().Changes[0].Delegate["T2"]
	require.Equal(t, "fail", d.Verdict)
	require.Equal(t, "no verdict", d.Reason)
	require.Equal(t, []string{"T2"}, p.Status().Changes[0].AwaitingHuman)

	_, err = p.PrepareDelegate(ctx(), "add-wave", "T2")
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "delegate.already-failed", fe.Code)
	require.Equal(t, ActionPerson, ActionFor(fe.Code))
	require.Contains(t, fe.Msg, "run run-8: no verdict")

	// The change moves (an artefact committed on its branch): the
	// delegate may judge it again.
	write(t, p, "development/changes/add-wave/assets/mock.txt", "v2\n")
	commitAll(t, p, "design: add-wave mock v2")
	_, err = p.PrepareDelegate(ctx(), "add-wave", "T2")
	require.NoError(t, err)
}

// D78: the delegate's evidence is resolved at change/<id>'s tip while the
// change is unarchived — where its design artefacts live — whatever branch
// HEAD is on; the record lands where a person's would (here, the base).
func TestDelegate_EvidenceResolvesAtTheChangeBranchTip(t *testing.T) {
	p := delegatedWave(t, "human_review: {}\n")
	write(t, p, "development/changes/add-wave/assets/mock.png", "png v1\n")
	commitAll(t, p, "design: add-wave mock")
	run(t, p.Root, "switch", "-q", "main")
	require.NoFileExists(t, p.Root+"/development/changes/add-wave/assets/mock.png", "only on change/add-wave")
	write(t, p, "main-only.txt", "x\n")
	commitAll(t, p, "docs: something on main")

	dr := startDelegate(t, p, "run-9")
	_, err := p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "pass", "fine", []string{"main-only.txt"})
	ue, ok := errors.AsType[*UsageError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "usage.evidence", ue.Code, "not on change/add-wave")
	require.Contains(t, ue.Msg, "committed on change/add-wave")
	v, err := p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "pass", "matches the mock",
		[]string{"development/changes/add-wave/assets/mock.png", "https://example.com/run/1"})
	require.NoError(t, err)
	require.Equal(t, "development/changes/add-wave/assets/mock.png", v.Evidence[0].Path)
	require.Len(t, v.Evidence[0].SHA256, 64)
	leaveSession(t)

	out, err := p.SettleDelegateRun(ctx(), dr)
	require.NoError(t, err)
	require.NotNil(t, out.Accepted, "%+v", out)
	require.Equal(t, v.Evidence, out.Accepted.Evidence)
	require.Equal(t, "apex: add-wave accepted T2", subjects(t, p)[0], "on the base, as a person's accept from it")
	require.Contains(t, read(t, p, "development/changes/add-wave/accepted.json"), "assets/mock.png")
}

// Only the delegate session records a verdict.
func TestDelegate_VerdictOnlyInsideItsSession(t *testing.T) {
	p := delegatedWave(t, "human_review: {}\n")
	code := func(err error) string {
		t.Helper()
		ue, ok := errors.AsType[*UsageError](err)
		require.True(t, ok, "%v", err)
		return ue.Code
	}
	_, err := p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "pass", "ok", nil)
	require.Equal(t, "delegate.not-in-session", code(err))

	t.Setenv(EnvDelegate, "add-wave/T2")
	_, err = p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "pass", "ok", nil)
	require.Equal(t, "delegate.not-in-session", code(err), "a marker with no run started")
	require.ErrorContains(t, err, "no delegate run of add-wave T2 is in progress")

	leaveSession(t)
	startDelegate(t, p, "run-3")
	t.Setenv(EnvDelegate, "add-wave/T9")
	_, err = p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "pass", "ok", nil)
	require.Equal(t, "delegate.not-in-session", code(err))
	t.Setenv(EnvDelegate, "add-wave/T2")
	t.Setenv(envDetachedRun, "the-orchestrators-own-run")
	_, err = p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "pass", "ok", nil)
	require.Equal(t, "delegate.not-in-session", code(err), "another run's process")
	t.Setenv(envDetachedRun, "run-3")

	_, err = p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "maybe", "ok", nil)
	require.Equal(t, "usage.verdict", code(err))
	_, err = p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "pass", " ", nil)
	require.Equal(t, "usage.reason", code(err))
	_, err = p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "pass", "ok", []string{"nope.png"})
	require.Equal(t, "usage.evidence", code(err))
	_, err = p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "pass", "ok", nil)
	require.NoError(t, err)
}

// The session is read-only: a commit or a stray file voids its verdict.
func TestDelegate_StrayWorkVoidsTheVerdict(t *testing.T) {
	t.Run("a commit", func(t *testing.T) {
		p := delegatedWave(t, "human_review: {}\n")
		dr := startDelegate(t, p, "run-4")
		write(t, p, "wave.sh", "#!/bin/sh\necho better wave\n")
		commitAll(t, p, "fix: improve it while looking")
		_, err := p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "pass", "now it is good", nil)
		require.NoError(t, err)
		leaveSession(t)
		out, err := p.SettleDelegateRun(ctx(), dr)
		require.NoError(t, err)
		require.True(t, out.Voided)
		require.Len(t, out.Violations, 1)
		require.Contains(t, out.Violations[0], "committed ")
		require.Contains(t, out.Violations[0], "fix: improve it while looking")
		require.Nil(t, out.Accepted)
		require.Empty(t, state(t, p, "add-wave").Accepted)
		d := p.Status().Changes[0].Delegate["T2"]
		require.Equal(t, "pass", d.Verdict)
		require.Equal(t, out.Violations, d.Voided)
		require.Contains(t, p.Status().Brief(), "(T2 delegate voided: committed ")
	})
	t.Run("a file outside its notes, and no verdict", func(t *testing.T) {
		p := delegatedWave(t, "human_review: {}\n")
		dr := startDelegate(t, p, "run-5")
		write(t, p, "development/changes/add-wave/assets/accept-T2.md", "note\n")
		write(t, p, "scratch.txt", "x\n")
		write(t, p, "development/changes/add-wave/assets/other.md", "x\n")
		leaveSession(t)
		out, err := p.SettleDelegateRun(ctx(), dr)
		require.NoError(t, err)
		require.True(t, out.Voided)
		require.ElementsMatch(t, []string{"left scratch.txt", "left development/changes/add-wave/assets/other.md"}, out.Violations)
		require.Empty(t, out.Verdict)
		require.Equal(t, out.Violations, p.LoadDelegateVerdict("run-5").Voided)
	})
}

// A reviewed change accepted from its base (D52 Q18): accepted.json on the
// base, the note with it, the trailer — and the archive squash still names
// the delegated acceptance.
func TestDelegate_FromTheBaseBranch(t *testing.T) {
	p := delegatedWave(t, "human_review: {}\n")
	run(t, p.Root, "switch", "-q", "main")
	dr := startDelegate(t, p, "run-6")
	require.Equal(t, "main", dr.Branch)
	write(t, p, "development/changes/add-wave/assets/accept-T2.md", "seen it\n")
	_, err := p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "pass", "fine", []string{"development/changes/add-wave/assets/accept-T2.md"})
	require.NoError(t, err)
	leaveSession(t)
	out, err := p.SettleDelegateRun(ctx(), dr)
	require.NoError(t, err)
	require.NotNil(t, out.Accepted, "%+v", out)
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
	require.Equal(t, "apex: add-wave accepted T2", subjects(t, p)[0])
	require.Equal(t, "Accepted-By: delegate:apex-accept T2 run run-6\nChange: add-wave", run(t, p.Root, "log", "-1", "--format=%b"))
	require.Contains(t, read(t, p, "development/changes/add-wave/accepted.json"), `"run": "run-6"`)

	run(t, p.Root, "switch", "-q", "change/add-wave")
	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Contains(t, run(t, p.Root, "log", "-1", "--format=%b", "main"), "Accepted-By: delegate:apex-accept T2 run run-6")
}

// An archived change's task (archived past it by a person) is delegated
// on the base: the archived state.json records it.
func TestDelegate_ArchivedChange(t *testing.T) {
	p := delegatedWave(t, "human_review: {}\n")
	res, err := p.Archive(ctx(), "add-wave", true, RunOptions{})
	require.NoError(t, err)
	dr := startDelegate(t, p, "run-7")
	require.Equal(t, res.ArchivedTo, dr.Folder)
	require.Contains(t, dr.Args, "--task T2 --change add-wave")
	note := res.ArchivedTo + "/assets/accept-T2.md"
	write(t, p, note, "ok\n")
	_, err = p.RecordDelegateVerdict(ctx(), "add-wave", "T2", "pass", "fine", nil)
	require.NoError(t, err)
	leaveSession(t)
	out, err := p.SettleDelegateRun(ctx(), dr)
	require.NoError(t, err)
	require.NotNil(t, out.Accepted, "%+v", out)
	require.Equal(t, "apex: add-wave accepted T2 (archived)", subjects(t, p)[0])
	require.Contains(t, run(t, p.Root, "log", "-1", "--format=%b"), "Accepted-By: delegate:apex-accept T2 run run-7")
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
	archived, err := LoadState(filepath.Join(p.Root, res.ArchivedTo, "state.json"))
	require.NoError(t, err)
	require.Equal(t, "run-7", archived.Accepted[0].Run)
	require.Empty(t, p.Status().Changes, "nothing awaits a person any more")
}

// The edit hook confines a delegate session to its notes.
func TestPreTool_DelegateSessionIsReadOnly(t *testing.T) {
	p := fixture(t, baseFiles())
	t.Setenv(EnvDelegate, "add-wave/T2")
	outside := filepath.Join(t.TempDir(), "copy", "greet.sh")
	for path, deny := range map[string]bool{
		"development/changes/add-wave/assets/accept-T2.md": false,
		outside:                                  false,
		"greet.sh":                               true,
		"development/changes/add-wave/change.md": true,
		"development/changes/add-wave/assets/review-ui.md":     true,
		"development/changes/add-wave/assets/accept-x/deep.md": true,
		"development/changes/other-one/assets/accept-T2.md":    true,
	} {
		d := PreTool(editPayload(t, p.Root, "Write", path))
		require.Equal(t, deny, d.Deny, path)
		if deny {
			require.Contains(t, d.Reason, "read-only delegate session for T2 of change add-wave")
		}
	}
}
