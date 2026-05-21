package runlog

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SessionMeta is the minimal chat-session record written to session.yaml
// when an `ape chat` run ends. PLAN-5 / C6 — chats are not pipelines,
// so no PLAN-3 manifest equivalent.
type SessionMeta struct {
	ChatID    string
	StartedAt time.Time
	EndedAt   time.Time
	Model     string
	CostUSD   float64
	TokensIn  int64
	TokensOut int64
}

// WriteSessionYAML emits session.yaml at <dir>/session.yaml. Hand-rolled
// because the file is tiny and the rest of ape doesn't pull a YAML
// encoder for trivial structs.
func WriteSessionYAML(dir string, m SessionMeta) error {
	path := filepath.Join(dir, "session.yaml")
	contents := fmt.Sprintf(
		"chat_id: %s\n"+
			"started_at: %s\n"+
			"ended_at: %s\n"+
			"model: %q\n"+
			"cost_usd: %f\n"+
			"tokens_input: %d\n"+
			"tokens_output: %d\n",
		m.ChatID,
		m.StartedAt.UTC().Format(time.RFC3339),
		m.EndedAt.UTC().Format(time.RFC3339),
		m.Model,
		m.CostUSD,
		m.TokensIn,
		m.TokensOut,
	)
	return os.WriteFile(path, []byte(contents), 0o644) //nolint:gosec // user-visible runlog metadata; world-readable is intentional
}
