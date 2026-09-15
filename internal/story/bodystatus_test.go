package story

import (
	"testing"

	"github.com/exoport/apex_process_ape/internal/mdscan"
	"github.com/stretchr/testify/require"
)

// Every spelling the pattern's comment claims, each asserted: the bold
// form `**Status:** x` was claimed and never matched.
func TestBodyStatus_Spellings(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"Status: in-progress":           "in-progress",
		"**Status:** in-progress":       "in-progress",
		"**Status**: in-progress":       "in-progress",
		"Status: **in-progress**":       "in-progress",
		"  status:   Review  ":          "review",
		"# Title\n\nStatus: done\n":     "done",
		"Status: done\nStatus: backlog": "done",
		"No status here":                "",
		"The status: is unclear, maybe": "",
	} {
		require.Equal(t, want, BodyStatus(in), "%q", in)
	}
}

// Read from prose: a fenced example's `Status:` is not the story's own.
func TestBodyStatus_IgnoresAFencedExample(t *testing.T) {
	t.Parallel()
	text := "# Story\n\n```md\nStatus: done\n```\n\nStatus: in-progress\n"
	require.Equal(t, "in-progress", BodyStatus(mdscan.StripFences(text)))
	require.Empty(t, BodyStatus(mdscan.StripFences("```md\nStatus: done\n```\n")))
}
