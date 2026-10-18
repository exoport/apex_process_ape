package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// D111 (Q52): a check that reads as human-review but is not exactly
// `human-review` is a change check finding, not a shell command that
// fails only when verify runs it. A command whose first word merely begins
// with the letters is not one.
func TestCheckChange_MalformedHumanReview(t *testing.T) {
	p := fixture(t, baseFiles())
	for check, refused := range map[string]bool{
		"human-review: read the README": true,
		"human-review: x":               true,
		"human-review.":                 true,
		"Human-Review":                  true,
		"HUMAN-REVIEW":                  true,
		"human review x":                true,
		"Human review of the copy":      true,
		"human-review":                  false,
		"  human-review  ":              false,
		"humanreview.sh":                false,
		"human-reviewer":                false,
		"./human-review.sh":             false,
	} {
		t.Run(check, func(t *testing.T) {
			addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", addedWave,
				"- T1 add wave — check: ./wave.sh\n- T2 copy reads well — check: "+check+"\n"))
			fs := ClassifyFindings(p.CheckChange("add-wave"))
			if !refused {
				require.Empty(t, fs)
				return
			}
			require.Len(t, fs, 1)
			require.Equal(t, CodeMalformedHumanReview, fs[0].Code)
			require.Equal(t, ActionReplan, ActionFor(fs[0].Code))
			require.Equal(t, 21, fs[0].Line, "the T2 line")
			require.Contains(t, fs[0].Message, "task T2's check `"+check+"` is not exactly human-review")
			require.Contains(t, fs[0].Fix, "write the task as `- T2 <what> — check: human-review`, the artefact and the criteria in <what>, before ` — check:`")
		})
	}
}

// Start refuses a change with a malformed human-review check, through the
// change check findings it already refuses on.
func TestStart_RefusesAMalformedHumanReview(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", addedWave,
		"- T1 add wave — check: ./wave.sh\n- T2 copy reads well — check: human-review: the README reads well\n"))
	commitAll(t, p, "plan: add-wave")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	var fe *FailError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, CodeCheckFailed, fe.Code)
	require.Contains(t, findingMessages(fe.Findings), "task T2's check `human-review: the README reads well` is not exactly human-review")
	require.Equal(t, StatusDraft, state(t, p, "add-wave").Status)
}
