package apexv1

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// waveChange is a change whose T1 is red until wave.sh exists.
func waveChange(id string) string {
	return changeDoc(id, "greeting", addedWave,
		"- T1 add wave — check: ./wave.sh\n- T2 copy reads well — check: human-review\n")
}

func startedWave(t *testing.T) *Project {
	t.Helper()
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	return p
}

func state(t *testing.T, p *Project, id string) *State {
	t.Helper()
	st, err := LoadState(p.StateFile(id))
	require.NoError(t, err)
	return st
}

func TestNewChange(t *testing.T) {
	p := fixture(t, baseFiles())
	res, err := p.NewChange(ctx(), "add-wave", "M1", []string{"greeting"}, []string{"internal/**"})
	require.NoError(t, err)
	require.Equal(t, "development/changes/add-wave/change.md", res.Path)
	require.Equal(t, StatusDraft, state(t, p, "add-wave").Status)
	require.Equal(t, "apex: add-wave draft", subjects(t, p)[0], "D11: the skeleton and draft state are committed")
	require.NotEmpty(t, res.Commit)
	clean(t, p, "change new")
	doc := read(t, p, res.Path)
	require.Contains(t, doc, "- farewells", "the milestone's out of scope is inherited")
	c := ParseChange("", doc)
	require.Equal(t, "add-wave", c.Meta.ID)
	require.Equal(t, []string{"internal/**"}, c.Meta.Paths)
	require.NotEmpty(t, p.CheckChange("add-wave"), "the skeleton is not a finished change")

	res2, err := p.NewChange(ctx(), "add-wave-two", "M1", []string{"greeting"}, []string{"internal/api/**"})
	require.NoError(t, err)
	require.Len(t, res2.Overlaps, 1)
	require.Equal(t, "add-wave", res2.Overlaps[0].With)

	for _, tc := range []struct {
		id, ms  string
		touches []string
		usage   bool
		msg     string
	}{
		{"Add-Wave", "M1", []string{"greeting"}, true, "must match"},
		{"ab", "M1", []string{"greeting"}, true, "must match"},
		{"add-wave", "M1", []string{"greeting"}, false, "already exists"},
		{"add-other", "M7", []string{"greeting"}, false, "not in roadmap.md"},
		{"add-other", "M1", nil, true, "--touches"},
		{"add-other", "M1", []string{"Bad"}, true, "kebab-case"},
	} {
		_, err := p.NewChange(ctx(), tc.id, tc.ms, tc.touches, nil)
		require.ErrorContains(t, err, tc.msg, tc.id)
		var ue *UsageError
		require.Equal(t, tc.usage, errors.As(err, &ue), tc.id)
	}
}

func TestNewChange_IDsAreUniqueAcrossTheArchive(t *testing.T) {
	files := baseFiles()
	files["development/archive/2026-09-01-add-wave/change.md"] = "old"
	// An older ape archived inside changes/: its ids stay taken too, the
	// one read of the old place besides project check's report.
	files["development/changes/archive/2026-08-01-add-hug/change.md"] = "old"
	p := fixture(t, files)
	_, err := p.NewChange(ctx(), "add-wave", "M1", []string{"greeting"}, nil)
	require.ErrorContains(t, err, "already archived as development/archive/2026-09-01-add-wave")
	_, err = p.NewChange(ctx(), "add-hug", "M1", []string{"greeting"}, nil)
	require.ErrorContains(t, err, "already archived as development/changes/archive/2026-08-01-add-hug")
	require.Equal(t, map[string]string{"add-wave": "2026-09-01-add-wave"}, p.ArchivedIDs(), "nothing else reads the old archive")
	require.Equal(t, []string{}, changeIDs(p.Changes()), "the old archive folder is not a change")
}

func changeIDs(cs []ChangeInfo) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func TestStart(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan")
	head := HeadSHA(ctx(), p.Root)

	res, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, head, res.BaseSHA)
	// The baseline is everything verify will run: the command task, the
	// untouched spec's scenarios (regression) and the delta's (item 2).
	require.Len(t, res.Baseline, 4, "human-review tasks have no baseline")
	require.Equal(t, 127, res.Baseline[0].Exit)
	require.Equal(t, KindScenario, res.Baseline[1].Kind)
	require.Equal(t, "greeting/Say hello/plain run", res.Baseline[1].Task)
	require.Equal(t, 0, res.Baseline[1].Exit, "the regression scenarios are green")
	require.Equal(t, "greeting/Wave/wave", res.Baseline[3].Task)
	require.True(t, res.Baseline[3].Delta)
	require.Equal(t, 127, res.Baseline[3].Exit, "the change's own scenario is red before the code")

	st := state(t, p, "add-wave")
	require.Equal(t, StatusInProgress, st.Status)
	require.Equal(t, Baseline{Task: "T1", Exit: 127, Cmd: "./wave.sh", SHA: head}, st.Baseline[0])
	require.Len(t, st.Baseline, 4)
	require.Equal(t, ParseChange("", read(t, p, "development/changes/add-wave/change.md")).PlanDigest(), st.PlanDigest)
	require.Equal(t, "apex: add-wave in-progress", subjects(t, p)[0], "D5: ape commits state.json")
	require.Contains(t, run(t, p.Root, "log", "-1", "--format=%b"), "Change: add-wave")
	require.Equal(t, "development/changes/add-wave/state.json", run(t, p.Root, "show", "--name-only", "--format=", "HEAD"))

	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.ErrorContains(t, err, "is in-progress")
}

func TestStart_Refusals(t *testing.T) {
	t.Run("check red", func(t *testing.T) {
		p := fixture(t, baseFiles())
		addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", "", ""))
		_, err := p.Start(ctx(), "add-wave", RunOptions{})
		var fe *FailError
		require.ErrorAs(t, err, &fe)
		require.NotEmpty(t, fe.Findings)
		require.Equal(t, StatusDraft, state(t, p, "add-wave").Status)
	})
	t.Run("milestone not approved", func(t *testing.T) {
		files := baseFiles()
		files["development/roadmap.md"] = strings.Replace(roadmapApproved, "approved (by eval 2026-10-15T12:00:00Z)", "proposed", 1)
		p := fixture(t, files)
		addChange(t, p, "add-wave", waveChange("add-wave"))
		_, err := p.Start(ctx(), "add-wave", RunOptions{})
		require.ErrorContains(t, err, "not approved")
	})
}

func TestStart_RequirementConflicts(t *testing.T) {
	modify := func(id string) string {
		return changeDoc(id, "greeting",
			"### MODIFIED [greeting] Requirement: Say hello\nThe system SHALL print Hello, then Hi.\n#### Scenario: plain run\n- GIVEN nothing WHEN greet runs THEN it prints Hello\n- check: ./greet.sh | grep -q Hello\n#### Scenario: hi\n- check: true\n",
			"- T1 hi — check: true\n")
	}
	remove := func(id string) string {
		return changeDoc(id, "greeting", "### REMOVED [greeting] Requirement: Say hello\nReason: r\n", "- T1 gone — check: true\n")
	}
	for name, tc := range map[string]struct {
		first, second string
		firstStatus   string
		conflict      bool
	}{
		"two MODIFIED":           {modify("a-first"), modify("b-second"), StatusInProgress, true},
		"MODIFIED vs REMOVED":    {modify("a-first"), remove("b-second"), StatusVerified, true},
		"two ADDED of one name":  {waveChange("a-first"), waveChange("b-second"), StatusReviewed, true},
		"other is blocked (D6)":  {modify("a-first"), modify("b-second"), StatusBlocked, false},
		"other is a draft":       {modify("a-first"), modify("b-second"), StatusDraft, false},
		"different requirements": {modify("a-first"), waveChange("b-second"), StatusInProgress, false},
	} {
		t.Run(name, func(t *testing.T) {
			p := fixture(t, baseFiles())
			first := strings.Replace(tc.first, "id: "+idOf(tc.first), "id: a-first", 1)
			addChange(t, p, "a-first", first)
			require.NoError(t, SaveState(p.StateFile("a-first"), &State{ID: "a-first", Status: tc.firstStatus}))
			addChange(t, p, "b-second", tc.second)
			commitAll(t, p, "plan")
			_, err := p.Start(ctx(), "b-second", RunOptions{})
			if !tc.conflict {
				require.NoError(t, err)
				return
			}
			var ce *ConflictError
			require.ErrorAs(t, err, &ce)
			require.Contains(t, ce.Error(), "a-first")
			require.Equal(t, StatusDraft, state(t, p, "b-second").Status, "a refused start writes nothing")
		})
	}
}

func idOf(doc string) string {
	c := ParseChange("", doc)
	return c.Meta.ID
}

func TestVerify(t *testing.T) {
	p := startedWave(t)
	before := subjects(t, p)

	// Red on an in-progress change: nothing is written to state.json and
	// nothing is committed (D5, restored by D38); the attempt is counted in
	// the ignored _apex/state/attempts.json (backlog 30).
	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.False(t, v.Green)
	require.Len(t, v.Failing(), 2, "the task and the delta's own scenario")
	require.NotEmpty(t, v.Failing()[0].Tail)
	require.Equal(t, KindScenario, v.Failing()[1].Kind)
	require.Equal(t, 1, v.Attempts)
	require.Equal(t, DefaultAttempts, v.AttemptsCap)
	st := state(t, p, "add-wave")
	require.Nil(t, st.Verify)
	require.Equal(t, Attempts{Since: 1, Total: 1}, p.attempts("add-wave"))
	require.Equal(t, before, subjects(t, p), "a red verify commits nothing")
	clean(t, p, "red verify")

	// Uncommitted code: refused.
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.ErrorContains(t, err, "uncommitted changes outside development/: wave.sh")

	commitAll(t, p, "feat: wave")
	v, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	require.Equal(t, []string{"T2"}, v.PendingHuman)
	st = state(t, p, "add-wave")
	require.Equal(t, StatusVerified, st.Status)
	require.Equal(t, 1, p.attempts("add-wave").Since, "a green verify does not reset the count")
	require.Equal(t, v.SHA, st.Verify.SHA)
	require.Len(t, st.Verify.Results[0].OutputSHA256, 64)
	require.Equal(t, "apex: add-wave verified", subjects(t, p)[0])

	// Code breaks again: a verified change goes back to in-progress, committed.
	write(t, p, "wave.sh", "#!/bin/sh\nexit 3\n")
	commitAll(t, p, "fix: break wave")
	v, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.False(t, v.Green)
	require.Equal(t, 3, v.Results[0].Exit)
	st = state(t, p, "add-wave")
	require.Equal(t, StatusInProgress, st.Status)
	require.False(t, st.Verify.Green, "the red verify is recorded on the way back")
	require.Equal(t, 2, v.Attempts)
	require.Equal(t, "apex: add-wave in-progress", subjects(t, p)[0], "a status change, committed on the change's branch")
	require.Equal(t, ChangeBranch("add-wave"), CurrentBranch(ctx(), p.Root))
}

// Backlog 30: the red verify that reaches limits.attempts blocks the
// change, with a reason naming the count and the failing checks; a
// re-start resets the count but not the running total.
func TestVerify_AttemptsCapBlocks(t *testing.T) {
	p := startedWave(t)
	write(t, p, "_apex/config.yaml", "limits:\n  attempts: 2\n")
	commitAll(t, p, "config: attempts 2")

	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, v.Attempts)
	require.Equal(t, 2, v.AttemptsCap)

	v, err = p.Verify(ctx(), "add-wave", RunOptions{})
	var fe *FailError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, "change.attempts-exceeded", fe.Code)
	require.NotNil(t, v, "the red results come back beside the error")
	require.Equal(t, StatusBlocked, v.Status)
	st := state(t, p, "add-wave")
	require.Equal(t, StatusBlocked, st.Status)
	require.NotNil(t, st.BlockedReason)
	require.Equal(t, "attempts exceeded: 2 red verifies — T1 (exit 127), greeting/Wave/wave (exit 127)", *st.BlockedReason)
	require.Equal(t, "apex: add-wave blocked", subjects(t, p)[0])
	clean(t, p, "attempts cap")

	// Blocked: verify no longer applies, so nothing more is counted.
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.ErrorContains(t, err, "is blocked")

	// ape status shows the reason; a resumed change starts a fresh count.
	r := p.Status()
	require.Equal(t, "attempts exceeded: 2 red verifies — T1 (exit 127), greeting/Wave/wave (exit 127)", r.Changes[0].BlockedReason)
	require.Equal(t, 2, r.Changes[0].Attempts, "a change the cap blocked still shows its count")
	require.Equal(t, 2, r.Changes[0].AttemptsCap, "… against the cap that blocked it")
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, Attempts{Since: 0, Total: 2}, p.attempts("add-wave"))
	v, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, v.Attempts)
	require.Equal(t, 3, v.AttemptsTotal)

	r = p.Status()
	require.Equal(t, 1, r.Changes[0].Attempts)
	require.Equal(t, 3, r.Changes[0].AttemptsTotal)
	require.Equal(t, 2, r.Changes[0].AttemptsCap)
	require.Contains(t, r.Brief(), "(1/2 attempts)")
}

func TestVerify_DraftAndArchived(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", waveChange("add-wave"))
	_, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.ErrorContains(t, err, "is a draft")
	_, err = p.Verify(ctx(), "nope-nope", RunOptions{})
	require.ErrorContains(t, err, "no change nope-nope")
}

func verifiedWave(t *testing.T) *Project {
	t.Helper()
	p := startedWave(t)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	write(t, p, "development/changes/add-wave/review.md", "fine\n")
	return p
}

func TestReview(t *testing.T) {
	p := verifiedWave(t)
	_, err := p.Review(ctx(), "add-wave", "maybe", "development/changes/add-wave/review.md")
	var ue *UsageError
	require.ErrorAs(t, err, &ue)
	_, err = p.Review(ctx(), "add-wave", "pass", "nowhere.md")
	require.ErrorAs(t, err, &ue)

	st, err := p.Review(ctx(), "add-wave", "block", "development/changes/add-wave/review.md")
	require.NoError(t, err)
	require.Equal(t, StatusInProgress, st.Status)
	require.Equal(t, "apex: add-wave in-progress", subjects(t, p)[0])

	_, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.ErrorContains(t, err, "review applies to a verified change")

	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	st, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.NoError(t, err)
	require.Equal(t, StatusReviewed, st.Status)
	require.Equal(t, "development/changes/add-wave/review.md", st.Review.Findings)

	// A re-verify with no code change keeps it reviewed.
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, StatusReviewed, state(t, p, "add-wave").Status)
}

func TestReview_PassNeedsAFreshVerify(t *testing.T) {
	p := verifiedWave(t)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave2\n")
	commitAll(t, p, "feat: wave2")
	_, err := p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.ErrorContains(t, err, "code changed since the last verify (wave.sh)")
}

func TestAcceptAndBlock(t *testing.T) {
	p := verifiedWave(t)
	_, err := p.Accept(ctx(), "add-wave", "T1", "ann")
	require.ErrorContains(t, err, "proven by `./wave.sh`")
	_, err = p.Accept(ctx(), "add-wave", "T9", "ann")
	require.ErrorContains(t, err, "no task T9")
	_, err = p.Accept(ctx(), "add-wave", "T2", "")
	require.ErrorContains(t, err, "--by")

	st, err := p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	require.Equal(t, "ann", st.Accepted[0].By)
	require.Empty(t, st.Verify.PendingHuman)
	require.Equal(t, "apex: add-wave verified", subjects(t, p)[0])

	_, err = p.Block(ctx(), "add-wave", " ")
	require.ErrorContains(t, err, "--reason")
	st, err = p.Block(ctx(), "add-wave", "which wave?")
	require.NoError(t, err)
	require.Equal(t, StatusBlocked, st.Status)
	require.Equal(t, "which wave?", *st.BlockedReason)
	require.Equal(t, "apex: add-wave blocked", subjects(t, p)[0])

	// start resumes a blocked change and keeps its base.
	base := st.BaseSHA
	res, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, base, res.BaseSHA)
	require.Nil(t, state(t, p, "add-wave").BlockedReason)
}

func TestAbandon(t *testing.T) {
	p := startedWave(t)
	_, err := p.Abandon(ctx(), "add-wave", "")
	require.ErrorContains(t, err, "--reason")
	res, err := p.Abandon(ctx(), "add-wave", "replaced by add-wave-v2")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(res.ArchivedTo, "development/archive/") && strings.HasSuffix(res.ArchivedTo, "-add-wave"), res.ArchivedTo)
	require.NoDirExists(t, p.ChangeDir("add-wave"))
	st, err := LoadState(filepath.Join(p.Root, res.ArchivedTo, "state.json"))
	require.NoError(t, err)
	require.Equal(t, StatusAbandoned, st.Status)
	require.Equal(t, "replaced by add-wave-v2", st.AbandonedReason)
	require.Equal(t, greetingSpec, read(t, p, "development/specs/greeting/spec.md"), "no delta merged")
	require.Equal(t, "apex: add-wave abandoned", subjects(t, p)[0])
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
	_, err = p.NewChange(ctx(), "add-wave", "M1", []string{"greeting"}, nil)
	require.ErrorContains(t, err, "already archived", "an abandoned id stays taken")
}

func TestStateCommit_LeavesOtherWorkAlone(t *testing.T) {
	p := startedWave(t)
	write(t, p, "notes.txt", "staged by someone\n")
	run(t, p.Root, "add", "notes.txt")
	_, err := p.Block(ctx(), "add-wave", "parked")
	require.NoError(t, err)
	require.Equal(t, "development/changes/add-wave/state.json", run(t, p.Root, "show", "--name-only", "--format=", "HEAD"))
	require.Equal(t, "A  notes.txt", run(t, p.Root, "status", "--porcelain"), "the operator's staged file stays staged")
}

func TestStateWritesOutsideGitStillWork(t *testing.T) {
	root := t.TempDir()
	for rel, content := range baseFiles() {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
	p, err := Open(root)
	require.NoError(t, err)
	addChange(t, p, "add-wave", waveChange("add-wave"))
	_, err = p.Block(ctx(), "add-wave", "why")
	require.NoError(t, err)
	require.Equal(t, StatusBlocked, state(t, p, "add-wave").Status)
}
