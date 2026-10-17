package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// L8, from radix: its ADR-NN comments cite a research repository's
// decision log, "not renumbered onto this repository's own governance
// identifiers". Only the project's own form, ADR-NNNN, is rewritten; a
// short id is listed, even when the project has a record of that number.
func TestADRRewriter_OnlyTheProjectsOwnForm(t *testing.T) {
	rw := newADRRewriter(map[int]int{1: 1, 8: 2}, nil, "")
	out, n, unresolved := rw.rewrite("Per ADR-08 and ADR-01/05, ADR-18 (research); ours: ADR-0008, ADR-0001/0008, ADR-0099.")
	require.Equal(t, "Per ADR-08 and ADR-01/05, ADR-18 (research); ours: ADR-0002, ADR-0001/0002, ADR-0099.", out)
	require.Equal(t, 2, n)
	require.Equal(t, []string{"ADR-08", "ADR-01", "ADR-18", "ADR-0099"}, unresolved)
	require.Equal(t, []int{8, 1, 8, 99}, citedADRs("ADR-08 ADR-0008 ADR-0001/0008 ADR-0099 ADR-7"), "short forms are no citation of the project's")
}

func TestEditComments_ShortADRIdsListedNotRewritten(t *testing.T) {
	rw := newADRRewriter(map[int]int{8: 2}, nil, "")
	src := "package x\n\n// Follows ADR-08 (research log), unlike ADR-0008.\nvar x = 1\n"
	res, err := editComments("x.go", src, langGo, testVocab(), rw)
	require.NoError(t, err)
	require.Equal(t, "package x\n\n// Follows ADR-08 (research log), unlike ADR-0002.\nvar x = 1\n", res.New)
	require.Equal(t, []LiftIDRef{{File: "x.go", Line: 3, Text: "ADR-08", Where: "comment"}}, res.Unresolved)
}

// L9, from the dry runs (radix, atomix, tenax, cortex): the punctuation
// around a removed id goes with it, and so do the story's fragments.
func TestStripIDsFromText_PunctuationAndFragments(t *testing.T) {
	v := testVocab()
	for in, want := range map[string]string{
		" Bounded (ADR-0019, Story 3.2).":               " Bounded (ADR-0019).",
		" Ratchet (ADR-0180; Story 3.2) holds.":         " Ratchet (ADR-0180) holds.",
		" Carried (Story 3.2, ADR-0017 honesty":         " Carried (ADR-0017 honesty",
		" Per ADR-0011 / Story 4.2, Task 2: the gate":   " Per ADR-0011: the gate",
		" Lives in (tools/verify/, Story 3.2) for now":  " Lives in (tools/verify/) for now",
		" Retry (Story 3.2, Task 2) twice.":             " Retry twice.",
		" Story 5.10 Subtask 3.1: paginate the trace":   " Paginate the trace",
		" Task 2 of Story 3.2 adds a retry.":            " Adds a retry.",
		" Story 3.2 AC 2 and AC 3: bounded":             " Bounded",
		" Retries a, Story 3.2, b and c.":               " Retries a, b and c.",
		" Bounded (ADR-0019 and Story 3.2) on purpose.": " Bounded (ADR-0019) on purpose.",
		" Task 2 stays: no story next to it (Epic 4).":  " Task 2 stays: no story next to it.",
		" Subtask 3.1 of the plan, not of a story id.":  " Subtask 3.1 of the plan, not of a story id.",
	} {
		spans := v.spans(in)
		got, empty := stripIDsFromText(in, spans)
		if len(spans) == 0 {
			got = in
		}
		require.False(t, empty, in)
		require.Equal(t, want, got, in)
	}
}
