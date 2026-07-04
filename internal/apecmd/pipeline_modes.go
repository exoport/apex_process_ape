package apecmd

import (
	"errors"
	"fmt"
	"io"
)

// PipelineMode is the resolved UI mode for a pipeline invocation. ape is
// PTY-only since v0.0.36 (PLAN-9 F2 removed the programmatic `claude -p`
// exec axis and the LOCKED `--eval` mode), so the only remaining
// dimension is where output renders. Every mode executes claude in the
// interactive per-stage in-process PTY (internal/repl).
type PipelineMode int

const (
	// PipelineModeTUIInteractive is the default: Bubble Tea TUI
	// rendering. Equivalent to `ape pipeline <name>` with no UI flag.
	PipelineModeTUIInteractive PipelineMode = iota
	// PipelineModeWebInteractive is `--web`: bridged web UI rendering.
	PipelineModeWebInteractive
	// PipelineModeNoneInteractive is `--no-tui`: no UI surface, plain
	// stdout progress lines.
	PipelineModeNoneInteractive
)

// HasUI reports whether the mode uses an interactive UI surface (TUI or
// web). False for the plain-stdout `--no-tui` mode.
func (m PipelineMode) HasUI() bool {
	return m == PipelineModeTUIInteractive || m == PipelineModeWebInteractive
}

// IsWeb reports whether the mode renders via the web broker.
func (m PipelineMode) IsWeb() bool { return m == PipelineModeWebInteractive }

// IsTUI reports whether the mode renders via Bubble Tea.
func (m PipelineMode) IsTUI() bool { return m == PipelineModeTUIInteractive }

// PipelineFlags bundles the UI selector flags consumed by
// resolvePipelineMode. Grouped so the resolver signature stays stable as
// new UI flags land.
type PipelineFlags struct {
	TUI   bool
	Web   bool
	NoTUI bool
}

// resolvePipelineMode interprets the user's UI selector and returns:
//
//   - mode: the resolved UI PipelineMode
//   - optOutTUI: true when the resolved mode does NOT render the Bubble
//     Tea TUI (used by the caller's `useTUI := !optOutTUI &&
//     term.IsTerminal(...)` guard).
//   - err: non-nil when more than one UI selector is set.
func resolvePipelineMode(flags PipelineFlags, _ io.Writer) (mode PipelineMode, optOutTUI bool, err error) {
	uiCount := 0
	for _, f := range []bool{flags.TUI, flags.Web, flags.NoTUI} {
		if f {
			uiCount++
		}
	}
	if uiCount > 1 {
		return PipelineModeTUIInteractive, false, errors.New("--tui, --web, and --no-tui are mutually exclusive (only one UI selector at a time)")
	}
	switch {
	case flags.Web:
		return PipelineModeWebInteractive, true, nil
	case flags.NoTUI:
		return PipelineModeNoneInteractive, true, nil
	default:
		// Default and explicit --tui both resolve to TUI interactive.
		return PipelineModeTUIInteractive, false, nil
	}
}

// describeMode renders a one-line label for the mode, printed on every
// pipeline start (PLAN-9 F3) so the resolved rendering surface is visible.
func describeMode(m PipelineMode) string {
	switch m {
	case PipelineModeTUIInteractive:
		return "tui (default)"
	case PipelineModeWebInteractive:
		return "web"
	case PipelineModeNoneInteractive:
		return "none (--no-tui)"
	}
	return fmt.Sprintf("unknown(%d)", m)
}
