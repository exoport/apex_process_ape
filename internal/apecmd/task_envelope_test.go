package apecmd

import (
	"encoding/json"
	"testing"

	"github.com/exoport/apex_process_ape/internal/commitowners"
	"github.com/exoport/apex_process_ape/internal/story"
	"github.com/stretchr/testify/require"
)

// TestTaskEnvelope_CommitContractKey pins the JSON key the framework's
// acceptance block greps for. It had no coverage: the field name lived in
// one struct tag and one CHANGELOG line, and nothing tied them together.
func TestTaskEnvelope_CommitContractKey(t *testing.T) {
	res := commitowners.Result{Skill: "apex-dev-story", Declared: false}
	blob, err := json.Marshal(taskEnvelope{Skill: "apex-dev-story", CommitContract: &res})
	require.NoError(t, err)
	require.Contains(t, string(blob), `"commit_contract"`,
		"the framework greps this key by name")

	var back map[string]any
	require.NoError(t, json.Unmarshal(blob, &back))
	contract, ok := back["commit_contract"].(map[string]any)
	require.True(t, ok, "commit_contract is an object, not a string")
	require.Equal(t, "apex-dev-story", contract["skill"])
	require.Equal(t, false, contract["declared"])
}

// TestTaskEnvelope_CommitContractAlwaysPresent is the field's own stated
// contract: "a consumer must be able to tell 'asserted and clean' from
// 'could not assert', and a field that appears only on failure cannot".
//
// It is `omitempty` on a POINTER, so it survives only because every path
// assigns a non-nil one. This asserts the clean case still emits it,
// which is the case an omitempty on a value type would have dropped.
func TestTaskEnvelope_CommitContractAlwaysPresent(t *testing.T) {
	clean := commitowners.Result{Skill: "apex-shard-doc", Declared: false}
	require.True(t, clean.OK())

	blob, err := json.Marshal(taskEnvelope{Skill: "apex-shard-doc", CommitContract: &clean})
	require.NoError(t, err)
	require.Contains(t, string(blob), `"commit_contract"`,
		"a clean verdict is still reported — silence would read as 'not asserted'")
}

// TestTaskEnvelope_SkippedContractIsDistinguishable closes the loop with
// internal/commitowners' own test: through the envelope, a skip and a
// clean pass must not look alike.
func TestTaskEnvelope_SkippedContractIsDistinguishable(t *testing.T) {
	skipped := commitowners.Skipped("apex-shard-doc", "--task-commit: ape makes the commit")
	clean := commitowners.Result{Skill: "apex-shard-doc"}

	skipBlob, err := json.Marshal(taskEnvelope{CommitContract: &skipped})
	require.NoError(t, err)
	cleanBlob, err := json.Marshal(taskEnvelope{CommitContract: &clean})
	require.NoError(t, err)

	require.NotEqual(t, string(skipBlob), string(cleanBlob),
		"a dispatch that asserted nothing must not marshal like one that asserted and passed")
	require.Contains(t, string(skipBlob), `"skipped":true`)
	require.NotContains(t, string(cleanBlob), `"skipped"`)
}

// TestSummaryLinesCannotBeQuotedWithoutTheirSkips is the rule the
// framework session and I arrived at after three gates whose skip read as
// a pass: **a skip is only reported if the summary cannot be quoted
// without it.**
//
// None of those three failed to print the skip. Each printed it honestly,
// on its own line. What defeated us was the summary ABOVE it — because
// the summary is what gets quoted into a message, pasted into a report,
// and carried forward as evidence. A total that counts skips as
// not-failures converts an absent guarantee into a believed-present one
// at the moment someone decides to trust it.
func TestSummaryLinesCannotBeQuotedWithoutTheirSkips(t *testing.T) {
	t.Run("task: contract asserted", func(t *testing.T) {
		res := commitowners.Result{Skill: "apex-help", Declared: true}
		require.Empty(t, contractSkipSuffix(&res),
			"a dispatch whose assertion ran reads exactly as it always did")
	})
	t.Run("task: contract skipped", func(t *testing.T) {
		res := commitowners.Skipped("apex-help", "--no-commit: …")
		require.Contains(t, contractSkipSuffix(&res), "NOT asserted")
	})
	t.Run("task: no contract at all", func(t *testing.T) {
		require.Empty(t, contractSkipSuffix(nil))
	})
	t.Run("story verify: classes skipped", func(t *testing.T) {
		require.Empty(t, skipSuffix(nil))
		require.Contains(t,
			skipSuffix([]story.SkippedCheck{{Check: "story.adr_unresolved", Reason: "x"}}),
			"SKIPPED")
	})
}
