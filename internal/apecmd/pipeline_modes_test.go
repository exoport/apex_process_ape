package apecmd

import (
	"bytes"
	"testing"
)

// TestResolvePipelineMode_DefaultsToTUIInteractive covers the PLAN-6
// invocation-matrix default: no flags → tui + interactive. This
// supersedes PLAN-5's web-by-default era.
func TestResolvePipelineMode_DefaultsToTUIInteractive(t *testing.T) {
	var buf bytes.Buffer
	mode, opt, err := resolvePipelineMode(PipelineFlags{}, &buf)
	if err != nil {
		t.Fatalf("resolvePipelineMode: %v", err)
	}
	if mode != PipelineModeTUIInteractive {
		t.Errorf("default mode = %s, want PipelineModeTUIInteractive", describeMode(mode))
	}
	if opt {
		t.Error("tui+interactive default should report optOutTUI=false")
	}
	if buf.Len() != 0 {
		t.Errorf("default should not warn; stderr=%q", buf.String())
	}
}

// TestResolvePipelineMode_PerInvocationMatrix locks the full PLAN-6
// invocation matrix as a table-driven test. Every row corresponds to a
// row in development/planning/plan-6_*.md's invocation table.
func TestResolvePipelineMode_PerInvocationMatrix(t *testing.T) {
	cases := []struct {
		name     string
		flags    PipelineFlags
		wantMode PipelineMode
		wantOpt  bool
		wantErr  bool
	}{
		// Default + explicit equivalents.
		{"default", PipelineFlags{}, PipelineModeTUIInteractive, false, false},
		{"--tui", PipelineFlags{TUI: true}, PipelineModeTUIInteractive, false, false},
		{"--interactive", PipelineFlags{Interactive: true}, PipelineModeTUIInteractive, false, false},
		{"--tui --interactive", PipelineFlags{TUI: true, Interactive: true}, PipelineModeTUIInteractive, false, false},
		// Web variants.
		{"--web", PipelineFlags{Web: true}, PipelineModeWebInteractive, true, false},
		{"--web -P", PipelineFlags{Web: true, Programmatic: true}, PipelineModeWebProgrammatic, true, false},
		// No-UI variants. --no-tui no longer aliases --print.
		{"--no-tui", PipelineFlags{NoTUI: true}, PipelineModeNoneInteractive, true, false},
		{"--no-tui -P", PipelineFlags{NoTUI: true, Programmatic: true}, PipelineModeNoneProgrammatic, true, false},
		// TUI programmatic.
		{"--tui -P", PipelineFlags{TUI: true, Programmatic: true}, PipelineModeTUIProgrammatic, false, false},
		// Print is LOCKED — no exec modifiers permitted.
		{"--print", PipelineFlags{Print: true}, PipelineModePrint, true, false},
		// Mutex errors.
		{"--tui --web", PipelineFlags{TUI: true, Web: true}, 0, false, true},
		{"--tui --print", PipelineFlags{TUI: true, Print: true}, 0, false, true},
		{"--print --no-tui", PipelineFlags{Print: true, NoTUI: true}, 0, false, true},
		{"--print --interactive", PipelineFlags{Print: true, Interactive: true}, 0, false, true},
		{"--print --programmatic", PipelineFlags{Print: true, Programmatic: true}, 0, false, true},
		{"--interactive --programmatic", PipelineFlags{Interactive: true, Programmatic: true}, 0, false, true},
		{"all-four-ui-flags", PipelineFlags{TUI: true, Web: true, NoTUI: true, Print: true}, 0, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			mode, opt, err := resolvePipelineMode(tc.flags, &buf)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if mode != tc.wantMode {
				t.Errorf("mode = %s, want %s", describeMode(mode), describeMode(tc.wantMode))
			}
			if opt != tc.wantOpt {
				t.Errorf("optOutTUI = %v, want %v", opt, tc.wantOpt)
			}
		})
	}
}

// TestResolveModeFlags_LegacyShim verifies the back-compat resolver
// still works for callers that don't yet pass the new exec axis.
func TestResolveModeFlags_LegacyShim(t *testing.T) {
	var buf bytes.Buffer
	opt, err := resolveModeFlags(false, false, false, &buf)
	if err != nil {
		t.Fatalf("resolveModeFlags: %v", err)
	}
	// The new default is tui+interactive, which is NOT optOutTUI.
	if opt {
		t.Error("post-PLAN-6 default should report optOutTUI=false")
	}
}
