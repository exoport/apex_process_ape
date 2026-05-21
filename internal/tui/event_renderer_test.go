//nolint:testpackage // see pipeline_test.go for the rationale on internal-package tests
package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diegosz/apex_process_ape/internal/bridge/orchestrator"
	"github.com/stretchr/testify/require"
)

func TestRenderEvent_EmptyLineSuppressed(t *testing.T) {
	r := RenderEvent("")
	require.Equal(t, EventSuppressed, r.Kind)
	require.False(t, r.IsDisplayable())
}

func TestRenderEvent_InvalidJSONFallsThrough(t *testing.T) {
	r := RenderEvent("not json")
	require.Equal(t, EventUnknown, r.Kind)
	require.Equal(t, "?", r.Glyph)
	require.Equal(t, "not json", r.Body)
}

func TestRenderEvent_SystemInit(t *testing.T) {
	r := RenderEvent(`{"type":"system","subtype":"init","session_id":"abc"}`)
	require.Equal(t, EventSystem, r.Kind)
	require.Equal(t, "·", r.Glyph)
	require.Contains(t, r.Body, "session start")
}

func TestRenderEvent_AssistantTextOneLine(t *testing.T) {
	r := RenderEvent(`{"type":"assistant","message":{"content":[{"type":"text","text":"Drafting the ADR table."}]}}`)
	require.Equal(t, EventText, r.Kind)
	require.Equal(t, "✎", r.Glyph)
	require.Equal(t, "Drafting the ADR table.", r.Body)
}

func TestRenderEvent_AssistantTextMultilineCollapses(t *testing.T) {
	r := RenderEvent(`{"type":"assistant","message":{"content":[{"type":"text","text":"line one\nline two\nline three"}]}}`)
	require.Equal(t, EventText, r.Kind)
	require.Equal(t, "line one", r.Body, "multiline collapses to first line")
}

func TestRenderEvent_AssistantTextTruncates(t *testing.T) {
	long := strings.Repeat("x", 200)
	r := RenderEvent(`{"type":"assistant","message":{"content":[{"type":"text","text":"` + long + `"}]}}`)
	require.Equal(t, EventText, r.Kind)
	require.True(t, strings.HasSuffix(r.Body, "…"), "expected truncation ellipsis, got %q", r.Body)
	// Allow the 3-byte UTF-8 ellipsis slack (lipgloss renders it as
	// one cell). The bound is a tight visual ceiling, not byte-exact.
	require.LessOrEqual(t, len(r.Body), maxTextLen+3)
}

func TestRenderEvent_ToolUseRead(t *testing.T) {
	r := RenderEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"development/planning/prd.md"}}]}}`)
	require.Equal(t, EventTool, r.Kind)
	require.Equal(t, "🔧", r.Glyph)
	require.Equal(t, "Read development/planning/prd.md", r.Body)
}

func TestRenderEvent_ToolUseEdit(t *testing.T) {
	r := RenderEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"foo.md"}}]}}`)
	require.Equal(t, EventTool, r.Kind)
	require.Equal(t, "Edit foo.md", r.Body)
}

func TestRenderEvent_ToolUseWrite(t *testing.T) {
	r := RenderEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"out.yaml"}}]}}`)
	require.Equal(t, EventTool, r.Kind)
	require.Equal(t, "Write out.yaml", r.Body)
}

func TestRenderEvent_ToolUseBashTruncates(t *testing.T) {
	long := "echo " + strings.Repeat("x", 200)
	r := RenderEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"` + long + `"}}]}}`)
	require.Equal(t, EventTool, r.Kind)
	require.True(t, strings.HasSuffix(r.Body, "…"), "expected truncation ellipsis, got %q", r.Body)
	// Truncated command is bounded by maxBashLen bytes (plus the
	// 3-byte UTF-8 ellipsis lipgloss renders as one cell). Allow a
	// 5-byte slack so the bound is a tight visual ceiling, not a
	// brittle byte-exact assertion.
	require.LessOrEqual(t, len(r.Body), len("Bash ")+maxBashLen+5,
		"body too long: %q (%d bytes)", r.Body, len(r.Body))
}

func TestRenderEvent_ToolUseGrepWithPath(t *testing.T) {
	r := RenderEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"foo","path":"src"}}]}}`)
	require.Equal(t, `Grep "foo" src`, r.Body)
}

func TestRenderEvent_ToolUseGrepNoPath(t *testing.T) {
	r := RenderEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"foo"}}]}}`)
	require.Equal(t, `Grep "foo"`, r.Body)
}

func TestRenderEvent_ToolUseGlob(t *testing.T) {
	r := RenderEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Glob","input":{"pattern":"**/*.md"}}]}}`)
	require.Equal(t, `Glob "**/*.md"`, r.Body)
}

func TestRenderEvent_ToolUseTask(t *testing.T) {
	r := RenderEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Task","input":{"subagent_type":"reviewer","description":"Check the ADR table"}}]}}`)
	require.Equal(t, `Task reviewer "Check the ADR table"`, r.Body)
}

func TestRenderEvent_ToolUseWebFetch(t *testing.T) {
	r := RenderEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"WebFetch","input":{"url":"https://docs.anthropic.com/some/path?q=1"}}]}}`)
	require.Equal(t, "WebFetch docs.anthropic.com", r.Body)
}

func TestRenderEvent_ToolUseWebSearch(t *testing.T) {
	r := RenderEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"WebSearch","input":{"query":"claude code stream-json"}}]}}`)
	require.Equal(t, `WebSearch "claude code stream-json"`, r.Body)
}

func TestRenderEvent_ToolUseUnknownFallsThroughByName(t *testing.T) {
	r := RenderEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"MyMCPTool","input":{"x":1}}]}}`)
	require.Equal(t, EventTool, r.Kind)
	require.Equal(t, "MyMCPTool", r.Body)
}

func TestRenderEvent_ToolResultSuccessSuppressed(t *testing.T) {
	r := RenderEvent(`{"type":"user","message":{"content":[{"type":"tool_result","is_error":false,"content":"File created successfully at /tmp/foo"}]}}`)
	require.Equal(t, EventSuppressed, r.Kind, "successful Read/Edit/Write-style results are noise")
}

func TestRenderEvent_ToolResultErrorSurfaced(t *testing.T) {
	r := RenderEvent(`{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":"file not found: /tmp/foo"}]}}`)
	require.Equal(t, EventToolError, r.Kind)
	require.Equal(t, "↳", r.Glyph)
	require.Contains(t, r.Body, "⚠")
	require.Contains(t, r.Body, "file not found")
}

func TestRenderEvent_ToolResultStructuredContent(t *testing.T) {
	// Anthropic sometimes nests the content as [{type:"text",text:"..."}]
	r := RenderEvent(`{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":[{"type":"text","text":"compilation failed: missing token"}]}]}}`)
	require.Equal(t, EventToolError, r.Kind)
	require.Contains(t, r.Body, "compilation failed")
}

func TestRenderEvent_ResultSuccess(t *testing.T) {
	r := RenderEvent(`{"type":"result","subtype":"success","is_error":false,"num_turns":3,"duration_ms":1234}`)
	require.Equal(t, EventSuccess, r.Kind)
	require.Equal(t, "✓", r.Glyph)
	require.Contains(t, r.Body, "skill complete")
	require.Contains(t, r.Body, "3 turns")
}

func TestRenderEvent_ResultError(t *testing.T) {
	r := RenderEvent(`{"type":"result","subtype":"error","is_error":true,"result":"validation rejected the output"}`)
	require.Equal(t, EventFailure, r.Kind)
	require.Equal(t, "✗", r.Glyph)
	require.Contains(t, r.Body, "skill failed")
	require.Contains(t, r.Body, "validation rejected")
}

func TestRenderEvent_ResultErrorByIsErrorField(t *testing.T) {
	// is_error=true without an explicit error subtype still classifies
	// as failure — the field is the canonical signal.
	r := RenderEvent(`{"type":"result","subtype":"success","is_error":true,"result":"surprise"}`)
	require.Equal(t, EventFailure, r.Kind)
}

func TestRenderEvent_UnknownTypeFallsThroughRaw(t *testing.T) {
	raw := `{"type":"telemetry_event","ms":42}`
	r := RenderEvent(raw)
	require.Equal(t, EventUnknown, r.Kind)
	require.Equal(t, raw, r.Body)
}

func TestRenderEvent_AssistantEmptyContentSuppressed(t *testing.T) {
	r := RenderEvent(`{"type":"assistant","message":{"content":[]}}`)
	require.Equal(t, EventSuppressed, r.Kind)
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://docs.anthropic.com/some/path": "docs.anthropic.com",
		"http://example.com":                   "example.com",
		"example.com/path":                     "example.com",
		"https://github.com/foo/bar?baz=1":     "github.com",
		"":                                     "",
		"not-a-url-but-no-scheme/and/some/path?q=1#frag": "not-a-url-but-no-scheme",
	}
	for in, want := range cases {
		require.Equal(t, want, hostOf(in), "hostOf(%q)", in)
	}
}

func TestTruncate(t *testing.T) {
	require.Equal(t, "abc", truncate("abc", 10))
	require.Equal(t, "abcde…", truncate("abcdefghij", 6))
	require.Equal(t, "ab…", truncate("abcde", 3))
}

// ─────────── PLAN-2 / F6 project-relative paths ───────────

func TestRelativizePath_StripsProjectPrefix(t *testing.T) {
	root := "/tmp/ape-v007-smoke-c70b"
	cases := map[string]string{
		"/tmp/ape-v007-smoke-c70b/development/planning/prd/index.md": "development/planning/prd/index.md",
		"/tmp/ape-v007-smoke-c70b":                                   ".",
		"/tmp/ape-v007-smoke-c70b/":                                  ".",
		"/tmp/other/development/planning":                            "/tmp/other/development/planning",     // different root
		"/home/diegos/_dev/foo":                                      "/home/diegos/_dev/foo",               // system path
		"relative/path/foo.md":                                       "relative/path/foo.md",                // not absolute
		"":                                                           "",                                    // empty
		"/tmp/ape-v007-smoke-c70b-not-prefix":                        "/tmp/ape-v007-smoke-c70b-not-prefix", // path-boundary check
	}
	for raw, want := range cases {
		require.Equal(t, want, relativizePath(root, raw), "relativizePath(%q, %q)", root, raw)
	}
}

func TestRelativizePath_EmptyRootIsNoOp(t *testing.T) {
	// When projectRoot is empty F6 is disabled and absolute paths
	// pass through. Mirrors how RenderEvent (no-root variant) and
	// the pre-F6 TUI behaved.
	require.Equal(t, "/abs/path", relativizePath("", "/abs/path"))
	require.Equal(t, "rel/path", relativizePath("", "rel/path"))
}

func TestRenderEvent_RelativizesReadFilePath(t *testing.T) {
	root := "/tmp/ape-v007-smoke-c70b"
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/tmp/ape-v007-smoke-c70b/development/planning/prd/index.md"}}]}}`
	r := RenderEventWithRoot(line, root)
	require.Equal(t, EventTool, r.Kind)
	require.Equal(t, "Read development/planning/prd/index.md", r.Body)
}

const sandboxRoot = "/sandbox"

func TestRenderEvent_RelativizesEditAndWriteFilePath(t *testing.T) {
	root := sandboxRoot
	cases := map[string]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/sandbox/_apex/config.yaml"}}]}}`:                     "Edit _apex/config.yaml",
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/sandbox/development/functionality/index.yaml"}}]}}`: "Write development/functionality/index.yaml",
	}
	for line, want := range cases {
		r := RenderEventWithRoot(line, root)
		require.Equal(t, EventTool, r.Kind)
		require.Equal(t, want, r.Body)
	}
}

func TestRenderEvent_RelativizesGrepAndGlobPath(t *testing.T) {
	root := sandboxRoot
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"foo","path":"/sandbox/internal"}}]}}`
	r := RenderEventWithRoot(line, root)
	require.Equal(t, EventTool, r.Kind)
	require.Equal(t, `Grep "foo" internal`, r.Body)

	line = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Glob","input":{"pattern":"**/*.md","path":"/sandbox/docs"}}]}}`
	r = RenderEventWithRoot(line, root)
	require.Equal(t, `Glob "**/*.md" docs`, r.Body)
}

func TestRenderEvent_OutsideRootStaysAbsolute(t *testing.T) {
	// Tool calls into framework source or system paths shouldn't be
	// mistakenly stripped — only paths inside the project root.
	root := sandboxRoot
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/home/user/.claude/skills/SKILL.md"}}]}}`
	r := RenderEventWithRoot(line, root)
	require.Equal(t, "Read /home/user/.claude/skills/SKILL.md", r.Body)
}

func TestRenderEvent_NoRootBackCompat(t *testing.T) {
	// RenderEvent (root-less variant) is back-compat and must render
	// absolute paths unchanged.
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/abs/foo.md"}}]}}`
	r := RenderEvent(line)
	require.Equal(t, "Read /abs/foo.md", r.Body)
}

// ─────────── PLAN-7 / FB hook-event renderer ───────────

// hookEventFixture is the JSON wire shape captured in
// testdata/hook_events/*.json. It maps almost one-to-one to
// orchestrator.HookEvent but lets payload stay a nested object in
// the fixture file (human-readable) while the Go struct expects
// json.RawMessage.
type hookEventFixture struct {
	Event   string          `json:"event"`
	Step    string          `json:"step"`
	Payload json.RawMessage `json:"payload"`
}

func loadHookFixture(t *testing.T, name string) orchestrator.HookEvent {
	t.Helper()
	path := filepath.Join("testdata", "hook_events", name)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "load fixture %s", name)
	var f hookEventFixture
	require.NoError(t, json.Unmarshal(data, &f))
	return orchestrator.HookEvent{Event: f.Event, Step: f.Step, Payload: f.Payload}
}

func TestRenderHookEvent_UserPromptSubmit(t *testing.T) {
	h := loadHookFixture(t, "userpromptsubmit.json")
	r := RenderHookEvent(h, "")
	require.Equal(t, EventText, r.Kind)
	require.Equal(t, "?", r.Glyph)
	require.Contains(t, r.Body, "apex-pattern-reconciliation")
	require.True(t, r.IsDisplayable())
	require.NotEmpty(t, r.Raw, "Raw must be populated for styleRawJSON")
}

func TestRenderHookEvent_PreToolUseReadStripsProjectRoot(t *testing.T) {
	h := loadHookFixture(t, "pretooluse_read.json")
	r := RenderHookEvent(h, "/home/diegos/_dev/ape-web-sandbox/greeter")
	require.Equal(t, EventTool, r.Kind)
	require.Equal(t, "🔧", r.Glyph)
	require.Equal(t, "Read _apex/config.yaml", r.Body, "project-root prefix must be stripped (PLAN-2 / F6 reuse)")
}

func TestRenderHookEvent_PreToolUseAbsoluteWithoutRoot(t *testing.T) {
	h := loadHookFixture(t, "pretooluse_read.json")
	r := RenderHookEvent(h, "")
	require.Equal(t, "Read /home/diegos/_dev/ape-web-sandbox/greeter/_apex/config.yaml", r.Body,
		"no projectRoot ⇒ render absolute")
}

func TestRenderHookEvent_PostToolUseSuccess(t *testing.T) {
	h := loadHookFixture(t, "posttooluse_read_ok.json")
	r := RenderHookEvent(h, "")
	require.Equal(t, EventToolResult, r.Kind)
	require.Equal(t, "↳", r.Glyph)
	require.Contains(t, r.Body, "Local-only")
}

func TestRenderHookEvent_PostToolUseError(t *testing.T) {
	h := loadHookFixture(t, "posttooluse_error.json")
	r := RenderHookEvent(h, "")
	require.Equal(t, EventToolError, r.Kind)
	require.Equal(t, "↳", r.Glyph)
	require.Contains(t, r.Body, "⚠")
	require.Contains(t, r.Body, "command failed")
}

func TestRenderHookEvent_Stop(t *testing.T) {
	h := loadHookFixture(t, "stop.json")
	r := RenderHookEvent(h, "")
	require.Equal(t, EventSuccess, r.Kind)
	require.Equal(t, "✓", r.Glyph)
	require.Contains(t, r.Body, "skill complete")
	require.Contains(t, r.Body, "Pattern Reconciliation Complete")
}

func TestRenderHookEvent_Notification(t *testing.T) {
	h := loadHookFixture(t, "notification.json")
	r := RenderHookEvent(h, "")
	require.Equal(t, EventSystem, r.Kind)
	require.Equal(t, "·", r.Glyph)
	require.Contains(t, r.Body, "permission")
}

func TestRenderHookEvent_UnknownSuppressed(t *testing.T) {
	h := loadHookFixture(t, "unknown.json")
	r := RenderHookEvent(h, "")
	require.Equal(t, EventSuppressed, r.Kind)
	require.False(t, r.IsDisplayable())
}

func TestRenderHookEvent_EmptyPayloadSuppressed(t *testing.T) {
	r := RenderHookEvent(orchestrator.HookEvent{Event: "PreToolUse"}, "")
	require.Equal(t, EventSuppressed, r.Kind)
}

func TestRenderHookEvent_MalformedPayload(t *testing.T) {
	r := RenderHookEvent(orchestrator.HookEvent{
		Event:   "PreToolUse",
		Payload: json.RawMessage("not json"),
	}, "")
	require.Equal(t, EventUnknown, r.Kind, "schema drift must not panic — falls through to Unknown")
}
