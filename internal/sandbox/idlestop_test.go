package sandbox

import (
	"strings"
	"testing"
	"time"
)

func TestParseIdleStop(t *testing.T) {
	for name, tc := range map[string]struct {
		in           string
		wantAfter    time.Duration
		wantDisabled bool
		wantErr      bool
	}{
		"empty means no opinion": {in: ""},
		"off":                    {in: "off", wantDisabled: true},
		"OFF is the same":        {in: "OFF", wantDisabled: true},
		"never":                  {in: "never", wantDisabled: true},
		"false":                  {in: "false", wantDisabled: true},
		"duration":               {in: "4h", wantAfter: 4 * time.Hour},
		"minutes":                {in: "30m", wantAfter: 30 * time.Minute},
		"whitespace":             {in: "  90m  ", wantAfter: 90 * time.Minute},
		"garbage":                {in: "soon", wantErr: true},
		// The one that matters: 0 most plausibly means "never", and reading it as
		// "stop immediately" would reap every workspace the moment it went quiet.
		"zero is an error, not a synonym for off": {in: "0s", wantErr: true},
		"negative is an error":                    {in: "-1h", wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			after, disabled, err := ParseIdleStop(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseIdleStop(%q) = %v, %v, nil; want an error", tc.in, after, disabled)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIdleStop(%q): %v", tc.in, err)
			}
			if after != tc.wantAfter || disabled != tc.wantDisabled {
				t.Errorf("ParseIdleStop(%q) = %v, %v; want %v, %v", tc.in, after, disabled, tc.wantAfter, tc.wantDisabled)
			}
		})
	}
}

func TestParseIdleStopZeroErrorNamesTheFix(t *testing.T) {
	_, _, err := ParseIdleStop("0")
	if err == nil {
		t.Fatal("0 was accepted")
	}
	if !strings.Contains(err.Error(), IdleStopOff) {
		t.Errorf("the error does not say how to actually disable idle-stop: %v", err)
	}
}

func TestResolveIdleStop(t *testing.T) {
	const nodeDefault = 2 * time.Hour
	for name, tc := range map[string]struct {
		value     string
		nodeAfter time.Duration
		wantAfter time.Duration
		wantOK    bool
	}{
		"no opinion takes the node default": {value: "", nodeAfter: nodeDefault, wantAfter: nodeDefault, wantOK: true},
		"workspace narrows":                 {value: "30m", nodeAfter: nodeDefault, wantAfter: 30 * time.Minute, wantOK: true},
		"workspace widens":                  {value: "8h", nodeAfter: nodeDefault, wantAfter: 8 * time.Hour, wantOK: true},
		"workspace opts out":                {value: "off", nodeAfter: nodeDefault},
		// A node with no reaper ignores everything a workspace asks for. No value
		// here can make a node reap something it otherwise would not.
		"node reaper off":            {value: "30m"},
		"node reaper off, and 'off'": {value: "off"},
		// A bad value that somehow reached here falls back to the node default
		// rather than silently disabling the reaper — the failure nobody notices.
		"garbage falls back, not off": {value: "soon", nodeAfter: nodeDefault, wantAfter: nodeDefault, wantOK: true},
	} {
		t.Run(name, func(t *testing.T) {
			after, ok := ResolveIdleStop(tc.value, tc.nodeAfter)
			if ok != tc.wantOK || after != tc.wantAfter {
				t.Errorf("ResolveIdleStop(%q, %v) = %v, %v; want %v, %v",
					tc.value, tc.nodeAfter, after, ok, tc.wantAfter, tc.wantOK)
			}
		})
	}
}
