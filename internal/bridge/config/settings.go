package config

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// SettingsOptions configures the inline --settings JSON.
//
// Hooks are injected when InjectHooks is true OR when Mode == ModeWeb
// (back-compat for PLAN-5 callers). For ModeEval the returned blob is
// always `{}` regardless of InjectHooks — PLAN-6 invariant #1 locks
// `--eval` byte-equivalence with the eval consumer; injecting hooks
// would change the per-tool-call subprocess spawn shape and break that
// contract. PLAN-6 / Phase E (TUI parity) adds InjectHooks so TUI and
// interactive modes can opt in to hook observability without piggy-
// backing on ModeWeb. PLAN-5 / C2 + C4 baseline.
type SettingsOptions struct {
	// APEBin is the absolute path to the ape binary used in the
	// hook `command` field (e.g. "/usr/local/bin/ape notify --event PreToolUse").
	// Required when hooks are injected.
	APEBin string
	// BridgePort is the TCP port `ape notify` dials. Wired into the
	// hook env as APE_BRIDGE_PORT. Required when hooks are injected.
	BridgePort int
	// Mode controls the eval-equivalence lock: ModeEval forces an
	// empty settings blob regardless of InjectHooks. ModeWeb and
	// ModeTUI both permit hooks when InjectHooks is set; ModeWeb also
	// permits hooks for back-compat when InjectHooks is left false.
	Mode Mode
	// InjectHooks opts into the hooks block independently of Mode.
	// PLAN-6 / Phase E: TUI + interactive modes set this to true.
	InjectHooks bool
	// OutputStyle pins Claude Code's output style for the spawned
	// session. Empty means DefaultOutputStyle; InheritOutputStyle omits
	// the key entirely and lets the machine's own configuration win.
	OutputStyle string
}

// Output-style pinning.
//
// # Why ape writes this key at all
//
// Every session ape spawns is a real Claude Code session in the project
// root, and it inherits whatever output style the machine has
// configured. An output style is not cosmetic: it claims precedence over
// other communication and formatting guidance, which is exactly what the
// APEX framework depends on at the end of a run — the fenced return
// contracts a batch orchestrator parses to reconstruct status and paths,
// the guided menus and HALT prompts, and the completion summaries the
// eval asserts on. A developer who set `Concise` for their own
// conversations would silently change what every skill run emits, on
// their machine only, in a way no test on another machine would catch.
//
// A skill run is machine-consumed output, not a conversation, so the pin
// is the default rather than an opt-in.
//
// # Why the key must be written explicitly
//
// Claude Code's settings precedence is: enterprise-managed, then
// `--settings`, then project-local, then shared-project, then user. An
// OMITTED key falls through to the highest file that defines one, so
// leaving it out neutralises nothing — the key has to be present to
// override. Enterprise-managed settings still outrank `--settings`,
// which is a documented residual limit rather than something ape can
// close.
//
// Verified against Claude Code 2.1.259 rather than assumed, because the
// docs only ever describe OMITTING the key: a custom style named in
// `--settings` takes effect, the same style set in a project settings
// file is overridden by an explicit `Default` in `--settings`, and an
// unknown style name is silently ignored rather than rejected — which is
// why "it did not error" was not accepted as evidence that the value was
// honoured.
const (
	// DefaultOutputStyle is Claude Code's own standard style, and the
	// value ape pins. Capitalised exactly as the built-in names are.
	DefaultOutputStyle = "Default"
	// InheritOutputStyle is the opt-out: no key is written and the
	// machine's configured style applies. For an operator who wants a
	// style deliberately.
	InheritOutputStyle = "inherit"
)

// builtinOutputStyles maps a lower-cased style name onto the spelling
// Claude Code resolves it by. The built-ins are capitalised; a name that
// does not resolve is ignored silently, so a wrong case is a row that
// looks correct and does nothing.
//
// "default" appears twice on purpose. Claude Code's own style table is
// keyed by the literal `default` for the standard style, while the value
// ape pins is `Default`, and for THIS name the two are observationally
// identical: an unresolvable name yields no style section, which is
// exactly what the standard style yields. Both spellings therefore
// reach the same session and neither deserves a warning.
var builtinOutputStyles = map[string]string{
	"default":     DefaultOutputStyle,
	"concise":     "Concise",
	"explanatory": "Explanatory",
	"learning":    "Learning",
	"proactive":   "Proactive",
}

// BuiltinOutputStyleSpelling reports the canonical spelling of a
// built-in output style, and whether the name names a built-in at all.
// A custom style — project, plugin or user-defined — is not a built-in
// and comes back (style, false): ape cannot enumerate what a machine has
// installed, so an unrecognised name is reported as unknown rather than
// as wrong.
func BuiltinOutputStyleSpelling(style string) (canonical string, builtin bool) {
	canonical, builtin = builtinOutputStyles[strings.ToLower(strings.TrimSpace(style))]
	if !builtin {
		return style, false
	}
	// Both `default` and `Default` are accepted spellings of the
	// standard style; report the input as canonical so neither warns.
	if strings.EqualFold(style, DefaultOutputStyle) {
		return style, true
	}
	return canonical, true
}

// resolveOutputStyle maps the option onto the value written, and reports
// whether to write the key at all.
func resolveOutputStyle(style string) (value string, write bool) {
	switch style {
	case InheritOutputStyle:
		return "", false
	case "":
		return DefaultOutputStyle, true
	default:
		return style, true
	}
}

// hookSpec is the Claude Code hooks shape, one entry per event in
// the settings JSON. Reference: https://code.claude.com/docs/en/hooks.
type hookSpec struct {
	// Matcher is an optional regex against the tool name (PreToolUse /
	// PostToolUse). Empty matches everything. Unused for non-tool
	// events (UserPromptSubmit, SubagentStart, etc.).
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookCommand `json:"hooks"`
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
// Mode == ModeWeb (PLAN-5) or InjectHooks == true (PLAN-6) wires eight
// events — the six from PLAN-5 / C4 plus the two session-boundary events
// below — with Stop the only synchronous one. ModeEval always returns
// `{}` regardless of InjectHooks (PLAN-6 invariant #1), so nothing here
// can reach the eval's spawn shape. All other combinations return `{}`.
func BuildSettings(opts SettingsOptions) (json.RawMessage, error) {
	// ModeEval stays byte-empty, and that is a deliberate exception to
	// the output-style pin rather than an oversight.
	//
	// PLAN-6 invariant #1 locks `--eval` byte-equivalence with an
	// external consumer that compares the spawn shape exactly; adding a
	// key here would break a cross-repo contract to close a hazard that
	// does not reach this path. The framework's own eval harness does
	// not pass `--eval` — its captures come through the interactive path
	// below, which IS pinned — so nothing the framework consumes is left
	// unprotected by this exception.
	if opts.Mode == ModeEval {
		return json.RawMessage(`{}`), nil
	}

	root := map[string]any{}
	if style, write := resolveOutputStyle(opts.OutputStyle); write {
		root["outputStyle"] = style
	}

	// The no-hooks path still gets the pin. It used to return `{}`, which
	// is precisely where a pin written inside the hooks block would have
	// been silently missing.
	if opts.Mode != ModeWeb && !opts.InjectHooks {
		return json.Marshal(root)
	}
	if opts.APEBin == "" {
		return nil, errors.New("config.BuildSettings: APEBin is empty (required when hooks are injected)")
	}
	if opts.BridgePort <= 0 || opts.BridgePort > 65535 {
		return nil, errors.New("config.BuildSettings: BridgePort must be in 1..65535 (required when hooks are injected)")
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
		// Session-boundary observability. Recorded into
		// hook-events.jsonl and read by nothing: neither event carries a
		// field ape gates on, which is exactly why both can be async.
		// A SYNCHRONOUS hook on a session boundary is how a run wedges.
		//
		// SessionStart fires on startup / resume / clear / compact, and
		// the runner sends `/clear` between steps, so expect roughly one
		// per step rather than one per spawned process.
		"SessionStart": {{
			Hooks: []hookCommand{{Type: "command", Command: cmd("SessionStart"), Async: true}},
		}},
		"PreCompact": {{
			Hooks: []hookCommand{{Type: "command", Command: cmd("PreCompact"), Async: true}},
		}},
		// Stop is the only sync hook: ape needs the run-log flushed
		// before the loop returns so the durable record is complete.
		"Stop": {{
			Hooks: []hookCommand{{Type: "command", Command: cmd("Stop"), Async: false}},
		}},
	}

	root["hooks"] = hooks
	return json.Marshal(root)
}
