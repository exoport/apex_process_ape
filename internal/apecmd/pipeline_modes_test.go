package apecmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestResolveModeFlags_Defaults(t *testing.T) {
	var buf bytes.Buffer
	opt, err := resolveModeFlags(false, false, false, &buf)
	if err != nil {
		t.Fatalf("resolveModeFlags: %v", err)
	}
	if opt {
		t.Error("default should keep TUI (optOutTUI=false)")
	}
	if buf.Len() != 0 {
		t.Errorf("default should not warn; stderr=%q", buf.String())
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
		{"tui-only", true, false, false, false, false, false},
		{"print-only", false, true, false, true, false, false},
		{"no-tui-only", false, false, true, true, true, false},
		{"tui+print", true, true, false, false, false, true},
		{"tui+no-tui", true, false, true, false, false, true},
		{"print+no-tui", false, true, true, false, false, true},
		{"all-three", true, true, true, false, false, true},
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
