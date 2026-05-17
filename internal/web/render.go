package web

import (
	"bytes"
	"fmt"
	"html/template"
	"time"

	"github.com/diegosz/apex_process_ape/internal/web/views"
)

// PageData is the input for the page template.
type PageData struct {
	Title    string
	Subtitle string
}

// RenderPage returns the full HTML body for GET /.
func RenderPage(t *template.Template, d PageData) string {
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "page", d); err != nil {
		return "<pre>render error: " + template.HTMLEscapeString(err.Error()) + "</pre>"
	}
	return b.String()
}

// RenderStageCard returns the HTML fragment for a stage-card SSE event.
func RenderStageCard(t *template.Template, st *views.Stage) string {
	st.ApplyStatus()
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "stage-card", st); err != nil {
		return ""
	}
	return b.String()
}

// HookLine is the typed input to the hook-line fragment.
type HookLine struct {
	TS   string
	Tool string
	Body string
}

func RenderHookLine(t *template.Template, h HookLine) string {
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "hook-line", h); err != nil {
		return ""
	}
	return b.String()
}

// ReplyLine is the typed input to the reply-line fragment.
type ReplyLine struct {
	StageTag string
	Content  string
}

func RenderReplyLine(t *template.Template, r ReplyLine) string {
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "reply-line", r); err != nil {
		return ""
	}
	return b.String()
}

// RenderAwaitPending / RenderAwaitResolved produce the decision-gate
// OOB swaps for the await-pending / await-resolved SSE events.
func RenderAwaitPending(t *template.Template) string {
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "await-pending", nil); err != nil {
		return ""
	}
	return b.String()
}

func RenderAwaitResolved(t *template.Template) string {
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "await-resolved", nil); err != nil {
		return ""
	}
	return b.String()
}

// RenderStatusBanner returns a status banner fragment for the stopped
// / error / connected SSE events.
type StatusBanner struct {
	Class string // "connected" / "disconnected" / "" (default styling)
	Text  string
}

func RenderStatusBanner(t *template.Template, s StatusBanner) string {
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "status-banner", s); err != nil {
		return ""
	}
	return b.String()
}

// RenderPipelineInit emits the reset-and-scaffold fragment for fresh
// connections. PLAN-5 / C8 — `pipeline-init` event.
func RenderPipelineInit(t *template.Template) string {
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "pipeline-init", nil); err != nil {
		return ""
	}
	return b.String()
}

// FmtDuration is a small helper for stage-card Duration fields. Kept
// here so templates can call it via FuncMap if a future fragment needs it.
func FmtDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// FmtCost formats USD with four decimals. Numbers under $0.01 stay
// readable; rounded to 4dp is enough for sub-step granularity. The
// dashboard route (C7) aggregates to step / run / day with two
// decimals.
func FmtCost(usd float64) string {
	if usd == 0 {
		return "$0"
	}
	return fmt.Sprintf("$%.4f", usd)
}
