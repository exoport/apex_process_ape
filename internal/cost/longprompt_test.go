package cost

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Claude Haiku 5.5 is billed by prompt length: $0.10 / $0.50 per MTok for a
// prompt of up to 100,000 tokens, $0.50 / $2.50 above it. A flat row at the
// short-prompt rate would price every turn past the threshold 5x low, and a
// Claude Code session's prompt passes it as the conversation grows.
func TestHaiku55IsPricedByPromptLength(t *testing.T) {
	t.Parallel()

	p, ok := Lookup("claude-haiku-5-5")
	if !ok {
		t.Fatal("claude-haiku-5-5 has no exact row")
	}
	if p.BaseInput != 0.10 || p.Output != 0.50 {
		t.Errorf("short-prompt rate = %v / %v, want 0.10 / 0.50", p.BaseInput, p.Output)
	}
	want := PromptTier{Over: 100_000, BaseInput: 0.50, Output: 2.50}
	if p.LongPrompt != want {
		t.Errorf("long-prompt tier = %+v, want %+v", p.LongPrompt, want)
	}
	if got := p.CacheReadMultiplier(); got != DefaultCacheReadMul {
		t.Errorf("cache-read multiple = %v, want the standard %v", got, DefaultCacheReadMul)
	}
}

// The `haiku` family word starts Claude Haiku 5.5 as of Claude Code 2.1.294.
// ape translates the word itself, so a stale alias runs the previous model.
func TestHaikuAliasIsHaiku55(t *testing.T) {
	t.Parallel()

	if got := NormalizeModel("haiku"); got != "claude-haiku-5-5" {
		t.Errorf("haiku → %q, want claude-haiku-5-5", got)
	}
}

// The threshold is "over 100,000": a prompt of exactly 100,000 tokens is
// still a short prompt.
func TestTurnCostSelectsTheTierAtTheThreshold(t *testing.T) {
	t.Parallel()

	p, ok := Lookup("claude-haiku-5-5")
	if !ok {
		t.Fatal("claude-haiku-5-5 missing")
	}
	for _, tc := range []struct {
		name   string
		input  int
		output int
		want   float64
	}{
		{"at the threshold", 100_000, 1_000, 100_000*0.10/perMillion + 1_000*0.50/perMillion},
		{"one past it", 100_001, 1_000, 100_001*0.50/perMillion + 1_000*2.50/perMillion},
	} {
		got := TurnCost(UsageBlock{InputTokens: tc.input, OutputTokens: tc.output}, p)
		if math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("%s: cost = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The cached prefix is part of the prompt. Nearly all of a Claude Code
// turn's prompt is cache, so a threshold read off input_tokens alone would
// keep almost every turn on the cheap rate. Every cache term also bills at
// its usual multiple of the SELECTED base rate.
func TestTurnCostCountsTheCachedPrefixTowardTheThreshold(t *testing.T) {
	t.Parallel()

	p, ok := Lookup("claude-haiku-5-5")
	if !ok {
		t.Fatal("claude-haiku-5-5 missing")
	}
	u := UsageBlock{
		InputTokens:   10,
		OutputTokens:  200,
		CacheRead:     90_000,
		CacheCreation: CacheCreation{Ephemeral5m: 6_000, Ephemeral1h: 4_000},
	}
	if got := u.PromptTokens(); got != 100_010 {
		t.Fatalf("PromptTokens = %d, want 100010", got)
	}
	want := (10*0.50 +
		6_000*0.50*CacheCreationEphemeral5mMul +
		4_000*0.50*CacheCreationEphemeral1hMul +
		90_000*0.50*DefaultCacheReadMul +
		200*2.50) / perMillion
	if got := TurnCost(u, p); math.Abs(got-want) > 1e-12 {
		t.Errorf("cost = %v, want %v (long-prompt rate on every term)", got, want)
	}
}

// A model with no tier bills one rate at every length.
func TestTurnCostWithoutATierIsFlat(t *testing.T) {
	t.Parallel()

	p := ModelPrice{BaseInput: 1, Output: 5}
	u := UsageBlock{InputTokens: 500_000, OutputTokens: 1_000}
	want := (500_000*1.0 + 1_000*5.0) / perMillion
	if got := TurnCost(u, p); math.Abs(got-want) > 1e-12 {
		t.Errorf("cost = %v, want %v", got, want)
	}
}

// A tier with a misspelled key decodes to a zero rate and would bill every
// long prompt as free; a tier with no threshold would never apply. Both are
// rejected rather than loaded.
func TestLongPromptTierValidation(t *testing.T) {
	t.Parallel()

	head := "prices:\n  claude-tier-5:\n    base_input: 0.10\n    output: 0.50\n    long_prompt:\n"
	for name, tier := range map[string]string{
		"misspelled base_input": "      over: 100000\n      base_imput: 0.50\n      output: 2.50\n",
		"no threshold":          "      base_input: 0.50\n      output: 2.50\n",
		"negative rate":         "      over: 100000\n      base_input: -0.50\n      output: 2.50\n",
	} {
		if _, err := parsePriceTable([]byte(head + tier)); err == nil {
			t.Errorf("%s: table accepted", name)
		}
	}
	if _, err := parsePriceTable([]byte(head + "      over: 100000\n      base_input: 0.50\n      output: 2.50\n")); err != nil {
		t.Errorf("valid tier rejected: %v", err)
	}
}

// `ape costs update --from` loads a file and SaveOverrides persists it. The
// save built its row field by field, so anything it did not name was lost:
// a persisted override came back without its tier, and without its
// cache_read_mul.
func TestOverrideTierAndCacheReadSurviveSaveRoundTrip(t *testing.T) {
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
  claude-haiku-5-5:
    base_input: 0.20
    output: 1.00
    cache_read_mul: 0.05
    long_prompt:
      over: 50000
      base_input: 0.80
      output: 4.00
`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOverridesFrom(src)
	if err != nil {
		t.Fatalf("LoadOverridesFrom: %v", err)
	}
	if err := SaveOverrides(loaded); err != nil {
		t.Fatalf("SaveOverrides: %v", err)
	}
	saved, err := os.ReadFile(overridesPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"long_prompt:", "over: 50000", "cache_read_mul: 0.05"} {
		if !strings.Contains(string(saved), key) {
			t.Errorf("saved overrides lack %q:\n%s", key, saved)
		}
	}

	p, ok := Lookup("claude-haiku-5-5")
	if !ok {
		t.Fatal("override lookup failed")
	}
	want := ModelPrice{
		BaseInput: 0.20, Output: 1.00, CacheReadMul: 0.05,
		LongPrompt: PromptTier{Over: 50_000, BaseInput: 0.80, Output: 4.00},
	}
	if p != want {
		t.Errorf("persisted override = %+v, want %+v", p, want)
	}
}
