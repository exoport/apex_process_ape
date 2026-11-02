package apecmd

import (
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// FINDING-V15-1A-13: a refused take-in or rebase names where its conflict
// is laid out as --json fields — conflict {change, branch, kind, files} and
// conflicts — so a skill need not parse the sentence (eval's stub read a
// top-level id and found none). The control: error is the same text as
// before, byte for byte, and a conflict with no place keeps the old shape.
func TestV1Fail_ConflictFields(t *testing.T) {
	render := func(t *testing.T, err error) map[string]any {
		t.Helper()
		cmd := &cobra.Command{}
		var out jsonBuf
		cmd.SetOut(&out)
		f := &v1Flags{json: true}
		ret := v1Fail(cmd, f, err, nil)
		require.Error(t, ret)
		var m map[string]any
		require.NoError(t, json.Unmarshal(out.Bytes(), &m), out.String())
		return m
	}
	takeIn := &apexv1.ConflictError{
		Code: apexv1.CodeConflicted, Action: apexv1.ActionApply,
		Msg:    "main does not merge cleanly into milestone/M7: take-in take-in-1 is laid out on change/take-in-1 with the conflict markers in docs/operations.md — apex-apply resolves them, `git add` and `git commit --no-edit`, then run the take-in again",
		Change: "take-in-1", Branch: "change/take-in-1", Kind: "take-in", Files: []string{"docs/operations.md"},
	}
	m := render(t, takeIn)
	require.Equal(t, takeIn.Msg, m["error"], "the text is as before")
	require.Equal(t, map[string]any{"change": "take-in-1", "branch": "change/take-in-1", "kind": "take-in", "files": []any{"docs/operations.md"}}, m["conflict"])
	require.Equal(t, []any{"docs/operations.md"}, m["conflicts"])
	require.EqualValues(t, exitV1Conflict, m["exit"])

	rebase := &apexv1.ConflictError{
		Code: apexv1.CodeConflicted, Msg: "main moved, and change add-wave does not rebase onto it", Conflicts: []string{"wave.sh"},
		Change: "add-wave", Branch: "change/add-wave", Kind: "rebase", Files: []string{"wave.sh"},
	}
	m = render(t, rebase)
	require.Equal(t, "main moved, and change add-wave does not rebase onto it: wave.sh", m["error"])
	c, ok := m["conflict"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "rebase", c["kind"])
	require.Equal(t, []any{"wave.sh"}, m["conflicts"])

	// The control: a conflict with no place (a run lock) keeps its shape.
	m = render(t, &apexv1.ConflictError{Code: apexv1.CodeRunBusy, Msg: "busy"})
	require.NotContains(t, m, "conflict")
	require.NotContains(t, m, "conflicts")
	require.Equal(t, "busy", m["error"])
}

type jsonBuf struct{ b []byte }

func (j *jsonBuf) Write(p []byte) (int, error) { j.b = append(j.b, p...); return len(p), nil }
func (j *jsonBuf) Bytes() []byte               { return j.b }
func (j *jsonBuf) String() string              { return string(j.b) }
