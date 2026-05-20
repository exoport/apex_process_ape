package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildSettings_NonWebReturnsEmptyObject(t *testing.T) {
	for _, mode := range []Mode{ModeEval, ModeTUI} {
		t.Run(mode.String(), func(t *testing.T) {
			raw, err := BuildSettings(SettingsOptions{Mode: mode})
			if err != nil {
				t.Fatalf("BuildSettings(%s): %v", mode, err)
			}
			if string(raw) != "{}" {
				t.Errorf("non-web settings = %s, want {}", string(raw))
			}
		})
	}
}

func TestBuildSettings_WebInjectsAllSixHooks(t *testing.T) {
	raw, err := BuildSettings(SettingsOptions{
		APEBin:     "/usr/local/bin/ape",
		BridgePort: 47291,
		Mode:       ModeWeb,
	})
	if err != nil {
		t.Fatalf("BuildSettings: %v", err)
	}

	var got struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Async   bool   `json:"async"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	wantEvents := []string{"PreToolUse", "PostToolUse", "UserPromptSubmit", "SubagentStart", "SubagentStop", "Stop"}
	for _, ev := range wantEvents {
		entries, ok := got.Hooks[ev]
		if !ok {
			t.Errorf("missing hook for %s", ev)
			continue
		}
		if len(entries) != 1 || len(entries[0].Hooks) != 1 {
			t.Errorf("%s: expected exactly one hook entry with one command, got %+v", ev, entries)
			continue
		}
		h := entries[0].Hooks[0]
		if h.Type != "command" {
			t.Errorf("%s: hook type = %q, want command", ev, h.Type)
		}
		if !strings.Contains(h.Command, "APE_BRIDGE_PORT=47291") {
			t.Errorf("%s: command missing APE_BRIDGE_PORT=47291: %q", ev, h.Command)
		}
		if !strings.Contains(h.Command, "/usr/local/bin/ape notify --event "+ev) {
			t.Errorf("%s: command does not invoke `ape notify --event %s`: %q", ev, ev, h.Command)
		}
		// Stop is the only sync hook (PLAN-5 / C4 — flush + close
		// per-step run-log; let the loop wait briefly).
		wantAsync := ev != "Stop"
		if h.Async != wantAsync {
			t.Errorf("%s: async = %v, want %v", ev, h.Async, wantAsync)
		}
	}
}

func TestBuildSettings_WebErrorsOnMissingAPEBin(t *testing.T) {
	_, err := BuildSettings(SettingsOptions{Mode: ModeWeb, BridgePort: 1234})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "APEBin is empty") {
		t.Errorf("error = %q, want substring 'APEBin is empty'", err.Error())
	}
}

func TestBuildSettings_WebErrorsOnInvalidPort(t *testing.T) {
	for _, p := range []int{0, -1, 70000} {
		_, err := BuildSettings(SettingsOptions{Mode: ModeWeb, APEBin: "/x", BridgePort: p})
		if err == nil {
			t.Errorf("port=%d: expected error, got nil", p)
			continue
		}
		if !strings.Contains(err.Error(), "BridgePort") {
			t.Errorf("port=%d: error = %q, want substring 'BridgePort'", p, err.Error())
		}
	}
}

// TestBuildSettings_BlobSizeUnderArgLimit locks PLAN-5 / C2's "<1 KB"
// invariant on the settings JSON. If a future hook adds enough surface
// to blow past 1 KB the runner needs revisiting (the argv path is
// fine up to 128 KB on Linux, but the assertion is a useful canary).
func TestBuildSettings_BlobSizeUnderArgLimit(t *testing.T) {
	raw, err := BuildSettings(SettingsOptions{
		APEBin:     "/usr/local/bin/ape",
		BridgePort: 47291,
		Mode:       ModeWeb,
	})
	if err != nil {
		t.Fatalf("BuildSettings: %v", err)
	}
	if len(raw) > 1024 {
		t.Errorf("settings blob is %d bytes, expected <1024", len(raw))
	}
}
