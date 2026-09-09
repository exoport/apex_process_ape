package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBuildSettings_EvalStaysByteEmpty is PLAN-6 invariant #1, and it is
// the one deliberate hole in the output-style pin: `--eval` locks
// byte-equivalence with an external consumer that compares the spawn
// shape exactly, so a key added here would break a cross-repo contract.
// Safe because nothing reaches the eval path that needs the pin — the
// framework's own harness does not pass `--eval`.
func TestBuildSettings_EvalStaysByteEmpty(t *testing.T) {
	raw, err := BuildSettings(SettingsOptions{Mode: ModeEval})
	if err != nil {
		t.Fatalf("BuildSettings(eval): %v", err)
	}
	if string(raw) != "{}" {
		t.Errorf("eval settings = %s, want {} — PLAN-6 invariant #1", string(raw))
	}
}

// TestBuildSettings_OutputStyleIsPinnedOnEverySpawnPath is the whole
// point of the pin: a machine's configured output style claims
// precedence over other formatting guidance, and the framework's fenced
// return contracts, guided menus and completion summaries are what that
// precedence would rewrite.
//
// The no-hooks path is covered explicitly because it used to return `{}`
// — a pin written inside the hooks block would have been silently
// missing in exactly the place a plain interactive spawn lands.
func TestBuildSettings_OutputStyleIsPinnedOnEverySpawnPath(t *testing.T) {
	cases := map[string]SettingsOptions{
		"tui, no hooks":   {Mode: ModeTUI},
		"tui, with hooks": {Mode: ModeTUI, InjectHooks: true, APEBin: "/x", BridgePort: 1234},
		"web":             {Mode: ModeWeb, APEBin: "/x", BridgePort: 1234},
		"web, hooks off":  {Mode: ModeWeb, InjectHooks: false, APEBin: "/x", BridgePort: 1234},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := BuildSettings(opts)
			if err != nil {
				t.Fatalf("BuildSettings: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got["outputStyle"] != DefaultOutputStyle {
				t.Errorf("outputStyle = %v, want %q — an unpinned spawn "+
					"inherits the machine's style", got["outputStyle"], DefaultOutputStyle)
			}
		})
	}
}

// TestBuildSettings_OutputStyleOptOut — the operator who wants a style
// deliberately gets one, and "inherit" means no key at all rather than a
// key naming a style called "inherit".
func TestBuildSettings_OutputStyleOptOut(t *testing.T) {
	raw, err := BuildSettings(SettingsOptions{Mode: ModeTUI, OutputStyle: InheritOutputStyle})
	if err != nil {
		t.Fatalf("BuildSettings: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := got["outputStyle"]; present {
		t.Errorf("settings = %s, want no outputStyle key at all", string(raw))
	}

	raw, err = BuildSettings(SettingsOptions{Mode: ModeTUI, OutputStyle: "Explanatory"})
	if err != nil {
		t.Fatalf("BuildSettings: %v", err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["outputStyle"] != "Explanatory" {
		t.Errorf("outputStyle = %v, want Explanatory", got["outputStyle"])
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

// Claude Code is case-sensitive and ignores a name it cannot resolve
// SILENTLY, so a miscased built-in enrols nothing and reports nothing.
// ape is the last place that spelling can be corrected: every
// declaration site — flag, CSV, pipeline YAML — terminates here.
func TestBuildSettings_FoldsBuiltinStyleCase(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"concise", "Concise"},
		{"CONCISE", "Concise"},
		{"Concise", "Concise"},
		{"  concise  ", "Concise"},
		{"explanatory", "Explanatory"},
		{"learning", "Learning"},
		{"proactive", "Proactive"},
		{"default", DefaultOutputStyle},
		{"Default", DefaultOutputStyle},
	} {
		raw, err := BuildSettings(SettingsOptions{Mode: ModeTUI, OutputStyle: tc.in})
		if err != nil {
			t.Fatalf("BuildSettings(%q): %v", tc.in, err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got["outputStyle"] != tc.want {
			t.Errorf("outputStyle for %q = %v, want %q", tc.in, got["outputStyle"], tc.want)
		}
	}
}

// A name matching no built-in is written EXACTLY as declared. ape cannot
// enumerate the custom styles a machine has installed, so guessing at
// one would silently retarget a style its owner defined.
func TestBuildSettings_UnknownStyleIsPassedThroughVerbatim(t *testing.T) {
	for _, name := range []string{"ApexTerse", "apex-terse", "Concsie"} {
		raw, err := BuildSettings(SettingsOptions{Mode: ModeTUI, OutputStyle: name})
		if err != nil {
			t.Fatalf("BuildSettings(%q): %v", name, err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got["outputStyle"] != name {
			t.Errorf("outputStyle for %q = %v, want it unchanged", name, got["outputStyle"])
		}
	}
}

// The opt-out is folded too, so `--output-style INHERIT` cannot silently
// become a style name Claude Code would try to resolve.
func TestBuildSettings_InheritIsCaseInsensitive(t *testing.T) {
	for _, name := range []string{"inherit", "Inherit", "INHERIT", " inherit "} {
		raw, err := BuildSettings(SettingsOptions{Mode: ModeTUI, OutputStyle: name})
		if err != nil {
			t.Fatalf("BuildSettings(%q): %v", name, err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if _, present := got["outputStyle"]; present {
			t.Errorf("outputStyle present for %q, want the key omitted", name)
		}
	}
}

func TestBuiltinOutputStyles_ListsTheCanonicalSpellings(t *testing.T) {
	got := BuiltinOutputStyles()
	want := []string{"Concise", "Default", "Explanatory", "Learning", "Proactive"}
	if len(got) != len(want) {
		t.Fatalf("BuiltinOutputStyles() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("BuiltinOutputStyles()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
