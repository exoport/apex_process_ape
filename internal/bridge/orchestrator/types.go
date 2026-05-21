package orchestrator

import (
	"encoding/json"
	"time"
)

// ToolCall mirrors an MCP tools/call seen at the bridge stdio layer.
// PLAN-5 / C6 — bridge-calls.jsonl schema source.
type ToolCall struct {
	ID        string
	Tool      string
	Params    json.RawMessage
	Result    json.RawMessage
	SessionID string
	At        time.Time
}

// HookEvent is one `ape notify` forward. PLAN-5 / C6 — hook-events.jsonl source.
type HookEvent struct {
	Event     string
	SessionID string
	AgentID   string
	Step      string
	Payload   json.RawMessage
	At        time.Time
}

// isDeferredEntry / isFlushEntry inspect the `await_message` tool's
// params payload to discriminate "open a deferred wait" from "flush a
// queued reply". Used by BridgeRuntime.dispatch to emit specialized
// RuntimeEvent kinds.
func isDeferredEntry(raw json.RawMessage) bool {
	var v struct {
		Deferred bool `json:"deferred"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.Deferred
}

func isFlushEntry(raw json.RawMessage) bool {
	var v struct {
		Flush bool `json:"flush"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.Flush
}

// defaultRenderer falls back to the inline HTML placeholders the
// orchestrator used before PLAN-5 / C8. Tests use these; production
// web runs the C8 web template renderer (see internal/web).
type defaultRenderer struct{}

func (defaultRenderer) PipelineInit() string { return `<div id="stages"></div>` }
func (defaultRenderer) Connected() string {
	return `<div id="status" class="connected">connected</div>`
}

func (defaultRenderer) Reply(content string) string {
	return `<div class="reply">` + htmlEscape(content) + `</div>`
}
func (defaultRenderer) AwaitPending() string  { return `<form id="decision-gate" enabled></form>` }
func (defaultRenderer) AwaitResolved() string { return `<form id="decision-gate" disabled></form>` }
func (defaultRenderer) Stopped() string       { return `<div id="status">Stopped by user</div>` }
func (defaultRenderer) BridgeError(msg string) string {
	return `<div id="status">Bridge error: ` + htmlEscape(msg) + `</div>`
}

func (defaultRenderer) HookFromEvent(h HookEvent) string {
	return `<li>` + htmlEscape(h.Event) + ` ` + htmlEscape(h.SessionID) + ` ` + htmlEscape(h.Step) + `</li>`
}

// htmlEscape is the minimal escape used by defaultRenderer for SSE
// payloads. The web template renderer (internal/web) uses Go stdlib
// html/template; this stays here as the no-template fallback for
// tests and for callers that opt out of FragmentRenderer.
func htmlEscape(s string) string {
	var out []byte
	for _, r := range s {
		switch r {
		case '<':
			out = append(out, "&lt;"...)
		case '>':
			out = append(out, "&gt;"...)
		case '&':
			out = append(out, "&amp;"...)
		case '"':
			out = append(out, "&quot;"...)
		default:
			out = append(out, string(r)...)
		}
	}
	return string(out)
}
