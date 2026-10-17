package apexv1

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// H9: a person answers a blocked change; the answer is recorded where the
// change's state lives, and start takes the answered change.
func TestAnswer(t *testing.T) {
	p := startedWave(t)
	_, err := p.AnswerChange(ctx(), "add-wave", "wave twice", "ann")
	require.ErrorContains(t, err, "answer applies to a blocked change")
	_, err = p.Block(ctx(), "add-wave", "wave once or twice?")
	require.NoError(t, err)
	run(t, p.Root, "switch", "-q", "main")
	mainHead := HeadSHA(ctx(), p.Root)

	t.Setenv("CLAUDECODE", "1")
	_, err = p.AnswerChange(ctx(), "add-wave", "twice", "ann")
	ue, ok := errors.AsType[*UsageError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodePersonOnly, ue.Code)
	t.Setenv("CLAUDECODE", "")
	_, err = p.AnswerChange(ctx(), "add-wave", " ", "ann")
	require.ErrorContains(t, err, "--text is required")

	// From the base branch: committed on change/add-wave, HEAD untouched.
	st, err := p.AnswerChange(ctx(), "add-wave", "twice", "ann")
	require.NoError(t, err)
	require.Equal(t, StatusAnswered, st.Status)
	require.Equal(t, "twice", st.Answer.Text)
	require.Equal(t, mainHead, HeadSHA(ctx(), p.Root))
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
	require.Equal(t, "apex: add-wave answered", run(t, p.Root, "log", "-1", "--format=%s", "change/add-wave"))
	require.Equal(t, "Change: add-wave", run(t, p.Root, "log", "-1", "--format=%b", "change/add-wave"))

	r := p.Status()
	require.Equal(t, StatusAnswered, r.Changes[0].Status)
	require.Equal(t, "ann", r.Changes[0].Answer.By)
	require.Contains(t, r.Next, "answered: add-wave — ann answered; re-plan it with the answer as the hint")
	require.Contains(t, r.Brief(), "answered by ann: twice")
	data, err := json.Marshal(r)
	require.NoError(t, err)
	require.Contains(t, string(data), `"answer":{"by":"ann","at":`)

	_, err = p.AnswerChange(ctx(), "add-wave", "again", "ann")
	require.ErrorContains(t, err, "is answered")

	res, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, StatusInProgress, res.Status)
	st = state(t, p, "add-wave")
	require.Equal(t, "twice", st.Answer.Text, "the answer stays on record")
	require.Nil(t, st.BlockedReason)
}

// A draft blocked before any start has no branch: its answer lands on the
// branch it was planned on.
func TestAnswer_DraftWithoutBranch(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan")
	_, err := p.Block(ctx(), "add-wave", "which greeting?")
	require.NoError(t, err)
	_, err = p.AnswerChange(ctx(), "add-wave", "hello", "ann")
	require.NoError(t, err)
	require.Equal(t, "apex: add-wave answered", subjects(t, p)[0])
	require.Equal(t, StatusAnswered, state(t, p, "add-wave").Status)
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
}
