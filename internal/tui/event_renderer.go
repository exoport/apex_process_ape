package tui

import (
	"encoding/json"
	"os"
	"strings"
)

// Bubble Tea event renderer (PLAN-1 / I4b).
//
// The pipeline runner invokes Observer.OnStepLine for every
// newline-delimited chunk the spawned claude subprocess writes to
// stdout. Each chunk is one event from `claude --output-format
// stream-json`. This file parses those events and turns them into
// one human-friendly display line.
//
// Event shapes (excerpted from real claude CLI traces):
//
//	{"type":"system","subtype":"init",...}
//	{"type":"assistant","message":{"content":[
//	    {"type":"text","text":"..."},
//	    {"type":"tool_use","name":"Read","input":{"file_path":"..."}},
//	    ...
//	]}}
//	{"type":"user","message":{"content":[
//	    {"type":"tool_result","tool_use_id":"...","content":"...","is_error":false},
//	    ...
//	]}}
//	{"type":"result","subtype":"success","is_error":false,"result":"...","duration_ms":...}
//
// We tolerate schema drift: anything that doesn't parse as expected
// is forwarded as a raw line with kind=EventUnknown so power users
// can still see what's happening (and we don't lose information).

// EventKind classifies one rendered event for color/style mapping
// in the TUI layer. The caller (pipelineModel) maps kind to a
// lipgloss.Style; the renderer itself stays presentation-agnostic
// so tests don't depend on lipgloss internals.
type EventKind int

const (
	// EventSuppressed indicates this line should not be displayed.
	// Returned for noise (successful Read/Edit/Write tool_results,
	// system pings, etc). Callers must skip such entries.
	EventSuppressed EventKind = iota
	// EventText is an assistant text block (the model's prose
	// thinking out loud). Glyph "✎", default color.
	EventText
	// EventTool is a tool_use call (Read, Edit, Bash, ...). Glyph
	// "🔧", cyan.
	EventTool
	// EventToolResult is a non-trivial tool_result with success
	// content worth surfacing (e.g. Bash output, WebFetch summary).
	// Glyph "↳", dim.
	EventToolResult
	// EventToolError is a tool_result with is_error=true. Glyph "↳",
	// red.
	EventToolError
	// EventSuccess is a top-level "result" event with success status.
	// Glyph "✓", green.
	EventSuccess
	// EventFailure is a top-level "result" event with error status.
	// Glyph "✗", red.
	EventFailure
	// EventSystem covers system messages (init, ping, etc) that we
	// surface as low-importance background info. Glyph "·", dim.
	EventSystem
	// EventUnknown is the schema-drift fallback: the line parsed as
	// JSON but didn't match any known shape, OR didn't parse at all
	// (and we forward it raw). Glyph "?", dim.
	EventUnknown
)

// RenderedEvent is one display row produced from one stream-json line.
// Callers concatenate Glyph + " " + Body and apply the style mapped
// from Kind. Raw is the original NDJSON line — populated by
// RenderEventWithRoot so PLAN-2 / F3's raw / both render styles can
// surface it without re-parsing.
type RenderedEvent struct {
	Kind  EventKind
	Glyph string
	Body  string
	Raw   string
}

// IsDisplayable reports whether the event should be rendered at all.
// Callers typically: r := RenderEvent(line); if !r.IsDisplayable() { continue }
func (r RenderedEvent) IsDisplayable() bool {
	return r.Kind != EventSuppressed
}

// Maximum lengths we let through into a one-line summary. These are
// generous enough to be informative without ever spilling to a second
// row in the TUI's live panel.
const (
	maxTextLen        = 80 // assistant text body — first line, truncated
	maxBashLen        = 60 // Bash command — head only
	maxToolResultLen  = 80 // tool_result success summary head
	maxToolErrorLen   = 60 // tool_result error head (kept tighter; reads as a warning)
	maxTaskDescLen    = 40 // Task subagent description head
	maxWebSearchQuery = 40 // WebSearch query head
)

// RenderEvent parses one stream-json line and returns its display
// representation. Never panics on malformed input: any failure to
// parse falls through to EventUnknown with the raw line in Body.
//
// Equivalent to RenderEventWithRoot(line, "") — paths in tool events
// render absolute. Callers that know their project root should prefer
// the with-root variant so PLAN-2 / F6 strips the prefix and keeps
// the informative tail of long paths visible.
func RenderEvent(line string) RenderedEvent {
	return RenderEventWithRoot(line, "")
}

// RenderEventWithRoot is RenderEvent with a project-root path prefix
// that path-shaped tool arguments are made relative to (PLAN-2 / F6).
// When projectRoot is empty, behavior is identical to RenderEvent —
// no relativization is attempted. Tokens that don't share the prefix
// (system paths, $HOME-relative, framework files) render as-is.
func RenderEventWithRoot(line, projectRoot string) RenderedEvent {
	line = strings.TrimSpace(line)
	if line == "" {
		return RenderedEvent{Kind: EventSuppressed}
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return RenderedEvent{Kind: EventUnknown, Glyph: "?", Body: line, Raw: line}
	}
	var out RenderedEvent
	switch readString(ev, "type") {
	case "system":
		out = renderSystemEvent(ev)
	case "assistant":
		out = renderAssistantEvent(ev, projectRoot)
	case "user":
		out = renderUserEvent(ev)
	case "result":
		out = renderResultEvent(ev)
	default:
		// Unknown top-level type; show the line raw rather than swallow.
		out = RenderedEvent{Kind: EventUnknown, Glyph: "?", Body: line}
	}
	if out.Kind != EventSuppressed {
		out.Raw = line
	}
	return out
}

// renderSystemEvent surfaces system events as a low-importance row.
// Most "subtype" values are harmless pings/inits; we show them dimly
// so they're available without dominating the view.
func renderSystemEvent(ev map[string]any) RenderedEvent {
	sub := readString(ev, "subtype")
	if sub == "" {
		return RenderedEvent{Kind: EventSuppressed}
	}
	if sub == "init" {
		return RenderedEvent{Kind: EventSystem, Glyph: "·", Body: "session start"}
	}
	return RenderedEvent{Kind: EventSystem, Glyph: "·", Body: sub}
}

// renderAssistantEvent walks the message.content array. Each content
// block becomes its own logical row. We collapse all blocks of the
// same event into ONE rendered event for display order to remain
// stable; callers that want fine-grained per-block events should
// iterate the array themselves. In practice, an assistant message
// usually has one text block + zero or more tool_use blocks; we
// surface the FIRST non-trivial block to keep the live panel
// uncluttered.
func renderAssistantEvent(ev map[string]any, projectRoot string) RenderedEvent {
	blocks := readContentBlocks(ev)
	if len(blocks) == 0 {
		return RenderedEvent{Kind: EventSuppressed}
	}
	for _, b := range blocks {
		btype := readString(b, "type")
		switch btype {
		case "text":
			text := readString(b, "text")
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			return RenderedEvent{Kind: EventText, Glyph: "✎", Body: firstLineTruncated(text, maxTextLen)}
		case "tool_use":
			return renderToolUse(b, projectRoot)
		case "thinking":
			// Show abbreviated thinking blocks as text (they're
			// model reasoning, not user-facing output). Truncate
			// aggressively.
			text := readString(b, "thinking")
			if text == "" {
				continue
			}
			return RenderedEvent{Kind: EventText, Glyph: "✎", Body: "(thinking) " + firstLineTruncated(text, maxTextLen-len("(thinking) "))}
		}
	}
	return RenderedEvent{Kind: EventSuppressed}
}

// renderToolUse maps tool_use content blocks to "🔧 <Name> <args>".
// Per-tool short summaries make the running output readable; unknown
// tools fall through to a generic "🔧 <Name>" row with no args.
// projectRoot, when non-empty, is stripped from path-shaped arguments
// (PLAN-2 / F6) so the displayed path keeps its informative tail.
func renderToolUse(b map[string]any, projectRoot string) RenderedEvent {
	name := readString(b, "name")
	if name == "" {
		return RenderedEvent{Kind: EventTool, Glyph: "🔧", Body: "(unknown tool)"}
	}
	input, _ := b["input"].(map[string]any)
	switch name {
	case "Read":
		return RenderedEvent{Kind: EventTool, Glyph: "🔧", Body: "Read " + relativizePath(projectRoot, readString(input, "file_path"))}
	case "Edit":
		return RenderedEvent{Kind: EventTool, Glyph: "🔧", Body: "Edit " + relativizePath(projectRoot, readString(input, "file_path"))}
	case "Write":
		return RenderedEvent{Kind: EventTool, Glyph: "🔧", Body: "Write " + relativizePath(projectRoot, readString(input, "file_path"))}
	case "Bash":
		cmd := readString(input, "command")
		return RenderedEvent{Kind: EventTool, Glyph: "🔧", Body: "Bash " + truncate(cmd, maxBashLen)}
	case "Grep":
		pat := readString(input, "pattern")
		path := relativizePath(projectRoot, readString(input, "path"))
		body := "Grep \"" + pat + "\""
		if path != "" {
			body += " " + path
		}
		return RenderedEvent{Kind: EventTool, Glyph: "🔧", Body: body}
	case "Glob":
		body := "Glob \"" + readString(input, "pattern") + "\""
		if path := relativizePath(projectRoot, readString(input, "path")); path != "" {
			body += " " + path
		}
		return RenderedEvent{Kind: EventTool, Glyph: "🔧", Body: body}
	case "Task":
		sub := readString(input, "subagent_type")
		if sub == "" {
			sub = "(no subagent)"
		}
		desc := readString(input, "description")
		body := "Task " + sub
		if desc != "" {
			body += " \"" + truncate(desc, maxTaskDescLen) + "\""
		}
		return RenderedEvent{Kind: EventTool, Glyph: "🔧", Body: body}
	case "WebFetch":
		url := readString(input, "url")
		return RenderedEvent{Kind: EventTool, Glyph: "🔧", Body: "WebFetch " + hostOf(url)}
	case "WebSearch":
		return RenderedEvent{Kind: EventTool, Glyph: "🔧", Body: "WebSearch \"" + truncate(readString(input, "query"), maxWebSearchQuery) + "\""}
	}
	return RenderedEvent{Kind: EventTool, Glyph: "🔧", Body: name}
}

// renderUserEvent typically surfaces tool_result blocks. We suppress
// successful Read/Edit/Write results (noise) and surface errors and
// non-trivial successes.
func renderUserEvent(ev map[string]any) RenderedEvent {
	blocks := readContentBlocks(ev)
	for _, b := range blocks {
		if readString(b, "type") != "tool_result" {
			continue
		}
		isError, _ := b["is_error"].(bool)
		content := readToolResultContent(b)
		content = strings.TrimSpace(content)
		if isError {
			return RenderedEvent{Kind: EventToolError, Glyph: "↳", Body: "⚠ " + firstLineTruncated(content, maxToolErrorLen)}
		}
		// Suppress trivial success results — they're implicit when
		// the corresponding tool_use already showed.
		if content == "" || isTrivialSuccess(content) {
			continue
		}
		return RenderedEvent{Kind: EventToolResult, Glyph: "↳", Body: firstLineTruncated(content, maxToolResultLen)}
	}
	return RenderedEvent{Kind: EventSuppressed}
}

// isTrivialSuccess reports whether a tool_result's content is a
// success-with-no-news payload that we should suppress to keep the
// stream readable. Real bash output, fetched HTML, etc. are NOT
// trivial; one-liners like "File created successfully" are.
func isTrivialSuccess(content string) bool {
	prefixes := []string{
		"File created successfully",
		"The file ",
		"Applied ",
		"<system-reminder>", // hide system reminders from the stream
	}
	for _, p := range prefixes {
		if strings.HasPrefix(content, p) {
			return true
		}
	}
	return false
}

// renderResultEvent handles the top-level skill-completion message.
// Maps to a single ✓/✗ row whose body summarizes the outcome.
func renderResultEvent(ev map[string]any) RenderedEvent {
	isError, _ := ev["is_error"].(bool)
	sub := readString(ev, "subtype")
	if isError || sub == "error" || sub == "error_max_turns" || sub == "error_during_execution" {
		body := "skill failed"
		if msg := readString(ev, "result"); msg != "" {
			body = "skill failed: " + firstLineTruncated(msg, maxToolErrorLen)
		} else if sub != "" {
			body = "skill failed (" + sub + ")"
		}
		return RenderedEvent{Kind: EventFailure, Glyph: "✗", Body: body}
	}
	// Success: include cost/turns if present.
	body := "skill complete"
	if turns, ok := ev["num_turns"].(float64); ok && turns > 0 {
		body += " (" + strings.TrimRight(strings.TrimRight(formatFloat(turns), "0"), ".") + " turns)"
	}
	return RenderedEvent{Kind: EventSuccess, Glyph: "✓", Body: body}
}

// readContentBlocks reaches into message.content (the canonical
// location for Anthropic-format events) and returns the slice as
// []map[string]any. Returns nil for malformed shapes.
func readContentBlocks(ev map[string]any) []map[string]any {
	msg, _ := ev["message"].(map[string]any)
	raw, _ := msg["content"].([]any)
	if len(raw) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, b := range raw {
		if m, ok := b.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// readToolResultContent extracts the text of a tool_result block.
// Anthropic sometimes nests the content as a string and sometimes as
// an array of {type:"text", text:"..."} blocks; handle both.
func readToolResultContent(b map[string]any) string {
	switch c := b["content"].(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, item := range c {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if t := readString(m, "text"); t != "" {
				if sb.Len() > 0 {
					sb.WriteByte('\n')
				}
				sb.WriteString(t)
			}
		}
		return sb.String()
	}
	return ""
}

// readString is a typed helper around map[string]any indexing for
// fields we expect to be strings. Returns "" on absence or type
// mismatch — the renderer treats both the same way.
func readString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// truncate trims s to at most n bytes, appending "…" when shortened.
// Note: byte-counted, not rune-counted; one stray multi-byte rune in
// the trailing position would render as a replacement glyph, which
// is acceptable for live progress output.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// firstLineTruncated returns the first non-empty line of s, then
// truncates the result to n bytes. Lets multi-paragraph assistant
// text collapse to a single readable display row.
func firstLineTruncated(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimRight(s[:i], "\r ")
	}
	return truncate(s, n)
}

// relativizePath strips projectRoot from raw when raw is an absolute
// path that sits inside the project tree. PLAN-2 / F6: tool-event
// lines (Read / Edit / Write / Grep path) compete with truncation
// limits; absolute /tmp/sandbox/... prefixes in temporary fixture
// directories were eating the informative suffix. Conservative
// detection: only strip when raw is an absolute path that shares the
// projectRoot prefix at a path boundary — system paths, $HOME-relative
// paths, framework-source paths, and any non-absolute string pass
// through untouched.
func relativizePath(projectRoot, raw string) string {
	if projectRoot == "" || raw == "" {
		return raw
	}
	if !strings.HasPrefix(raw, "/") {
		return raw
	}
	sep := string(os.PathSeparator)
	root := strings.TrimRight(projectRoot, sep)
	if raw == root {
		return "."
	}
	prefix := root + sep
	if !strings.HasPrefix(raw, prefix) {
		return raw
	}
	rel := raw[len(prefix):]
	if rel == "" {
		return "."
	}
	return rel
}

// hostOf extracts the host portion of a URL for display. Falls back
// to the whole URL if parsing is non-trivial.
func hostOf(url string) string {
	u := url
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	if u == "" {
		return url
	}
	return u
}

// formatFloat renders n with at most one decimal, used for the turn
// count in result events. We want "3" not "3.0", and "3.5" not
// "3.500000". strconv.FormatFloat 'f', -1, 64 produces the latter
// shape for whole values; this helper trims it to the visual norm
// used in skill output.
func formatFloat(n float64) string {
	whole := int64(n)
	if float64(whole) == n {
		return intToString(whole)
	}
	return intToString(whole) + "." + intToString(int64(n*10)%10)
}

// intToString is a tiny helper to avoid pulling strconv into the
// renderer. Negative numbers aren't expected; defensive code returns
// the bare digits unsigned.
func intToString(n int64) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
