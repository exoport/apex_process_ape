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

func TestBuildSettings_WebInjectsAllHooks(t *testing.T) {
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

	wantEvents := []string{
		"PreToolUse", "PostToolUse", "UserPromptSubmit",
		"SubagentStart", "SubagentStop",
		"SessionStart", "PreCompact",
		"Stop",
	}
	// Exact set, not a subset: an event registered here spawns a hook
	// subprocess on every occurrence, so one added by accident is a cost
	// nobody chose to pay.
	if len(got.Hooks) != len(wantEvents) {
		t.Errorf("registered %d events, want %d: %v", len(got.Hooks), len(wantEvents), got.Hooks)
	}
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

// TestBuildSettings_BlobSizeUnderArgLimit is a canary on the settings
// JSON's size, not a limit the argv path imposes — that is fine to 128 KB
// on Linux and 32 KB on Windows, and the blob is two orders of magnitude
// below both.
//
// The ceiling was 1024 while the blob carried PLAN-5 / C4's six events
// (876 bytes). The two session-boundary events took it to 1164, so the
// canary moves to 2048 rather than being deleted: each registered event
// costs ~145 bytes AND a hook subprocess per occurrence, and the size is
// the cheap proxy for the cost that actually matters. Roughly six more
// events would trip it again, which is the right moment to re-ask whether
// every one of them is earning its spawn.
func TestBuildSettings_BlobSizeUnderArgLimit(t *testing.T) {
	raw, err := BuildSettings(SettingsOptions{
		APEBin:     "/usr/local/bin/ape",
		BridgePort: 47291,
		Mode:       ModeWeb,
	})
	if err != nil {
		t.Fatalf("BuildSettings: %v", err)
	}
	if len(raw) > 2048 {
		t.Errorf("settings blob is %d bytes, expected <2048", len(raw))
	}
}
