package cost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if got["claude-opus-4-7"].Price.BaseInput != 5.00 {
		t.Errorf("opus input = %f, want 5", got["claude-opus-4-7"].Price.BaseInput)
	}
	if got["custom-model"].Price.Output != 30.00 {
		t.Errorf("custom-model output = %f, want 30", got["custom-model"].Price.Output)
	}
	// A row without effective_from applies unconditionally.
	if !got["claude-opus-4-7"].From.IsZero() {
		t.Errorf("undated row gained a date: %v", got["claude-opus-4-7"].From)
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

	override := map[string]OverrideEntry{
		"claude-opus-4-7": {Price: ModelPrice{BaseInput: 99.00, Output: 200.00}},
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

// TestOverrideEffectiveFromSurvivesSaveRoundTrip locks the load → save
// round-trip. LoadOverridesFrom used to validate effective_from and then
// discard it, so `ape costs update --from a-dated-file` persisted an
// override that applied unconditionally — silently repricing history the
// file explicitly excluded.
func TestOverrideEffectiveFromSurvivesSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	resetOverrideCache := func() {
		overridesMu.Lock()
		loadedOverrides = nil
		overridesLoaded = false
		overridesMu.Unlock()
	}
	resetOverrideCache()
	t.Cleanup(resetOverrideCache)

	src := filepath.Join(dir, "src.yaml")
	if err := os.WriteFile(src, []byte(`prices:
  claude-opus-4-7:
    base_input: 99.00
    output: 990.00
    effective_from: 2026-10-01
`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOverridesFrom(src)
	if err != nil {
		t.Fatalf("LoadOverridesFrom: %v", err)
	}
	if loaded["claude-opus-4-7"].From.IsZero() {
		t.Fatal("effective_from dropped on load")
	}
	if err := SaveOverrides(loaded); err != nil {
		t.Fatalf("SaveOverrides: %v", err)
	}

	before := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	after := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	if p, _ := LookupAt("claude-opus-4-7", before); p.BaseInput == 99.00 {
		t.Error("persisted override applied before its effective_from — the date was lost on save")
	}
	if p, _ := LookupAt("claude-opus-4-7", after); p.BaseInput != 99.00 {
		t.Error("persisted override did not apply at/after its effective_from")
	}
}

// TestPriceRowValidationRejectsTypos locks the guard against reintroducing a
// silent zero through a misspelled key in the table itself.
func TestPriceRowValidationRejectsTypos(t *testing.T) {
	cases := map[string]string{
		"misspelled base_input": "prices:\n  claude-typo-5:\n    base_imput: 5.0\n    output: 25.0\n",
		"both zero":             "prices:\n  claude-typo-5:\n    base_input: 0\n    output: 0\n",
		"output only":           "prices:\n  claude-typo-5:\n    base_input: 5.0\n    output: 0\n",
	}
	for name, doc := range cases {
		if _, err := parsePriceTable([]byte(doc)); err == nil {
			t.Errorf("%s: accepted a zero rate; a typo would price those tokens at $0", name)
		}
	}
	// A sentinel id is allowed a genuine zero.
	if _, err := parsePriceTable([]byte("prices:\n  \"<synthetic>\":\n    base_input: 0\n    output: 0\n")); err != nil {
		t.Errorf("sentinel row rejected: %v", err)
	}
}

// TestInvalidOverrideRowIsDroppedNotApplied locks the read path. An override
// wins over the built-in table, so honouring a typo'd row would price that
// model at $0 — the original silent-zero bug arriving through the override file
// rather than the table. Dropping the row falls back to the good built-in rate.
func TestInvalidOverrideRowIsDroppedNotApplied(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	reset := func() {
		overridesMu.Lock()
		loadedOverrides = nil
		overridesLoaded = false
		rejectedOverrides = nil
		overridesMu.Unlock()
	}
	reset()
	t.Cleanup(reset)

	if err := os.MkdirAll(filepath.Join(dir, ".ape"), 0o755); err != nil {
		t.Fatal(err)
	}
	// `base_imput` is a typo: it unmarshals to base_input == 0.
	doc := "prices:\n" +
		"  claude-opus-4-7:\n    base_imput: 5.0\n    output: 25.0\n" +
		"  claude-sonnet-4-6:\n    base_input: 7.5\n    output: 30.0\n"
	if err := os.WriteFile(filepath.Join(dir, ".ape", "prices.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	// The typo'd row must NOT apply — the built-in $5/$25 stands.
	p, ok := Lookup("claude-opus-4-7")
	if !ok {
		t.Fatal("expected the built-in rate to remain available")
	}
	if p.BaseInput == 0 {
		t.Error("a zero-rate override was applied; that model would report as free")
	}
	if p.BaseInput != 5.00 {
		t.Errorf("BaseInput = %v, want the built-in 5.00", p.BaseInput)
	}
	// A valid row alongside it still applies — one bad row must not void the file.
	if v, _ := Lookup("claude-sonnet-4-6"); v.BaseInput != 7.5 {
		t.Errorf("valid override not applied: BaseInput = %v, want 7.5", v.BaseInput)
	}
	// And the rejection is reported rather than lost.
	bad := RejectedOverrides()
	if len(bad) != 1 {
		t.Fatalf("RejectedOverrides() = %v, want exactly one entry", bad)
	}
	if !strings.Contains(bad[0], "claude-opus-4-7") {
		t.Errorf("rejection reason %q does not name the offending model", bad[0])
	}
}
