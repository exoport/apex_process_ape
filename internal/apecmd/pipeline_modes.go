package apecmd

import (
	"errors"
	"fmt"
	"io"
)

// PipelineMode is the resolved mode for a pipeline invocation.
type PipelineMode int

const (
	// PipelineModeTUI is the Bubble Tea TUI (today's default).
	PipelineModeTUI PipelineMode = iota
	// PipelineModePrint is plain stdout (today's --no-tui shape).
	PipelineModePrint
	// PipelineModeWeb is the bridged web UI (PLAN-5 / C1 + C3).
	PipelineModeWeb
)

// resolvePipelineMode interprets the four pipeline-mode flags and
// returns the resolved mode plus the optOutTUI boolean that the
// existing `useTUI := !optOutTUI && term.IsTerminal(...)` check
// downstream still wants. PLAN-5 / C1.
//
// `--no-tui` is a deprecated alias for `--print`; using it prints a
// stderr warning. Multiple mode flags simultaneously is an error.
//
// Today's default is TUI. The eventual flip to web-default is held
// until a follow-up release per PLAN-5 / C1 phasing — the surface
// here is ready for that flip to be a one-line change.
func resolvePipelineMode(tui, print, noTUI, web bool, stderr io.Writer) (mode PipelineMode, optOutTUI bool, err error) {
	count := 0
	for _, f := range []bool{tui, print, noTUI, web} {
		if f {
			count++
		}
	}
	if count > 1 {
		return PipelineModeTUI, false, errors.New("--tui, --print, --no-tui, and --web are mutually exclusive")
	}
	if noTUI {
		fmt.Fprintln(stderr, "warning: --no-tui is deprecated; use --print instead")
	}
	switch {
	case web:
		return PipelineModeWeb, true, nil
	case print || noTUI:
		return PipelineModePrint, true, nil
	default:
		return PipelineModeTUI, false, nil
	}
}

// resolveModeFlags is the pre-web shape kept for the existing tests
// in pipeline_modes_test.go. New call sites should use
// resolvePipelineMode directly.
func resolveModeFlags(tui, print, noTUI bool, stderr io.Writer) (optOutTUI bool, err error) {
	_, opt, err := resolvePipelineMode(tui, print, noTUI, false, stderr)
	return opt, err
}
