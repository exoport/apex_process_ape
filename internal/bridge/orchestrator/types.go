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
