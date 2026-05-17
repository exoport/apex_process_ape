package config

import (
	"encoding/json"
	"errors"
	"strconv"
)

// SettingsOptions configures the inline --settings JSON. Hooks are
// injected only when Mode == ModeWeb; for ModeTUI / ModePrint the
// returned blob is `{}` so no `ape notify` subprocess is spawned per
// tool call (zero overhead in non-web modes). PLAN-5 / C2 + C4.
type SettingsOptions struct {
	// APEBin is the absolute path to the ape binary used in the
	// hook `command` field (e.g. "/usr/local/bin/ape notify --event PreToolUse").
	// Required when Mode == ModeWeb.
	APEBin string
	// BridgePort is the TCP port `ape notify` dials. Wired into the
	// hook env as APE_BRIDGE_PORT. Required when Mode == ModeWeb.
	BridgePort int
	// Mode controls hook injection. ModeWeb → hooks block present;
	// ModeTUI / ModePrint → returned blob is `{}`.
	Mode Mode
}

// hookSpec is the Claude Code hooks shape, one entry per event in
// the settings JSON. Reference: https://code.claude.com/docs/en/hooks.
type hookSpec struct {
	// Matcher is an optional regex against the tool name (PreToolUse /
	// PostToolUse). Empty matches everything. Unused for non-tool
	// events (UserPromptSubmit, SubagentStart, etc.).
	Matcher string         `json:"matcher,omitempty"`
	Hooks   []hookCommand  `json:"hooks"`
}

type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	// Async lets the tool loop proceed without waiting for the hook
	// to return. Stop is the only event where ape needs the hook to
	// complete (run-log flush) so it stays sync.
	Async bool `json:"async"`
}

// BuildSettings produces the JSON blob handed to `claude --settings`.
// Mode == ModeWeb wires the six events listed in PLAN-5 / C4 (five
// async, Stop sync); other modes return `{}`.
func BuildSettings(opts SettingsOptions) (json.RawMessage, error) {
	if opts.Mode != ModeWeb {
		return json.RawMessage(`{}`), nil
	}
	if opts.APEBin == "" {
		return nil, errors.New("config.BuildSettings: APEBin is empty (required for ModeWeb)")
	}
	if opts.BridgePort <= 0 || opts.BridgePort > 65535 {
		return nil, errors.New("config.BuildSettings: BridgePort must be in 1..65535 (required for ModeWeb)")
	}

	// Hook command is identical except for the --event value; ape
	// notify reads APE_BRIDGE_PORT from env, so we set it once at
	// the top-level hook scope. (Claude Code currently flattens env
	// from the parent claude process; the env field on the hook
	// itself is not part of the documented schema — we wire the
	// port via the bridge subprocess invocation instead, see below.)
	bridgePortStr := strconv.Itoa(opts.BridgePort)
	cmd := func(event string) string {
		// `env APE_BRIDGE_PORT=<port> <ape-bin> notify --event <event>`
		// guarantees the port reaches the subprocess regardless of
		// how Claude Code propagates env. `env(1)` is POSIX; on
		// Windows the runner converts the matcher at execution
		// time, which is fine because hooks don't fire on the
		// platforms where env(1) is missing.
		return "env APE_BRIDGE_PORT=" + bridgePortStr + " " + opts.APEBin + " notify --event " + event
	}

	hooks := map[string][]hookSpec{
		// Tool-call observability — matcher "" hits every tool.
		"PreToolUse": {{
			Hooks: []hookCommand{{Type: "command", Command: cmd("PreToolUse"), Async: true}},
		}},
		"PostToolUse": {{
			Hooks: []hookCommand{{Type: "command", Command: cmd("PostToolUse"), Async: true}},
		}},
		"UserPromptSubmit": {{
			Hooks: []hookCommand{{Type: "command", Command: cmd("UserPromptSubmit"), Async: true}},
		}},
		"SubagentStart": {{
			Hooks: []hookCommand{{Type: "command", Command: cmd("SubagentStart"), Async: true}},
		}},
		"SubagentStop": {{
			Hooks: []hookCommand{{Type: "command", Command: cmd("SubagentStop"), Async: true}},
		}},
		// Stop is the only sync hook: ape needs the run-log flushed
		// before the loop returns so the durable record is complete.
		"Stop": {{
			Hooks: []hookCommand{{Type: "command", Command: cmd("Stop"), Async: false}},
		}},
	}

	root := map[string]any{"hooks": hooks}
	return json.Marshal(root)
}
