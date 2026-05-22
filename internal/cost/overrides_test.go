package cost

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOverridesFrom_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prices.yaml")
	if err := os.WriteFile(path, []byte(`prices:
  claude-opus-4-7:
    base_input: 5.00
    output: 25.00
  custom-model:
    base_input: 7.50
    output: 30.00
`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadOverridesFrom(path)
	if err != nil {
		t.Fatalf("LoadOverridesFrom: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got["claude-opus-4-7"].BaseInput != 5.00 {
		t.Errorf("opus input = %f, want 5", got["claude-opus-4-7"].BaseInput)
	}
	if got["custom-model"].Output != 30.00 {
		t.Errorf("custom-model output = %f, want 30", got["custom-model"].Output)
	}
}

func TestLoadOverridesFrom_Errors(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantSub string
	}{
		{"empty file", "", "no `prices:` map"},
		{"no prices key", "other: x", "no `prices:` map"},
		{"negative price", "prices:\n  bad:\n    base_input: -1\n    output: 1\n", "negative"},
		{"malformed yaml", "not: yaml: at all:\n  - [", "parse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "prices.yaml")
			_ = os.WriteFile(path, []byte(tc.content), 0o644)
			_, err := LoadOverridesFrom(path)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestLookup_OverrideWinsOverBuiltin(t *testing.T) {
	// Redirect HOME so SaveOverrides / loadOverridesOnce hit a tmp path.
	// On Windows, os.UserHomeDir() reads USERPROFILE, not HOME — set
	// both so the override file lands in t.TempDir() on every platform
	// and gets cleaned up by the test framework. Without USERPROFILE
	// set on Windows, the $99/$200 opus override would leak into the
	// real C:\Users\…\.ape\prices.yaml and poison subsequent tests
	// (TestScanSessionJSONL_Aggregates would see $5+$12.50 jump to
	// $99+$100=$199).
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// Drop any cached overrides from previous tests in the same binary.
	resetOverrideCache := func() {
		overridesMu.Lock()
		loadedOverrides = nil
		overridesLoaded = false
		overridesMu.Unlock()
	}
	resetOverrideCache()
	// Reset again after the test so unrelated tests in the same binary
	// don't pick up our $99 opus rate.
	t.Cleanup(resetOverrideCache)

	override := map[string]ModelPrice{
		"claude-opus-4-7": {BaseInput: 99.00, Output: 200.00},
	}
	if err := SaveOverrides(override); err != nil {
		t.Fatalf("SaveOverrides: %v", err)
	}
	got, ok := Lookup("claude-opus-4-7")
	if !ok {
		t.Fatal("expected lookup hit")
	}
	if got.BaseInput != 99.00 {
		t.Errorf("override ignored: BaseInput=%f, want 99", got.BaseInput)
	}
}
