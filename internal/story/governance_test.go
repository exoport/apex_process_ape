package story

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// --- 9.12b, the adrs_considered recomputation --------------------------

// TestFinding_CandidatesOmittedWhenEmpty — every other check's finding
// keeps its existing shape on the wire.
func TestFinding_CandidatesOmittedWhenEmpty(t *testing.T) {
	plain, err := json.Marshal(Finding{Check: "story.x", Path: "p", Message: "m"})
	require.NoError(t, err)
	require.NotContains(t, string(plain), "candidates")

	withCands, err := json.Marshal(Finding{
		Check: CheckADRsConsidered, Path: "p", Message: "m",
		Candidates: []Candidate{{ID: "ADR-0001", Tag: "wiring"}},
	})
	require.NoError(t, err)
	require.Contains(t, string(withCands), `"candidates":[{"id":"ADR-0001","tag":"wiring"}]`)
}

// --- 13.3b, ids resolve at HEAD ----------------------------------------
