package apecmd

import (
	"errors"
	"fmt"
	"io"
)

// PipelineMode is the resolved mode for a pipeline invocation.
type PipelineMode int

const (
	// PipelineModeWeb is the bridged web UI (PLAN-5 / C1 + C3).
	// This is the default as of the PLAN-5 release.
	PipelineModeWeb PipelineMode = iota
	// PipelineModeTUI is the Bubble Tea TUI (pre-PLAN-5 default).
	// Now opt-in via --tui.
	PipelineModeTUI
	// PipelineModePrint is plain stdout (eval / CI capture path).
	PipelineModePrint
)

// resolvePipelineMode interprets the four pipeline-mode flags and
// returns the resolved mode plus the optOutTUI boolean that the
// downstream `useTUI := !optOutTUI && term.IsTerminal(...)` check
// still wants. PLAN-5 / C1.
//
// `--no-tui` is a deprecated alias for `--print`; using it prints a
// stderr warning. Multiple mode flags simultaneously is an error.
//
// **Default flipped to web in PLAN-5.** TUI is now opt-in via `--tui`.
// Plain stdout is opt-in via `--print` (`--no-tui` deprecated alias).
// The eval consumer should pin `--print` explicitly; the no-flag form
// `ape pipeline <name>` now spawns a browser.
func resolvePipelineMode(tui, print, noTUI, web bool, stderr io.Writer) (mode PipelineMode, optOutTUI bool, err error) {
	count := 0
	for _, f := range []bool{tui, print, noTUI, web} {
		if f {
			count++
		}
	}
	if count > 1 {
		return PipelineModeWeb, true, errors.New("--tui, --print, --no-tui, and --web are mutually exclusive")
	}
	if noTUI {
		fmt.Fprintln(stderr, "warning: --no-tui is deprecated; use --print instead")
	}
	switch {
	case tui:
		return PipelineModeTUI, false, nil
	case print || noTUI:
		return PipelineModePrint, true, nil
	case web:
		return PipelineModeWeb, true, nil
	default:
		// New default: web. PLAN-5 / C1 — breaking UX change.
		return PipelineModeWeb, true, nil
	}
}

// resolveModeFlags is the pre-web shape kept for the existing tests
// in pipeline_modes_test.go. New call sites should use
// resolvePipelineMode directly.
func resolveModeFlags(tui, print, noTUI bool, stderr io.Writer) (optOutTUI bool, err error) {
	_, opt, err := resolvePipelineMode(tui, print, noTUI, false, stderr)
	return opt, err
}
