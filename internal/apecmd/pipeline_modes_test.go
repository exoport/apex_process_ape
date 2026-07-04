package apecmd

import (
	"bytes"
	"testing"
)

// TestResolvePipelineMode_DefaultsToTUI covers the default: no UI flag →
// tui. ape is PTY-only since v0.0.36, so exec is always interactive.
func TestResolvePipelineMode_DefaultsToTUI(t *testing.T) {
	var buf bytes.Buffer
	mode, opt, err := resolvePipelineMode(PipelineFlags{}, &buf)
	if err != nil {
		t.Fatalf("resolvePipelineMode: %v", err)
	}
	if mode != PipelineModeTUIInteractive {
		t.Errorf("default mode = %s, want PipelineModeTUIInteractive", describeMode(mode))
	}
	if opt {
		t.Error("tui default should report optOutTUI=false")
	}
	if buf.Len() != 0 {
		t.Errorf("default should not warn; stderr=%q", buf.String())
	}
}

// TestResolvePipelineMode_Matrix locks the (now UI-only) invocation
// matrix: one selector at a time, mutual exclusion otherwise.
func TestResolvePipelineMode_Matrix(t *testing.T) {
	cases := []struct {
		name     string
		flags    PipelineFlags
		wantMode PipelineMode
		wantOpt  bool
		wantErr  bool
	}{
		{"default", PipelineFlags{}, PipelineModeTUIInteractive, false, false},
		{"--tui", PipelineFlags{TUI: true}, PipelineModeTUIInteractive, false, false},
		{"--web", PipelineFlags{Web: true}, PipelineModeWebInteractive, true, false},
		{"--no-tui", PipelineFlags{NoTUI: true}, PipelineModeNoneInteractive, true, false},
		{"--tui --web", PipelineFlags{TUI: true, Web: true}, 0, false, true},
		{"--tui --no-tui", PipelineFlags{TUI: true, NoTUI: true}, 0, false, true},
		{"--web --no-tui", PipelineFlags{Web: true, NoTUI: true}, 0, false, true},
		{"all-three", PipelineFlags{TUI: true, Web: true, NoTUI: true}, 0, false, true},
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
