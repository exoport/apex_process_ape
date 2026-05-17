package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diegosz/apex_process_ape/internal/web/views"
)

func TestMustTemplates_ParsesAndRendersPage(t *testing.T) {
	tpl := MustTemplates()
	html := RenderPage(tpl, PageData{Title: "ape chat", Subtitle: "test-id"})
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
			t.Errorf("page missing %q", want)
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
	html := RenderHookLine(tpl, HookLine{TS: "10:23:45", Tool: "PreToolUse", Body: "Bash ls -la"})
	if !strings.Contains(html, `hx-swap-oob="beforeend:#hooks"`) {
		t.Errorf("missing OOB swap target: %s", html)
	}
	if !strings.Contains(html, "PreToolUse") || !strings.Contains(html, "Bash") {
		t.Errorf("missing content: %s", html)
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
		if resp.StatusCode != 200 {
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
