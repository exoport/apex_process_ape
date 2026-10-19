package cost

import (
	"bufio"
	"encoding/json"
	"os"
)

// contextLine is the minimal shape LatestContext needs from a transcript
// row: the usage and the raw model of an assistant turn. Separate from
// AssistantLine for the reason finalMessageLine is: it decodes the one
// summed cache-creation field the cost scanner has no use for.
type contextLine struct {
	Type        string `json:"type"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Model string `json:"model"`
		Usage struct {
			UsageBlock

			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// LatestContext is the context a session holds now, as the newest main
// turn of its transcript sent it: input + cache read + cache creation
// tokens (the prompt it sent, which is the conversation so far), and that
// turn's model in the RAW spelling the transcript recorded — `[1m]` kept,
// so ContextWindow can tell a 1M variant from its 200K base (the loop
// relay's fill is these tokens over that window; OI-11).
//
// Skipped rows: non-assistant, isMeta, isSidechain (a sub-agent's turn is
// not the session's context) and a synthetic model's (`<synthetic>`, a
// locally generated turn with no usage). The cache-creation count is the
// larger of the summed field and the 5m/1h split, so a transcript that
// writes only one of them still counts it.
//
// ok is false when the file cannot be read or holds no such turn.
// Malformed lines are skipped, never fatal.
func LatestContext(path string) (tokens int, model string, ok bool) {
	if path == "" {
		return 0, "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, "", false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var cl contextLine
		if err := json.Unmarshal(sc.Bytes(), &cl); err != nil {
			continue
		}
		if cl.Type != AssistantRowType || cl.IsMeta || cl.IsSidechain || cl.Message.Model == "" || IsSyntheticModel(cl.Message.Model) {
			continue
		}
		u := cl.Message.Usage
		creation := max(u.CacheCreationInputTokens, u.CacheCreation.Ephemeral5m+u.CacheCreation.Ephemeral1h)
		tokens, model, ok = u.InputTokens+u.CacheRead+creation, cl.Message.Model, true
	}
	// A truncated tail still leaves the newest turn read so far.
	return tokens, model, ok
}
