package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/diegosz/apex_process_ape/internal/bridge/ipc"
	"github.com/diegosz/apex_process_ape/internal/web/views"
)

// HookFragment is the orchestrator-facing input to the activity-feed
// renderer. It mirrors orchestrator.HookEvent but the web package
// declares it locally so internal/web stays import-free of
// orchestrator (the webRenderer adapter in apecmd bridges the two).
type HookFragment struct {
	At        time.Time
	Event     string
	SessionID string
	AgentID   string
	Step      string
	// Payload is the raw hook envelope as forwarded by `ape notify`.
	// Shape varies by event; the renderer extracts tool name + a
	// short summary appropriate to the event kind.
	Payload []byte
	// ProjectRoot, when non-empty, is stripped from any
	// file-path-shaped summary so the feed shows relative paths
	// instead of absolute ones.
	ProjectRoot string
}

// RenderHookFragment turns a HookFragment into one row for the
// #hooks scrolling feed. PLAN-5 / C8.
func RenderHookFragment(t *template.Template, hf HookFragment) string {
	tool, summary := parseHookForFeed(hf.Event, hf.Payload)
	summary = relativisePath(summary, hf.ProjectRoot)
	line := HookLine{
		TS:       hf.At.Local().Format("15:04:05"), //nolint:gosmopolitan // intentional: hook line timestamps shown in the user's local clock
		Event:    hf.Event,
		Tool:     tool,
		Summary:  summary,
		CSSClass: cssClassForEvent(hf.Event),
	}
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "hook-line", line); err != nil {
		return ""
	}
	return b.String()
}

// relativisePath rewrites an absolute-path-shaped summary so it shows
// the project-relative form. Leaves anything that does not start with
// the project root alone (commands, prompts, etc. may legitimately
// reference paths outside the project). PLAN-5 follow-up after first
// user-facing pass: full /home/user/.../greeter/foo paths drowned out
// the rest of the feed.
func relativisePath(summary, projectRoot string) string {
	if projectRoot == "" || summary == "" {
		return summary
	}
	prefix := projectRoot
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	// Replace every occurrence — Bash commands and Grep summaries
	// can mention the same path multiple times. ReplaceAll keeps
	// the order intact and is cheap.
	return strings.ReplaceAll(summary, prefix, "")
}

// parseHookForFeed extracts (tool, summary) from a hook payload by
// event kind. PreToolUse/PostToolUse get tool_name + tool-specific
// summary; UserPromptSubmit surfaces the prompt; Subagent* surface
// the agent id.
func parseHookForFeed(event string, payload []byte) (tool, summary string) {
	if len(payload) == 0 {
		return "", ""
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", ""
	}
	switch event {
	case ipc.HookPreToolUse, ipc.HookPostToolUse:
		tool = stringField(p, "tool_name")
		summary = summarizeToolCall(tool, p, event == ipc.HookPostToolUse)
	case ipc.HookUserPromptSubmit:
		summary = views.TruncateMid(stringField(p, "prompt"), 100)
	case "SubagentStart":
		summary = fmt.Sprintf("%s (%s)", stringField(p, "agent_type"), shortID(stringField(p, "agent_id")))
	case "SubagentStop":
		summary = "(" + shortID(stringField(p, "agent_id")) + ")"
	case "Stop":
		summary = "end of turn"
	}
	return tool, summary
}

func summarizeToolCall(tool string, p map[string]json.RawMessage, post bool) string {
	input := map[string]json.RawMessage{}
	if raw, ok := p["tool_input"]; ok {
		_ = json.Unmarshal(raw, &input)
	}
	core := ""
	switch tool {
	case "Read", "Edit", "Write", "NotebookEdit":
		core = stringField(input, "file_path")
	case "Bash":
		core = stringField(input, "command")
	case "Glob":
		core = stringField(input, "pattern")
	case "Grep":
		pattern := stringField(input, "pattern")
		path := stringField(input, "path")
		if path != "" {
			core = pattern + " in " + path
		} else {
			core = pattern
		}
	case "Task":
		typ := stringField(input, "subagent_type")
		desc := stringField(input, "description")
		core = typ
		if desc != "" {
			core = typ + ": " + desc
		}
	case "TodoWrite":
		var todos []json.RawMessage
		if raw, ok := input["todos"]; ok {
			_ = json.Unmarshal(raw, &todos)
		}
		core = fmt.Sprintf("%d todo(s)", len(todos))
	case "WebFetch":
		core = stringField(input, "url")
	default:
		// Fallback: first non-empty string-ish field.
		for _, k := range []string{"path", "file_path", "command", "pattern", "url", "query"} {
			if v := stringField(input, k); v != "" {
				core = v
				break
			}
		}
	}
	core = views.TruncateMid(core, 120)
	if post {
		if dur := numericField(p, "duration_ms"); dur > 0 {
			return fmt.Sprintf("%s  (%dms)", core, dur)
		}
	}
	return core
}

func stringField(m map[string]json.RawMessage, key string) string {
	raw, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func numericField(m map[string]json.RawMessage, key string) int64 {
	raw, ok := m[key]
	if !ok {
		return 0
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0
	}
	return n
}

func shortID(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}

func cssClassForEvent(event string) string {
	switch event {
	case ipc.HookPreToolUse, ipc.HookPostToolUse:
		return "tool"
	case ipc.HookUserPromptSubmit:
		return "prompt"
	case "SubagentStart", "SubagentStop":
		return "agent"
	case "Stop":
		return "endturn"
	}
	return ""
}
