package registry

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVerify_EntryIncompleteFollowsEachFamilysSchema: the required set is
// per family, so a complete capability entry — no file:, no title — is
// clean, and the same entry missing components is not.
func TestVerify_EntryIncompleteFollowsEachFamilysSchema(t *testing.T) {
	f := newFixture(t)
	f.record("capabilities", "cap-1_a.md", "CAP-1")
	f.record("capabilities", "cap-2_b.md", "CAP-2")
	f.capabilityIndex("CAP-1", "CAP-2")
	require.Empty(t, f.verify("capabilities").Findings)

	f.raw("capabilities", IndexFileName, "generated_at: '20260101000000'\ncapabilities:\n"+
		"  - id: CAP-1\n    slug: a\n    name: A\n    status: accepted\n"+
		"    created_at: '20260101000000'\n    updated_at: '20260101000000'\n"+
		"  - id: CAP-2\n    slug: b\n    name: B\n    status: accepted\n    components: []\n"+
		"    created_at: '20260101000000'\n    updated_at: '20260101000000'\n")
	got := findingsOf(f.verify("capabilities"), CheckEntryIncomplete)
	require.Len(t, got, 1)
	require.Equal(t, "CAP-1", got[0].ID)
	require.Contains(t, got[0].Message, "components")
}
