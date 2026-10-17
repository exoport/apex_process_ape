package story

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// --- the derived section set ------------------------------------------

// --- File List markers -------------------------------------------------

// --- placeholder residue ----------------------------------------------

// --- compliance tables -------------------------------------------------

// --- GCC line form -----------------------------------------------------

// --- the exit contract -------------------------------------------------

// --- fenced code blocks are examples, not content ----------------------

func TestStripFences_Mechanics(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"tilde fence":                  {"a\n~~~\nhidden\n~~~\nb", "a\nb"},
		"info string":                  {"a\n```go\nhidden\n```\nb", "a\nb"},
		"indented up to three":         {"a\n   ```\nhidden\n   ```\nb", "a\nb"},
		"indented four is not a fence": {"a\n    ```\nb", "a\n    ```\nb"},
		"longer inner run stays open": {
			"a\n````\n```\nstill hidden\n````\nb", "a\nb",
		},
		"tilde does not close a backtick fence": {
			"a\n```\nhidden\n~~~\nalso hidden\n```\nb", "a\nb",
		},
		"unclosed fence runs to the end": {"a\n```\nhidden", "a"},
		"inline code is not a fence":     {"a `x` b", "a `x` b"},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, stripFences(tc.in))
		})
	}
}

// --- story.requirement_ids_missing (advisory, opt-in) ------------------

// TestRequirementIDs_MigrationOrdering is the interaction the framework
// flagged: v0.16.0's own migration entry is what clears this class, so
// the runner that reports the work list and the migration that empties
// it meet in the first release shipping both. Asserted here rather than
// discovered in the gating eval.
func TestRequirementIDs_MigrationOrdering(t *testing.T) {
	f := newFixture(t, "")
	f.story("1-1_thing.md", "story_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\n")

	// Before the backfill: silent by default, work list when asked.
	quiet, err := VerifyCorpus(f.cfg)
	require.NoError(t, err)
	require.Empty(t, quiet.Findings, "a mid-migration project must not red a gate")

	work, err := VerifyCorpusWith(f.cfg, VerifyOptions{IncludeAdvisory: true})
	require.NoError(t, err)
	require.Len(t, work.Findings, 1)
	require.Equal(t, CheckRequirementIDsMissing, work.Findings[0].Check)

	// After the backfill writes the field, the work list is empty.
	f.story("1-1_thing.md",
		"story_id: 1-1\nepic: 1\nstatus: done\nrequirement_ids: [FR-1]\n"+
			"output_document: x.md\n")
	done, err := VerifyCorpusWith(f.cfg, VerifyOptions{IncludeAdvisory: true})
	require.NoError(t, err)
	require.Empty(t, done.Findings, "the migration clears its own work list")
}
