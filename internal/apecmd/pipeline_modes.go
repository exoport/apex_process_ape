package apecmd

import (
	"errors"
	"fmt"
	"io"
)

// PipelineMode is the resolved (UI × Exec) mode for a pipeline
// invocation. PLAN-6 / C1 introduces the orthogonal UI / Exec axes:
//
//   - UI    ∈ {none, tui, web}        (where the output renders)
//   - Exec  ∈ {programmatic, interactive} (how claude is spawned)
//
// `--eval` collapses the two axes into a single locked mode for the
// eval consumer (PLAN-6 invariant #1: byte-equivalent stdout, no
// bridge, no hooks). The flag was renamed from `--print` to make
// the byte-equivalence contract evident at the call site.
type PipelineMode int

const (
	// PipelineModeTUIInteractive is the PLAN-6 default: Bubble Tea
	// TUI rendering, one `claude` per stage with bridge step-contract
	// verification. Equivalent to `ape pipeline <name>` with no flags.
	PipelineModeTUIInteractive PipelineMode = iota
	// PipelineModeWebInteractive is `--web` (PLAN-6 default): web UI
	// rendering, interactive exec. Equivalent to PLAN-5's `--web` plus
	// the per-stage claude lifetime change.
	PipelineModeWebInteractive
	// PipelineModeNoneInteractive is `--no-tui`: no UI, but still
	// interactive exec. PLAN-6 stops aliasing `--no-tui` to `--eval`;
	// users who want plain stdout programmatic mode must pass `--eval`.
	PipelineModeNoneInteractive
	// PipelineModeTUIProgrammatic is `--tui -P`: TUI rendering with
	// today's per-step claude spawn. The TUI panels behave the same as
	// PipelineModeTUIInteractive but without per-stage process lifetime.
	PipelineModeTUIProgrammatic
	// PipelineModeWebProgrammatic is `--web -P`: PLAN-5's original
	// `--web` semantics — web UI with per-step claude spawn, hooks
	// captured via the bridge.
	PipelineModeWebProgrammatic
	// PipelineModeNoneProgrammatic is `--no-tui -P`: no UI, per-step
	// claude spawn. Plain stdout streaming.
	PipelineModeNoneProgrammatic
	// PipelineModeEval is `--eval`: LOCKED — no UI, no bridge, no
	// hooks injection, no per-stage spawn, byte-equivalent with the
	// PLAN-5 `--no-tui` output the eval harness consumes. Renamed
	// from PipelineModePrint on 2026-05-20 so the byte-equivalence
	// contract is visible at every call site.
	PipelineModeEval
)

// IsInteractive reports whether the mode uses the per-stage interactive
// exec runtime (PLAN-6 / Phase C). Used by the dispatch in pipeline.go
// to pick between runWithInteractive and the legacy programmatic paths.
func (m PipelineMode) IsInteractive() bool {
	switch m {
	case PipelineModeTUIInteractive, PipelineModeWebInteractive, PipelineModeNoneInteractive:
		return true
	}
	return false
}

// HasUI reports whether the mode uses an interactive UI surface (TUI or
// web). False for the plain-stdout modes.
func (m PipelineMode) HasUI() bool {
	switch m {
	case PipelineModeTUIInteractive, PipelineModeTUIProgrammatic, PipelineModeWebInteractive, PipelineModeWebProgrammatic:
		return true
	}
	return false
}

// IsWeb reports whether the mode renders via the web broker.
func (m PipelineMode) IsWeb() bool {
	switch m {
	case PipelineModeWebInteractive, PipelineModeWebProgrammatic:
		return true
	}
	return false
}

// IsTUI reports whether the mode renders via Bubble Tea.
func (m PipelineMode) IsTUI() bool {
	switch m {
	case PipelineModeTUIInteractive, PipelineModeTUIProgrammatic:
		return true
	}
	return false
}

// IsEval reports whether the mode is the LOCKED eval path (byte-
// equivalence invariant for the eval consumer).
func (m PipelineMode) IsEval() bool { return m == PipelineModeEval }

// PipelineFlags bundles the user-supplied flag values consumed by
// resolvePipelineMode. Grouped so the resolver signature stays stable
// as new exec/UI flags land.
type PipelineFlags struct {
	TUI          bool
	Web          bool
	NoTUI        bool
	Eval         bool
	Interactive  bool
	Programmatic bool
}

// resolvePipelineMode interprets the user's UI + Exec flag selection
// per the PLAN-6 / C1 invocation matrix and returns:
//
//   - mode: the resolved (UI × Exec) PipelineMode
//   - optOutTUI: true when the resolved mode does NOT render the
//     Bubble Tea TUI (used by the caller's `useTUI := !optOutTUI &&
//     term.IsTerminal(...)` guard).
//   - err: non-nil on mutex violations (multiple UI flags, --eval
//     with any modifier, --interactive + --programmatic).
func resolvePipelineMode(flags PipelineFlags, _ io.Writer) (mode PipelineMode, optOutTUI bool, err error) {
	uiCount := 0
	for _, f := range []bool{flags.TUI, flags.Web, flags.NoTUI, flags.Eval} {
		if f {
			uiCount++
		}
	}
	if uiCount > 1 {
		return PipelineModeTUIInteractive, false, errors.New("--tui, --web, --no-tui, and --eval are mutually exclusive (only one UI selector at a time)")
	}
	if flags.Eval {
		if flags.Interactive || flags.Programmatic {
			// --eval is the LOCKED byte-equivalence path; no exec
			// modifier may apply (PLAN-6 invariant #1).
			return PipelineModeTUIInteractive, false, errors.New("--eval admits no exec modifier: drop --interactive / --programmatic")
		}
		return PipelineModeEval, true, nil
	}
	if flags.Interactive && flags.Programmatic {
		return PipelineModeTUIInteractive, false, errors.New("--interactive and --programmatic are mutually exclusive")
	}
	interactive := flags.Interactive || !flags.Programmatic
	switch {
	case flags.Web:
		if interactive {
			return PipelineModeWebInteractive, true, nil
		}
		return PipelineModeWebProgrammatic, true, nil
	case flags.NoTUI:
		if interactive {
			return PipelineModeNoneInteractive, true, nil
		}
		return PipelineModeNoneProgrammatic, true, nil
	case flags.TUI:
		if interactive {
			return PipelineModeTUIInteractive, false, nil
		}
		return PipelineModeTUIProgrammatic, false, nil
	default:
		// PLAN-6 default flip: tui + interactive.
		if interactive {
			return PipelineModeTUIInteractive, false, nil
		}
		return PipelineModeTUIProgrammatic, false, nil
	}
}

// resolveModeFlags is the pre-PLAN-6 shape kept for the legacy tests
// in pipeline_modes_test.go. New call sites use resolvePipelineMode
// directly. The shape returns just the optOutTUI bool for back-compat;
// the (--interactive, --programmatic) axis is fixed at the default
// (interactive).
func resolveModeFlags(tui, eval, noTUI bool, stderr io.Writer) (optOutTUI bool, err error) {
	_, opt, resolveErr := resolvePipelineMode(PipelineFlags{TUI: tui, Eval: eval, NoTUI: noTUI}, stderr)
	if resolveErr != nil {
		return opt, resolveErr
	}
	return opt, nil
}

// describeMode renders a one-line CHANGELOG-style label for the mode,
// used by `--debug` builds and by error messages that want to surface
// the resolved mode back to the user.
func describeMode(m PipelineMode) string {
	switch m {
	case PipelineModeTUIInteractive:
		return "tui + interactive (default)"
	case PipelineModeWebInteractive:
		return "web + interactive"
	case PipelineModeNoneInteractive:
		return "none + interactive (--no-tui)"
	case PipelineModeTUIProgrammatic:
		return "tui + programmatic"
	case PipelineModeWebProgrammatic:
		return "web + programmatic"
	case PipelineModeNoneProgrammatic:
		return "none + programmatic"
	case PipelineModeEval:
		return "eval (LOCKED)"
	}
	return fmt.Sprintf("unknown(%d)", m)
}
