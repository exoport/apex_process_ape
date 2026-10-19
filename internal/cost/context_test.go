package cost

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func usageTurn(model string, input, read, creation int) string {
	return `{"type":"assistant","message":{"model":"` + model + `","usage":{"input_tokens":` + strconv.Itoa(input) +
		`,"output_tokens":7,"cache_read_input_tokens":` + strconv.Itoa(read) + `,"cache_creation_input_tokens":` + strconv.Itoa(creation) + `}}}`
}

// The newest main turn's prompt is the context: input + cache read +
// cache creation, in the raw model spelling (the suffix kept); sub-agent,
// meta and synthetic turns are not the session's.
func TestLatestContext_NewestMainTurn(t *testing.T) {
	t.Parallel()
	path := writeMsgTranscript(t,
		usageTurn("claude-sonnet-4-5", 10, 1000, 100),
		`{"type":"user","message":{"content":"next"}}`,
		usageTurn("claude-sonnet-4-5[1m]", 20, 120000, 2000),
		`{"type":"assistant","isSidechain":true,"message":{"model":"claude-haiku-4-5","usage":{"input_tokens":999999}}}`,
		`{"type":"assistant","isMeta":true,"message":{"model":"claude-haiku-4-5","usage":{"input_tokens":999999}}}`,
		`{"type":"assistant","message":{"model":"<synthetic>","usage":{"input_tokens":0}}}`,
		`not json`,
	)
	tokens, model, ok := LatestContext(path)
	require.True(t, ok)
	require.Equal(t, 122020, tokens)
	require.Equal(t, "claude-sonnet-4-5[1m]", model)
	w, _ := ContextWindow(model)
	require.Equal(t, 1000000, w, "the suffix the raw spelling keeps selects the 1M window")
}

// A transcript that writes only the 5m/1h split still counts its cache
// creation.
func TestLatestContext_CacheCreationSplit(t *testing.T) {
	t.Parallel()
	path := writeMsgTranscript(t,
		`{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":5,"cache_read_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":40}}}}`)
	tokens, _, ok := LatestContext(path)
	require.True(t, ok)
	require.Equal(t, 175, tokens)
}

func TestLatestContext_NoTurn(t *testing.T) {
	t.Parallel()
	_, _, ok := LatestContext(writeMsgTranscript(t, `{"type":"user","message":{"content":"hi"}}`))
	require.False(t, ok)
	_, _, ok = LatestContext("")
	require.False(t, ok)
	_, _, ok = LatestContext("/no/such/transcript.jsonl")
	require.False(t, ok)
}
