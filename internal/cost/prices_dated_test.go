package cost

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Claude Sonnet 5 bills 2.00/10.00 at EVERY date. It was announced as an
// intro rate through 2026-08-31 with a rise to 3.00/15.00 scheduled for
// 2026-09-01, and this table encoded that schedule as fact — so from
// 2026-09-01 until the correction, ape priced Sonnet 5 50% high. The
// increase was cancelled and the intro rate became the standard one.
//
// The assertions are deliberately date-spanning rather than a single
// dateless Lookup: the defect was date-shaped, and a test that only asked
// "what does Sonnet cost" would have passed against the wrong table on
// either side of the boundary.
func TestLookupSonnet5IsFlatAcrossTheCancelledBoundary(t *testing.T) {
	inIntro := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	afterIntro := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	wellAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

	for _, ts := range []time.Time{inIntro, afterIntro, wellAfter} {
		if p, _ := LookupAt("claude-sonnet-5", ts); p.BaseInput != 2.00 || p.Output != 10.00 {
			t.Errorf("price at %s = %+v, want {2,10} — the 3/15 increase was cancelled",
				ts.Format("2006-01-02"), p)
		}
	}
	// The dateless lookup agrees, because there is no window to be
	// conservative about any more.
	if p, _ := Lookup("claude-sonnet-5"); p.BaseInput != 2.00 || p.Output != 10.00 {
		t.Errorf("dateless Lookup = %+v, want {2,10}", p)
	}
	// A non-windowed model prices identically regardless of date.
	if p, _ := LookupAt("claude-opus-4-8", inIntro); p.BaseInput != 5.00 || p.Output != 25.00 {
		t.Errorf("opus dated price = %+v, want {5,25}", p)
	}
	// The [1m] suffix still normalizes onto the base id before lookup.
	if p, _ := LookupAt("claude-sonnet-5[1m]", inIntro); p.BaseInput != 2.00 {
		t.Errorf("sonnet-5[1m] price = %+v, want {2,10}", p)
	}
}

// The dated_prices MECHANISM survives the removal of its only entry, so a
// real future window still resolves. Asserted at the parser rather than
// through package globals, which the embedded table owns.
func TestDatedPricesMechanismStillParses(t *testing.T) {
	tbl, err := parsePriceTable([]byte(
		"prices:\n" +
			"  demo-model:\n" +
			"    base_input: 9.00\n" +
			"    output: 90.00\n" +
			"dated_prices:\n" +
			"  demo-model:\n" +
			"    - until: 2026-08-31T23:59:59Z\n" +
			"      base_input: 1.00\n" +
			"      output: 10.00\n"))
	if err != nil {
		t.Fatalf("parsePriceTable: %v", err)
	}
	w := tbl.DatedPrices["demo-model"]
	if len(w) != 1 {
		t.Fatalf("dated window not parsed: %+v", tbl.DatedPrices)
	}
	if w[0].BaseInput != 1.00 || w[0].Output != 10.00 {
		t.Errorf("dated window = %+v, want {1,10}", w[0])
	}
	if w[0].Until.IsZero() {
		t.Error("dated window lost its `until` instant")
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
