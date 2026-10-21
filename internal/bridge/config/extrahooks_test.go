package config

import (
	"encoding/json"
	"testing"
)

// ExtraHooks ride beside the bridge hooks, each in its own group with its
// matcher and timeout; the bridge's own hook for the event is kept.
func TestBuildSettings_ExtraHooks(t *testing.T) {
	raw, err := BuildSettings(SettingsOptions{
		APEBin: "/bin/ape", BridgePort: 4242, Mode: ModeTUI, InjectHooks: true,
		ExtraHooks: []ExtraHook{
			{Event: "Stop", Command: "ape hook stop --inline", Timeout: 1800},
			{Event: "PreToolUse", Matcher: "Edit|Write", Command: "ape hook pre-tool --inline", OnFailure: "block"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]hookSpec `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	stop := s.Hooks["Stop"]
	if len(stop) != 2 || stop[1].Hooks[0].Command != "ape hook stop --inline" || stop[1].Hooks[0].Timeout != 1800 || stop[1].Hooks[0].Async {
		t.Errorf("Stop groups = %+v", stop)
	}
	pre := s.Hooks["PreToolUse"]
	if len(pre) != 2 || pre[1].Matcher != "Edit|Write" || pre[1].Hooks[0].Timeout != 0 || pre[1].Hooks[0].OnFailure != "block" {
		t.Errorf("PreToolUse groups = %+v", pre)
	}
	if stop[1].Hooks[0].OnFailure != "" {
		t.Errorf("Stop gained an onFailure: %+v", stop[1])
	}
	if len(s.Hooks["SessionStart"]) != 1 {
		t.Errorf("SessionStart gained a group: %+v", s.Hooks["SessionStart"])
	}
}
