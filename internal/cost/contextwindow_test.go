package cost

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestContextWindow_ResolutionOrder walks the whole ladder. The two rows
// that matter most are the `[1m]` pair and the unknowns.
func TestContextWindow_ResolutionOrder(t *testing.T) {
	for _, tc := range []struct {
		model      string
		wantWindow int
		wantSource WindowSource
		why        string
	}{
		{"claude-opus-5", 1_000_000, WindowExact, "exact table row"},
		{"opus", 1_000_000, WindowExact, "alias resolves before lookup"},
		{"claude-sonnet-5", 1_000_000, WindowExact, "Sonnet 5 is 1M, not 200k"},
		{
			"claude-sonnet-4-5", 200_000, WindowExact,
			"pre-4.6 models kept the 200k default — the generation boundary is real",
		},
		{"claude-haiku-4-5", 200_000, WindowExact, "Haiku 4.5 is 200k even in the 4.5+ era"},
		{
			"claude-haiku-4-5-20251001", 200_000, WindowExact,
			"a dated snapshot resolves to its base model, same as pricing",
		},
		{
			"fictional-model", 0, WindowNone,
			"an id we know nothing about stays unknown",
		},
		{
			"<synthetic>", 0, WindowNone,
			"a sentinel is not a model; IsSyntheticModel keeps it out of the gap report",
		},
	} {
		t.Run(tc.model, func(t *testing.T) {
			w, src := ContextWindow(tc.model)
			require.Equal(t, tc.wantWindow, w, tc.why)
			require.Equal(t, tc.wantSource, src, tc.why)
		})
	}
}

// TestContextWindow_SuffixWins is the acceptance criterion the framework
// asked for, and the one place this differs from price resolution.
//
// A suffixed and unsuffixed model are the same model at the same rate —
// NormalizeModel strips the suffix so both attribute to one pricing bucket —
// but they can be different amounts of CONTEXT. If window resolution reused
// the pricing key it would report the base window for both, and every
// occupancy ratio on a 1M step would read 5x high: the exact failure this
// table was added to retire, reproduced one layer down.
//
// The test asserts on `claude-sonnet-4-5` rather than `opus`, and the reason
// is worth keeping. The framework's request framed this as "opus and
// opus[1m] differ by 5x", which was true when the Opus base window was 200K.
// From Opus 4.6 / Sonnet 4.6 onward 1M IS the base, so those two now agree
// and would assert nothing. The pre-4.6 models kept a 200K default with a 1M
// opt-in, so they are where the mechanism is still observable — and a test
// that cannot fail is worse than no test.
func TestContextWindow_SuffixWins(t *testing.T) {
	base, baseSrc := ContextWindow("claude-sonnet-4-5")
	suffixed, suffixedSrc := ContextWindow("claude-sonnet-4-5[1m]")

	require.Equal(t, 200_000, base)
	require.Equal(t, WindowExact, baseSrc)
	require.Equal(t, 1_000_000, suffixed)
	require.Equal(t, WindowSuffix, suffixedSrc)
	require.NotEqual(t, base, suffixed, "the suffix must not collapse into the base window")

	// The suffix wins over an alias too, and case-folds.
	w, src := ContextWindow("opus[1m]")
	require.Equal(t, 1_000_000, w)
	require.Equal(t, WindowSuffix, src)
	w, src = ContextWindow("Sonnet-4-5[1M]")
	require.Equal(t, 1_000_000, w)
	require.Equal(t, WindowSuffix, src)

	// Pricing is deliberately unmoved by the suffix — the two axes disagree
	// on purpose, and that is what makes the suffix-aware path necessary.
	pBase, _ := LookupSourceAt("claude-sonnet-4-5", time.Time{})
	pSuffixed, _ := LookupSourceAt("claude-sonnet-4-5[1m]", time.Time{})
	require.Equal(t, pBase, pSuffixed, "same model, same rate — only the window differs")
}

// TestContextWindow_GenerationBoundary locks the fact that made the first
// shipped values wrong: 1M is not a beta opt-in on current models, it is the
// standard window from Opus 4.6 / Sonnet 4.6 onward. Encoding 200K for those
// would divide every occupancy ratio by a fifth of the real window — the
// same fivefold inflation, on the newest and most-used models.
func TestContextWindow_GenerationBoundary(t *testing.T) {
	for _, m := range []string{
		"claude-fable-5-1", "claude-fable-5", "claude-mythos-5", "claude-opus-5",
		"claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6",
		"claude-sonnet-5", "claude-sonnet-4-6",
	} {
		w, src := ContextWindow(m)
		require.Equal(t, 1_000_000, w, "%s is a 1M-context model", m)
		require.Equal(t, WindowExact, src, "%s", m)
	}
	for _, m := range []string{
		"claude-opus-4-5", "claude-opus-4-1", "claude-opus-4",
		"claude-sonnet-4-5", "claude-sonnet-4", "claude-haiku-4-5",
	} {
		w, src := ContextWindow(m)
		require.Equal(t, 200_000, w, "%s predates the 1M default", m)
		require.Equal(t, WindowExact, src, "%s", m)
	}
}

// An unrecognized suffix yields NO window rather than the base model's.
// `opus[7m]` means the caller asked for a size this binary does not know;
// answering 200K would be a wrong number instead of a missing one, and a
// wrong denominator is worse than an absent one.
func TestContextWindow_UnknownSuffixIsUnknownNotBase(t *testing.T) {
	w, src := ContextWindow("opus[7m]")
	require.Zero(t, w)
	require.Equal(t, WindowNone, src)

	base, _ := ContextWindow("opus")
	require.NotZero(t, base, "the base model does have a window — the suffix is what suppressed it")
}

// TestContextWindow_NoFamilyFallbackInShippedTable locks the deliberate
// omission. The schema supports a family window; the shipped table has
// none, because a family guess fails toward the SMALLER window and would
// silently divide a future 1M model by 200k.
func TestContextWindow_NoFamilyFallbackInShippedTable(t *testing.T) {
	// A plausible future id of a known family: priced by the family tier,
	// and deliberately NOT given that family's window.
	_, priceSrc := LookupSourceAt("claude-sonnet-9", time.Time{})
	require.Equal(t, PriceFamily, priceSrc, "the family tier still prices it")

	w, src := ContextWindow("claude-sonnet-9")
	require.Zero(t, w, "a family window would be a guess at a denominator")
	require.Equal(t, WindowNone, src)

	for _, f := range familyTiers {
		require.Zero(t, f.Window,
			"family %q has a context_window; see the families: comment in prices.yaml", f.Family)
	}
}

// TestContextWindow_OverrideRoundTrip is the acceptance criterion for
// correcting a window without rebuilding the binary: it must survive
// `ape costs update --from <file>` → ~/.ape/prices.yaml → the next lookup.
func TestContextWindow_OverrideRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	resetOverridesForTest()
	t.Cleanup(resetOverridesForTest)

	// Baseline from the built-in table.
	w, src := ContextWindow("claude-sonnet-4-5")
	require.Equal(t, 200_000, w)
	require.Equal(t, WindowExact, src)

	assetPath := filepath.Join(home, "corrected.yaml")
	require.NoError(t, os.WriteFile(assetPath, []byte(
		"prices:\n"+
			"  claude-sonnet-4-5:\n"+
			"    base_input: 3.00\n"+
			"    output: 15.00\n"+
			"    context_window: 1000000\n",
	), 0o600))

	loaded, err := LoadOverridesFrom(assetPath)
	require.NoError(t, err)
	require.Equal(t, 1_000_000, loaded["claude-sonnet-4-5"].Window)

	require.NoError(t, SaveOverrides(loaded))
	resetOverridesForTest()

	w, src = ContextWindow("claude-sonnet-4-5")
	require.Equal(t, 1_000_000, w, "the correction reached the lookup with no new binary")
	require.Equal(t, WindowOverride, src)
}

// A price-only override must not ERASE the built-in window. The two travel
// in one row, so a rate correction that omits the window has to fall
// through rather than blank it — otherwise fixing a price silently removes
// a denominator.
func TestContextWindow_PriceOnlyOverrideKeepsBuiltinWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	resetOverridesForTest()
	t.Cleanup(resetOverridesForTest)

	require.NoError(t, SaveOverrides(map[string]OverrideEntry{
		"claude-sonnet-4-5": {Price: ModelPrice{BaseInput: 9, Output: 45}},
	}))
	resetOverridesForTest()

	w, src := ContextWindow("claude-sonnet-4-5")
	require.Equal(t, 200_000, w)
	require.Equal(t, WindowExact, src, "fell through to the built-in table, not blanked")
}

// resetOverridesForTest drops the process-wide override cache so a test
// that rewrites ~/.ape/prices.yaml sees its own file.
func resetOverridesForTest() {
	overridesMu.Lock()
	loadedOverrides = nil
	overridesLoaded = false
	overridesMu.Unlock()
}

// TestScanSession_WindowComesFromTheRawSpelling is the regression lock for
// the eval's 2026-08-29 finding: the `[1m]` suffix reaches ape's own
// transcripts, and resolving a bucket's window from the normalized key
// threw it away.
//
// `claude-sonnet-4-5` and `claude-sonnet-4-5[1m]` fold to ONE pricing
// bucket on purpose — same model, same rate — so the key cannot carry the
// distinction and anything derived from it reports 200k for a 1M step.
// The window therefore has to be read before normalization, and this test
// asserts it survives the whole scan.
func TestScanSession_WindowComesFromTheRawSpelling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(
		assistantLine("claude-sonnet-4-5[1m]")+"\n"+
			assistantLine("claude-sonnet-4-5[1m]")+"\n",
	), 0o600))

	res, err := ScanSession(path)
	require.NoError(t, err)

	// One pricing bucket, under the normalized id — unchanged behaviour.
	require.Len(t, res.ByModel, 1)
	require.Contains(t, res.ByModel, "claude-sonnet-4-5")
	// …and the variant's window, not the base model's.
	require.Equal(t, 1_000_000, res.ModelWindows["claude-sonnet-4-5"],
		"the [1m] window must survive normalization of the key")
	require.NotEqual(t, 200_000, res.ModelWindows["claude-sonnet-4-5"])
}

// A bucket holding BOTH spellings has no single right window — the two
// answers differ by 5x — so it collapses to unknown rather than picking a
// side. Reachable in practice: a step's sub-agents need not run the
// spelling the step was spawned with.
func TestScanSession_MixedVariantsCollapseToUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(
		assistantLine("claude-sonnet-4-5")+"\n"+
			assistantLine("claude-sonnet-4-5[1m]")+"\n",
	), 0o600))

	res, err := ScanSession(path)
	require.NoError(t, err)
	require.Len(t, res.ByModel, 1, "still one pricing bucket")
	w, seen := res.ModelWindows["claude-sonnet-4-5"]
	require.True(t, seen, "the bucket was observed")
	require.Zero(t, w, "two windows in one bucket is unknowable, not 200k")
}

// The ordinary case: no suffix anywhere, window from the model's own row.
func TestScanSession_UnsuffixedWindowIsTheModelsOwn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(
		assistantLine("claude-opus-5")+"\n",
	), 0o600))

	res, err := ScanSession(path)
	require.NoError(t, err)
	require.Equal(t, 1_000_000, res.ModelWindows["claude-opus-5"])
}

// assistantLine is one transcript row naming `model`, with enough usage to
// count as a real turn.
func assistantLine(model string) string {
	return `{"type":"assistant","timestamp":"2026-08-29T12:00:00Z",` +
		`"sessionId":"s1","message":{"id":"msg_` + model + `","model":"` + model + `",` +
		`"usage":{"input_tokens":10,"output_tokens":5}}}`
}
