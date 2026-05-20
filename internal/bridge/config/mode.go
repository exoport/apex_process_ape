// Package config builds the inline JSON blobs that `ape pipeline` and
// `ape chat` hand to the spawned `claude` process via `--mcp-config` and
// `--settings`. Nothing here writes to disk — the runner stringifies
// the json.RawMessage values from BuildMCPConfig / BuildSettings and
// passes them as argv. See PLAN-5 / C2.
package config

// Mode identifies which UX surface the pipeline / chat is running
// against. The web bridge wires hooks; the TUI and eval modes do not
// (zero overhead in non-web modes per PLAN-5 / C4).
type Mode int

const (
	// ModeEval is the locked byte-equivalent stdout path (opt-in via
	// --eval, renamed from --print on 2026-05-20). No bridge, no
	// hooks, no per-tool-call subprocess spawn.
	ModeEval Mode = iota
	// ModeTUI is the Bubble Tea TUI (today's default; will become
	// opt-in via --tui once C1's default flip lands).
	ModeTUI
	// ModeWeb is the bridged web UI. Hooks fire; the bridge MCP
	// server is wired; `ape notify` is reachable on APE_BRIDGE_PORT.
	ModeWeb
)

// String returns the human-readable mode name. Used in error messages
// and the bridge-calls.jsonl `mode` field.
func (m Mode) String() string {
	switch m {
	case ModeEval:
		return "eval"
	case ModeTUI:
		return "tui"
	case ModeWeb:
		return "web"
	}
	return "unknown"
}
