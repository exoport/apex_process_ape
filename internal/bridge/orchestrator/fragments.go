package orchestrator

// FragmentRenderer abstracts the C8 web template render functions so
// the orchestrator can emit HTML fragments without importing the web
// package directly (avoids an import cycle if the web package ever
// needs to reach into orchestrator state).
//
// Implementations live in internal/web; chat.go wires a small adapter
// at the call site. Returning the empty string falls back to a small
// inline placeholder so tests can run the orchestrator without
// templates.
type FragmentRenderer interface {
	PipelineInit() string
	Connected() string
	Reply(content string) string
	AwaitPending() string
	AwaitResolved() string
	Stopped() string
	BridgeError(msg string) string
	HookFromEvent(h HookEvent) string
}
