package config

import (
	"encoding/json"
	"errors"
	"strconv"
)

// MCPOptions configures the inline --mcp-config JSON. Exactly one
// server is declared: `mcp-bridge`, pointing at `<APEBin> mcp-bridge`
// with APE_IPC_PORT in env. PLAN-5 / C2.
//
// The runner always passes `--strict-mcp-config` alongside the inline
// config so the spawned `claude` ignores project `.mcp.json` and user
// MCP servers — ape's bridge is the only MCP server visible to the
// session, which keeps skill behaviour deterministic across users.
type MCPOptions struct {
	// APEBin is the absolute path to the ape binary. The spawned
	// `claude` execs this as the MCP server command. Must be set;
	// BuildMCPConfig returns an error otherwise.
	APEBin string
	// IPCPort is the TCP port the bridge subprocess dials back to
	// the parent ape process. Must be in 1–65535; BuildMCPConfig
	// returns an error otherwise.
	IPCPort int
	// ExtraServers, if non-nil, is merged into mcpServers alongside
	// `mcp-bridge`. Reserved for future use (project-MCP merge per
	// PLAN-5 Scope — OUT); the C2 callers leave this nil.
	ExtraServers map[string]MCPServer
}

// MCPServer mirrors the shape Claude Code expects under
// `mcpServers.<name>`. Only the stdio-transport fields are surfaced —
// the bridge ships with stdio only.
type MCPServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// BuildMCPConfig produces the JSON blob handed to `claude --mcp-config`.
// Returns json.RawMessage so the runner can stringify it once and pass
// it as a single argv element. The blob is <1 KB — well under
// MAX_ARG_STRLEN on Linux (128 KB) and the equivalent on macOS / Windows.
func BuildMCPConfig(opts MCPOptions) (json.RawMessage, error) {
	if opts.APEBin == "" {
		return nil, errors.New("config.BuildMCPConfig: APEBin is empty")
	}
	if opts.IPCPort <= 0 || opts.IPCPort > 65535 {
		return nil, errors.New("config.BuildMCPConfig: IPCPort must be in 1..65535")
	}

	servers := map[string]MCPServer{
		"mcp-bridge": {
			Command: opts.APEBin,
			Args:    []string{"mcp-bridge"},
			Env:     map[string]string{"APE_IPC_PORT": strconv.Itoa(opts.IPCPort)},
		},
	}
	for name, srv := range opts.ExtraServers {
		if name == "mcp-bridge" {
			return nil, errors.New("config.BuildMCPConfig: ExtraServers cannot override `mcp-bridge`")
		}
		servers[name] = srv
	}

	root := map[string]any{"mcpServers": servers}
	return json.Marshal(root)
}
