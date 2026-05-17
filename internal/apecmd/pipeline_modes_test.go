package apecmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestResolvePipelineMode_DefaultsToWeb(t *testing.T) {
	// PLAN-5 / C1 — the no-flag form `ape pipeline <name>` now spawns
	// the bridged web UI. The old test asserted the opposite (TUI by
	// default); flipped here as part of the release-cycle merge that
	// shipped the web path.
	var buf bytes.Buffer
	mode, opt, err := resolvePipelineMode(false, false, false, false, &buf)
	if err != nil {
		t.Fatalf("resolvePipelineMode: %v", err)
	}
	if mode != PipelineModeWeb {
		t.Errorf("default mode = %d, want PipelineModeWeb (%d)", mode, PipelineModeWeb)
	}
	if !opt {
		t.Error("default web mode should report optOutTUI=true")
	}
	if buf.Len() != 0 {
		t.Errorf("default should not warn; stderr=%q", buf.String())
	}
}

func TestResolveModeFlags_LegacyShim_DefaultsToOptOutTUI(t *testing.T) {
	// The pre-web shim used by pipeline_modes_test.go. After the
	// default flip it reports optOutTUI=true because web is the new
	// default. Existing callers that passed all-false (i.e. no
	// mode flags) get web semantics — that's the breaking change.
	var buf bytes.Buffer
	opt, err := resolveModeFlags(false, false, false, &buf)
	if err != nil {
		t.Fatalf("resolveModeFlags: %v", err)
	}
	if !opt {
		t.Error("post-flip default should report optOutTUI=true")
	}
}

func TestResolveModeFlags_PrintAndNoTUI_BothOptOut(t *testing.T) {
	// `--print` byte-equivalence with today's `--no-tui` rests on
	// both flags routing through the same value. PLAN-5 / C1.
	cases := []struct {
		name      string
		tui       bool
		print     bool
		noTUI     bool
		wantOut   bool
		wantWarn  bool
		wantError bool
	}{
		// Post-flip semantics: --tui explicitly chooses Bubble Tea,
		// which is the only mode where optOutTUI=false. Everything
		// else (web / print) reports optOutTUI=true.
		{"tui-only", true, false, false, false, false, false},
		{"print-only", false, true, false, true, false, false},
		{"no-tui-only", false, false, true, true, true, false},
		// Error cases: resolveModeFlags now reports optOutTUI=true on
		// any mutex error because the new default (web) is opt-out-TUI.
		{"tui+print", true, true, false, true, false, true},
		{"tui+no-tui", true, false, true, true, false, true},
		{"print+no-tui", false, true, true, true, false, true},
		{"all-three", true, true, true, true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			opt, err := resolveModeFlags(tc.tui, tc.print, tc.noTUI, &buf)
			if (err != nil) != tc.wantError {
				t.Fatalf("err = %v, wantError=%v", err, tc.wantError)
			}
			if opt != tc.wantOut {
				t.Errorf("optOutTUI = %v, want %v", opt, tc.wantOut)
			}
			warned := strings.Contains(buf.String(), "deprecated")
			if warned != tc.wantWarn {
				t.Errorf("warning printed = %v, want %v (stderr=%q)", warned, tc.wantWarn, buf.String())
			}
		})
	}
}
