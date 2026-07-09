package cost

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLookupAtSonnetIntro locks PLAN-10 D3 date-aware pricing: Sonnet 5
// bills at the $2/$10 intro rate through 2026-08-31 and the $3/$15
// standard rate after, while the dateless Lookup returns the conservative
// standard rate.
func TestLookupAtSonnetIntro(t *testing.T) {
	intro := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	post := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	if p, _ := LookupAt("claude-sonnet-5", intro); p.BaseInput != 2.00 || p.Output != 10.00 {
		t.Errorf("intro-window price = %+v, want {2,10}", p)
	}
	if p, _ := LookupAt("claude-sonnet-5", SonnetIntroEnd); p.BaseInput != 2.00 {
		t.Errorf("price at SonnetIntroEnd = %+v, want intro {2,10} (inclusive)", p)
	}
	if p, _ := LookupAt("claude-sonnet-5", post); p.BaseInput != 3.00 || p.Output != 15.00 {
		t.Errorf("post-intro price = %+v, want {3,15}", p)
	}
	// Dateless Lookup is the conservative standard rate.
	if p, _ := Lookup("claude-sonnet-5"); p.BaseInput != 3.00 || p.Output != 15.00 {
		t.Errorf("dateless Lookup = %+v, want standard {3,15}", p)
	}
	// A non-windowed model prices identically regardless of date.
	if p, _ := LookupAt("claude-opus-4-8", intro); p.BaseInput != 5.00 || p.Output != 25.00 {
		t.Errorf("opus intro-date price = %+v, want {5,25}", p)
	}
	// The [1m] suffix normalizes onto the base id before the date lookup.
	if p, _ := LookupAt("claude-sonnet-5[1m]", intro); p.BaseInput != 2.00 {
		t.Errorf("sonnet-5[1m] intro price = %+v, want {2,10}", p)
	}
}

// TestOverrideEffectiveFrom locks the optional override dating (D3): an
// override with effective_from applies only to turns at/after it; a
// dateless Lookup ignores it (stays conservative), and an undated override
// wins unconditionally.
func TestOverrideEffectiveFrom(t *testing.T) {
	dir := t.TempDir()
	// os.UserHomeDir reads USERPROFILE on Windows, HOME elsewhere — set
	// both so the override file lands in the temp dir on every platform.
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

	yaml := "prices:\n" +
		"  claude-sonnet-5:\n" +
		"    base_input: 99.0\n" +
		"    output: 990.0\n" +
		"    effective_from: 2026-10-01\n"
	if err := os.MkdirAll(filepath.Join(dir, ".ape"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ape", "prices.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	before := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	after := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)

	// Before effective_from: falls through to the built-in table.
	if p, _ := LookupAt("claude-sonnet-5", before); p.BaseInput == 99.0 {
		t.Errorf("override applied before effective_from: %+v", p)
	}
	// At/after: the override wins.
	if p, _ := LookupAt("claude-sonnet-5", after); p.BaseInput != 99.0 || p.Output != 990.0 {
		t.Errorf("override not applied after effective_from: %+v", p)
	}
	// Dateless Lookup never activates a dated override.
	if p, _ := Lookup("claude-sonnet-5"); p.BaseInput == 99.0 {
		t.Errorf("dateless Lookup activated a dated override: %+v", p)
	}
}
