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
