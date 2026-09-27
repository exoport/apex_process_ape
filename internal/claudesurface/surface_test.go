package claudesurface

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiff(t *testing.T) {
	t.Parallel()
	added, removed := Diff([]string{"Bash", "Read", "TaskOutput"}, []string{"Bash", "Read", "Monitor"})
	require.Equal(t, []string{"Monitor"}, added)
	require.Equal(t, []string{"TaskOutput"}, removed)

	added, removed = Diff([]string{"a"}, []string{"a"})
	require.Empty(t, added)
	require.Empty(t, removed)
}

// A name straddling a window boundary must be recorded whole, once — never
// as a truncated prefix. The window is small enough that boundaries land
// inside names.
func TestEnvVars_WindowBoundaries(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("x", 300) + "a.CLAUDE_CODE_FORK_SUBAGENT===!1" + strings.Repeat("y", 290) +
		`,"CLAUDE_AUTO_BACKGROUND_TASKS",` + strings.Repeat("z", 5) + " process.env.CLAUDE_CODE_EFFORT_LEVEL" + strings.Repeat("\x00", 10) +
		"NOT_CLAUDE_X ANTHROPIC_API_KEY claude_lower"
	want := []string{"CLAUDE_AUTO_BACKGROUND_TASKS", "CLAUDE_CODE_EFFORT_LEVEL", "CLAUDE_CODE_FORK_SUBAGENT"}
	for _, chunk := range []int{7, 64, 300, 1 << 20} {
		got, err := envVars(bytes.NewReader([]byte(body)), chunk)
		require.NoError(t, err)
		require.Equal(t, want, got, "chunk %d", chunk)
	}
}

func TestToolsFromStreamJSON(t *testing.T) {
	t.Parallel()
	stream := `{"type":"system","subtype":"hook_started"}
{"type":"system","subtype":"init","claude_code_version":"2.1.283","tools":["Read","Bash","mcp__srv__x","Task","Bash"]}
{"type":"assistant"}
`
	tools, version, err := ToolsFromStreamJSON(strings.NewReader(stream))
	require.NoError(t, err)
	require.Equal(t, "2.1.283", version)
	require.Equal(t, []string{"Bash", "Read", "Task"}, tools, "sorted, deduplicated, MCP tools dropped")

	_, _, err = ToolsFromStreamJSON(strings.NewReader(`{"type":"assistant"}`))
	require.Error(t, err)
}

// The two entries that would have flagged this incident, verbatim from
// Claude Code's CHANGELOG, must match — and an unrelated one must not.
const changelogFixture = `# Changelog

## 2.1.283

- Fixed the prompt box flickering on resize

## 2.1.277

- Removed the deprecated TaskOutput tool; Claude reads a background task's output file with Read instead
- Improved startup time

## 2.1.232

- Subagent forking is now on by default: a ` + "`subagent_type: \"fork\"`" + ` subagent inherits the full conversation and prompt cache, and non-teammate agent spawns in interactive sessions now run in the background by default

## 2.1.200

- Fixed a typo in /help
`

func TestParseChangelogAndRelevantSince(t *testing.T) {
	t.Parallel()
	sections := ParseChangelog([]byte(changelogFixture))
	require.Len(t, sections, 4)
	require.Equal(t, "2.1.283", sections[0].Version)
	require.Len(t, sections[1].Entries, 2)

	got := RelevantSince(sections, "2.1.200", "2.1.283")
	require.Len(t, got, 2, "%v", got)
	require.Equal(t, "2.1.277", got[0].Version, "newest first")
	require.Contains(t, got[0].Text, "TaskOutput")
	require.Equal(t, "2.1.232", got[1].Version)

	require.Empty(t, RelevantSince(sections, "2.1.283", "2.1.283"), "nothing newer than the review")
	require.Len(t, RelevantSince(sections, "2.1.200", "2.1.276"), 1, "upto bounds the installed version")
	require.Len(t, RelevantSince(sections, "", "2.1.283"), 2, "no review recorded: everything counts")
}

func TestCompareAndCanonical(t *testing.T) {
	t.Parallel()
	require.Negative(t, Compare("2.1.99", "2.1.100"), "numeric, not lexical")
	require.Zero(t, Compare("2.1.283 (Claude Code)", "v2.1.283"))
	require.Equal(t, "2.1.283", Canonical("2.1.283 (Claude Code)"))
}

func TestBaselineRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "b.json")
	require.NoError(t, (&Baseline{ReviewedThrough: "2.1.283", Tools: []string{"Read", "Bash", "Read"}, EnvVars: []string{"B", "A"}}).Save(path))
	b, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, []string{"Bash", "Read"}, b.Tools)
	require.Equal(t, []string{"A", "B"}, b.EnvVars)
}
