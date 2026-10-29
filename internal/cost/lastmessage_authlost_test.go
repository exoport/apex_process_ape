package cost

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// D603: what a session that lost its login writes, captured on Claude Code
// 2.1.310 and 2.1.312 in a scratch config dir (D637 item 1b): an invalid
// token, an expired login whose refresh fails, and no credential at all.
// Each ends with Claude Code's own message, kind authentication_failed.
func TestLastAssistantMessage_AuthLostCaptures(t *testing.T) {
	for file, text := range map[string]string{
		"auth-lost-312-token.jsonl":   "Please run /login · API Error: 401 OAuth access token is invalid.",
		"auth-lost-312-expired.jsonl": "Login expired · Please run /login",
		"auth-lost-312-none.jsonl":    "Not logged in · Please run /login",
		"auth-lost-310-token.jsonl":   "Please run /login · API Error: 401 OAuth access token is invalid.",
		"auth-lost-310-expired.jsonl": "Login expired · Please run /login",
		"auth-lost-310-none.jsonl":    "Not logged in · Please run /login",
	} {
		m, ok := LastAssistantMessage(filepath.Join("testdata", file))
		require.True(t, ok, file)
		require.Equal(t, AssistantMessage{Text: text, APIErrorKind: "authentication_failed"}, m, file)
	}
	m, ok := LastAssistantMessage(filepath.Join("testdata", "session-golden.jsonl"))
	require.True(t, ok)
	require.Empty(t, m.APIErrorKind, "the model's own message has no API error kind")
}
