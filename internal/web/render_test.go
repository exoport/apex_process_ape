package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diegosz/apex_process_ape/internal/web/views"
)

func TestMustTemplates_ParsesAndRendersPage_ChatMode(t *testing.T) {
	tpl := MustTemplates()
	html := RenderPage(tpl, PageData{Title: "ape chat", Subtitle: "test-id", Mode: "chat"})
	for _, want := range []string{
		"<!doctype html>",
		"/assets/styles.css",
		"/assets/vendor/htmx.min.js",
		"/assets/vendor/htmx-ext-sse.min.js",
		`sse-connect="/api/events"`,
		`id="stages"`,
		`id="hooks"`,
		`id="replies"`,
		`id="decision-gate"`,
		`hx-post="/api/stop"`,
		"ape chat",
		"test-id",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("chat page missing %q", want)
		}
	}
}

func TestMustTemplates_ParsesAndRendersPage_PipelineMode(t *testing.T) {
	tpl := MustTemplates()
	html := RenderPage(tpl, PageData{
		Title:    "ape pipeline design",
		Subtitle: "/proj",
		Mode:     "pipeline",
		Stages: []StageSeed{
			{Slug: "create-prd", Name: "create-prd"},
			{Slug: "shard-prd", Name: "shard-prd"},
		},
	})
	// Stage seeds rendered.
	for _, want := range []string{
		`id="stage-create-prd"`,
		`id="stage-shard-prd"`,
		`class="stage pending"`,
		"ape pipeline design",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("pipeline page missing %q", want)
		}
	}
	// Chat-only UI absent.
	for _, mustNot := range []string{`id="decision-gate"`, `id="replies"`} {
		if strings.Contains(html, mustNot) {
			t.Errorf("pipeline page should NOT contain %q", mustNot)
		}
	}
}

func TestRenderStageCard_StatusClassAndGlyph(t *testing.T) {
	tpl := MustTemplates()
	st := &views.Stage{Slug: "design-arch", Name: "design/architecture", Status: "running", Duration: "12.3s"}
	html := RenderStageCard(tpl, st)
	if !strings.Contains(html, `id="stage-design-arch"`) {
		t.Errorf("missing stage id: %s", html)
	}
	if !strings.Contains(html, "running") {
		t.Errorf("missing running class: %s", html)
	}
	if !strings.Contains(html, "design/architecture") {
		t.Errorf("missing stage name: %s", html)
	}
}

func TestRenderHookLine(t *testing.T) {
	tpl := MustTemplates()
	html := RenderHookLine(tpl, HookLine{
		TS:       "10:23:45",
		Event:    "PreToolUse",
		Tool:     "Bash",
		Summary:  "ls -la",
		CSSClass: "tool",
	})
	if !strings.Contains(html, `hx-swap-oob="beforeend:#hooks"`) {
		t.Errorf("missing OOB swap target: %s", html)
	}
	if !strings.Contains(html, `class="hook-row hook-tool"`) {
		t.Errorf("expected hook-row + hook-tool class: %s", html)
	}
	for _, want := range []string{"PreToolUse", "Bash", "ls -la", "10:23:45"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in hook-line html: %s", want, html)
		}
	}
}

func TestRenderHookFragment_PreToolUseBash(t *testing.T) {
	tpl := MustTemplates()
	hf := HookFragment{
		Event:   "PreToolUse",
		Payload: []byte(`{"tool_name":"Bash","tool_input":{"command":"ls -la /tmp"}}`),
	}
	html := RenderHookFragment(tpl, hf)
	for _, want := range []string{"PreToolUse", "Bash", "ls -la /tmp", "hook-tool"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in hook fragment: %s", want, html)
		}
	}
}

func TestRenderHookFragment_UserPromptSubmitTruncates(t *testing.T) {
	tpl := MustTemplates()
	long := strings.Repeat("abcdef ", 50) // 350 chars
	hf := HookFragment{
		Event:   "UserPromptSubmit",
		Payload: []byte(`{"prompt":"` + long + `"}`),
	}
	html := RenderHookFragment(tpl, hf)
	// Should be truncated mid-string (TruncateMid uses an ellipsis).
	if !strings.Contains(html, "…") {
		t.Errorf("expected truncated summary in: %s", html)
	}
}

func TestRenderAwaitPending_EnablesInput(t *testing.T) {
	tpl := MustTemplates()
	html := RenderAwaitPending(tpl)
	if !strings.Contains(html, "skill is awaiting input") {
		t.Errorf("await-pending should label the gate: %s", html)
	}
	if strings.Contains(html, "disabled") {
		t.Errorf("await-pending should NOT carry `disabled`: %s", html)
	}
}

func TestRenderAwaitResolved_DisablesInput(t *testing.T) {
	tpl := MustTemplates()
	html := RenderAwaitResolved(tpl)
	if !strings.Contains(html, "disabled") {
		t.Errorf("await-resolved should carry `disabled`: %s", html)
	}
}

func TestMountAssets_ServesEmbeddedVendor(t *testing.T) {
	mux := http.NewServeMux()
	if err := MountAssets(mux); err != nil {
		t.Fatalf("MountAssets: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, path := range []string{
		"/assets/styles.css",
		"/assets/vendor/htmx.min.js",
		"/assets/vendor/htmx-ext-sse.min.js",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
	}
}

func TestTruncateMid(t *testing.T) {
	cases := []struct {
		in, want string
		n        int
	}{
		{"short", "short", 80},
		{"abcdefghij", "ab…ij", 5},
	}
	for _, tc := range cases {
		if got := views.TruncateMid(tc.in, tc.n); got != tc.want {
			t.Errorf("TruncateMid(%q,%d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}
