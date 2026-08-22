package cost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeMsgTranscript materialises a JSONL transcript from raw lines.
func writeMsgTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	var body strings.Builder
	for _, l := range lines {
		body.WriteString(l + "\n")
	}
	require.NoError(t, os.WriteFile(path, []byte(body.String()), 0o600))
	return path
}

func assistant(text string) string {
	return `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}`
}

func TestLastAssistantText_LastTurnWins(t *testing.T) {
	t.Parallel()
	path := writeMsgTranscript(
		t,
		assistant("first"),
		`{"type":"user","message":{"content":[{"type":"text","text":"ignored"}]}}`,
		assistant("run_status: partial"),
	)
	got, ok := LastAssistantText(path)
	require.True(t, ok)
	require.Equal(t, "run_status: partial", got)
}

// A sub-agent's closing message is not the run's. Sidechain rows are the
// sub-agent turns, so they must never be mistaken for the final message.
func TestLastAssistantText_SkipsSidechainAndMeta(t *testing.T) {
	t.Parallel()
	path := writeMsgTranscript(
		t,
		assistant("the real closing message"),
		`{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"text","text":"sub-agent said this"}]}}`,
		`{"type":"assistant","isMeta":true,"message":{"content":[{"type":"text","text":"meta noise"}]}}`,
	)
	got, ok := LastAssistantText(path)
	require.True(t, ok)
	require.Equal(t, "the real closing message", got)
}

// A run often ends on a tool_use turn that carries no text; the contract
// lives in the last turn that actually said something.
func TestLastAssistantText_SkipsTextlessTurns(t *testing.T) {
	t.Parallel()
	path := writeMsgTranscript(
		t,
		assistant("run_status: complete"),
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash"}]}}`,
	)
	got, ok := LastAssistantText(path)
	require.True(t, ok)
	require.Equal(t, "run_status: complete", got)
}

func TestLastAssistantText_JoinsMultipleTextBlocks(t *testing.T) {
	t.Parallel()
	path := writeMsgTranscript(
		t,
		`{"type":"assistant","message":{"content":[`+
			`{"type":"thinking","thinking":"hidden"},`+
			`{"type":"text","text":"summary above"},`+
			`{"type":"text","text":"run_status: partial"}]}}`,
	)
	got, ok := LastAssistantText(path)
	require.True(t, ok)
	require.Equal(t, "summary above\nrun_status: partial", got)
}

// One malformed line must not invalidate the whole read — the same
// tolerance the cost scanner applies, for the same reason.
func TestLastAssistantText_SkipsMalformedLines(t *testing.T) {
	t.Parallel()
	path := writeMsgTranscript(
		t,
		`{not json at all`,
		assistant("still readable"),
		``,
	)
	got, ok := LastAssistantText(path)
	require.True(t, ok)
	require.Equal(t, "still readable", got)
}

func TestLastAssistantText_MissingOrEmpty(t *testing.T) {
	t.Parallel()
	_, ok := LastAssistantText("")
	require.False(t, ok)

	_, ok = LastAssistantText(filepath.Join(t.TempDir(), "nope.jsonl"))
	require.False(t, ok)

	// A transcript with no assistant text at all.
	path := writeMsgTranscript(t, `{"type":"user","message":{"content":[]}}`)
	_, ok = LastAssistantText(path)
	require.False(t, ok)
}
