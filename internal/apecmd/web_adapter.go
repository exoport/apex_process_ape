package apecmd

import (
	"html/template"

	"github.com/diegosz/apex_process_ape/internal/bridge/orchestrator"
	"github.com/diegosz/apex_process_ape/internal/web"
)

// webRenderer adapts internal/web template rendering to the
// orchestrator.FragmentRenderer interface. The orchestrator emits
// SSE events via fragRenderer().*; this adapter routes them through
// the embedded HTMX fragments. PLAN-5 / C8.
type webRenderer struct {
	t *template.Template
}

func newWebRenderer(t *template.Template) orchestrator.FragmentRenderer {
	return &webRenderer{t: t}
}

func (r *webRenderer) PipelineInit() string  { return web.RenderPipelineInit(r.t) }
func (r *webRenderer) AwaitPending() string  { return web.RenderAwaitPending(r.t) }
func (r *webRenderer) AwaitResolved() string { return web.RenderAwaitResolved(r.t) }
func (r *webRenderer) Stopped() string {
	return web.RenderStatusBanner(r.t, web.StatusBanner{Class: "disconnected", Text: "Stopped by user"})
}
func (r *webRenderer) BridgeError(msg string) string {
	return web.RenderStatusBanner(r.t, web.StatusBanner{Class: "disconnected", Text: "Bridge error: " + msg})
}
func (r *webRenderer) Reply(content string) string {
	return web.RenderReplyLine(r.t, web.ReplyLine{Content: content})
}
func (r *webRenderer) Hook(event, sessionID, step string) string {
	return web.RenderHookLine(r.t, web.HookLine{Tool: event, Body: step})
}
