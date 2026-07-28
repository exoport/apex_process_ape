package cost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEmbeddedPriceTableLoads is the guard on the go:embed asset. init()
// panics on a malformed prices.yaml, so a broken table would take out every
// ape command; this fails the build instead.
func TestEmbeddedPriceTableLoads(t *testing.T) {
	if len(Prices) == 0 {
		t.Fatal("Prices is empty — embedded prices.yaml did not load")
	}
	if PriceTableUpdated == "" {
		t.Error("PriceTableUpdated is empty — prices.yaml is missing its `updated:` stamp")
	}
	if len(familyTiers) == 0 {
		t.Error("familyTiers is empty — the family fallback is the only thing standing between a new model id and a $0 total")
	}
	if SonnetIntroEnd.IsZero() {
		t.Error("SonnetIntroEnd not derived from the claude-sonnet-5 dated window")
	}
}

// TestPriceTableSelfConsistency locks the invariants a stale hand-edit
// would break. This is the release-gate check that needs no transcripts:
// it cannot detect that Claude Code changed a model id, only that the table
// contradicts itself.
func TestPriceTableSelfConsistency(t *testing.T) {
	for alias, full := range modelAliases {
		if _, ok := Prices[full]; !ok {
			t.Errorf("alias %q → %q, which has no row in the price table", alias, full)
		}
		if _, isAlso := Prices[alias]; isAlso {
			t.Errorf("alias %q is also an exact model id — resolution order becomes ambiguous", alias)
		}
	}
	// Every family a model id belongs to must have a tier, or a future
	// generation of that family silently prices at zero.
	for model := range Prices {
		if model == "<synthetic>" || strings.HasPrefix(model, "<") {
			continue
		}
		if ModelFamily(model) == "" {
			t.Errorf("model %q matches no family tier — a future generation of it would be unpriced", model)
		}
	}
	for _, f := range familyTiers {
		if f.Price.BaseInput <= 0 || f.Price.Output <= 0 {
			t.Errorf("family %q has a non-positive rate %+v", f.Family, f.Price)
		}
	}
	// Every alias name must BE a family word. detectAliasDrift and
	// parseGeneration both key on the alias as the family, so an alias named
	// anything else (`s: claude-sonnet-5`) would be silently exempt from drift
	// detection — unchecked, while looking checked.
	families := map[string]bool{}
	for _, f := range familyTiers {
		families[f.Family] = true
	}
	for alias := range modelAliases {
		if !families[alias] {
			t.Errorf("alias %q is not a family word, so it is silently exempt from drift detection; "+
				"add a matching `families:` entry or rename the alias", alias)
		}
	}
}

// TestOpusFiveIsExactlyPriced is the regression lock for the 2026-07-14
// incident: `opus[1m]` began resolving to claude-opus-5, the table had no
// such row, and every cost silently reported $0.00 for 13 days.
func TestOpusFiveIsExactlyPriced(t *testing.T) {
	for _, id := range []string{"claude-opus-5", "claude-opus-5[1m]", "opus", "opus[1m]"} {
		p, src := LookupSourceAt(id, time.Time{})
		if !src.Exact() {
			t.Errorf("%q resolved via %s, want an exact rate", id, src)
		}
		if p.BaseInput != 5.00 || p.Output != 25.00 {
			t.Errorf("%q priced %+v, want {5,25}", id, p)
		}
	}
}

// TestFamilyFallbackPricesUnknownGeneration locks the safety net: a model
// id this binary has never seen must price at its family tier and be
// FLAGGED as an estimate — never silently zero, and never claimed exact.
func TestFamilyFallbackPricesUnknownGeneration(t *testing.T) {
	cases := []struct {
		model string
		in    float64
		out   float64
	}{
		{"claude-opus-9", 5.00, 25.00},
		{"claude-sonnet-7-2", 3.00, 15.00},
		{"claude-haiku-6", 1.00, 5.00},
		{"claude-fable-6", 10.00, 50.00},
	}
	for _, c := range cases {
		p, src := LookupSourceAt(c.model, time.Time{})
		if src != PriceFamily {
			t.Errorf("%q source = %s, want %s", c.model, src, PriceFamily)
		}
		if !src.Estimated() || src.Exact() {
			t.Errorf("%q must report as estimated and NOT exact", c.model)
		}
		if p.BaseInput != c.in || p.Output != c.out {
			t.Errorf("%q priced %+v, want {%v,%v}", c.model, p, c.in, c.out)
		}
		// The exact-match contract is unchanged: LookupAt still says no.
		if _, ok := LookupAt(c.model, time.Time{}); ok {
			t.Errorf("LookupAt(%q) reported ok — the family estimate must not leak into the exact-match API", c.model)
		}
	}
	// A non-Claude id matches nothing and stays honestly unpriced.
	if _, src := LookupSourceAt("fictional-model", time.Time{}); src != PriceNone {
		t.Errorf("fictional-model source = %s, want %s", src, PriceNone)
	}
}

// TestNormalizeModelResilience covers the spellings that reach the pricing
// path from transcripts and hand-written specs alike.
func TestNormalizeModelResilience(t *testing.T) {
	cases := map[string]string{
		"claude-opus-5":             "claude-opus-5",
		"claude-opus-5[1m]":         "claude-opus-5",
		"Claude-Opus-5":             "claude-opus-5",
		"claude_opus_5":             "claude-opus-5",
		"claude-sonnet-4.6":         "claude-sonnet-4-6",
		"claude-haiku-4-5-20251001": "claude-haiku-4-5", // dated snapshot
		"opus":                      "claude-opus-5",    // alias
		"  sonnet  ":                "claude-sonnet-5",
		"<synthetic>":               "<synthetic>",
	}
	for in, want := range cases {
		if got := NormalizeModel(in); got != want {
			t.Errorf("NormalizeModel(%q) = %q, want %q", in, got, want)
		}
	}
	// An unrecognized dated id keeps its full form so coverage reports what
	// Claude Code actually emitted rather than a truncation of it.
	if got := NormalizeModel("claude-unknown-9-20260101"); got != "claude-unknown-9-20260101" {
		t.Errorf("unknown dated id was truncated to %q", got)
	}
}

// TestCanonicalModelArg covers the `--model` / spec `model:` spellings a
// human writes by hand.
func TestCanonicalModelArg(t *testing.T) {
	cases := []struct {
		in         string
		want       string
		recognized bool
	}{
		// A bare family word resolves to the CURRENT generation of that family.
		{"sonnet", "claude-sonnet-5", true},
		{"Sonnet", "claude-sonnet-5", true},
		{"SONNET", "claude-sonnet-5", true},
		{"claude-sonnet", "claude-sonnet-5", true},
		{"opus", "claude-opus-5", true},
		{"claude-opus", "claude-opus-5", true},
		{"haiku", "claude-haiku-4-5", true},
		// An explicit generation is honoured as written.
		{"sonnet-5", "claude-sonnet-5", true},
		{"claude-sonnet-5", "claude-sonnet-5", true},
		{"claude-sonnet-4.6", "claude-sonnet-4-6", true},
		{"claude_sonnet_4_6", "claude-sonnet-4-6", true},
		// The context-window suffix rides along with the resolution.
		{"opus[1m]", "claude-opus-5[1m]", true},
		{"Claude-Opus-5[1m]", "claude-opus-5[1m]", true},
		{"", "", true},
		{"   ", "", true},
		{"gpt-4", "gpt-4", false},
		{"claude-sonet-5", "claude-sonet-5", false}, // typo passes through, flagged
	}
	for _, c := range cases {
		got, recognized := CanonicalModelArg(c.in)
		if got != c.want || recognized != c.recognized {
			t.Errorf("CanonicalModelArg(%q) = (%q, %v), want (%q, %v)",
				c.in, got, recognized, c.want, c.recognized)
		}
	}
}

// TestScanReportsUnpricedWithoutLosingTokens is the core no-silent-zero
// lock. An unpriced model must cost $0 AND keep every token — trading a
// wrong cost for wrong tokens would be strictly worse than the original bug.
func TestScanReportsUnpricedWithoutLosingTokens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	line := `{"type":"assistant","timestamp":"2026-07-20T10:00:00Z","sessionId":"s1","version":"2.1.0",` +
		`"message":{"id":"msg_1","model":"totally-unknown-model","stop_reason":"end_turn",` +
		`"usage":{"input_tokens":1000,"output_tokens":500,"cache_read_input_tokens":200,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":0}}}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := ScanSession(path)
	if err != nil {
		t.Fatalf("ScanSession: %v", err)
	}
	if res.Totals.InputTokens != 1000 || res.Totals.OutputTokens != 500 {
		t.Errorf("tokens lost on an unpriced model: %+v", res.Totals)
	}
	if res.Totals.NumTurns != 1 {
		t.Errorf("NumTurns = %d, want 1", res.Totals.NumTurns)
	}
	if res.Totals.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0 for an unpriced model", res.Totals.CostUSD)
	}
	if res.UnpricedModels["totally-unknown-model"] != 1 {
		t.Errorf("UnpricedModels = %+v, want totally-unknown-model:1", res.UnpricedModels)
	}
	if len(res.Turns) != 1 || res.Turns[0].PriceSource != PriceNone {
		t.Errorf("turn PriceSource = %q, want %q", res.Turns[0].PriceSource, PriceNone)
	}
	note := res.PricingNote()
	if !strings.Contains(note, "unpriced") || !strings.Contains(note, "totally-unknown-model") {
		t.Errorf("PricingNote() = %q, want it to name the unpriced model", note)
	}
}

// TestScanReportsEstimatedModels locks that a family-priced turn is
// non-zero AND reported as an estimate — the number is useful, the caveat
// is mandatory.
func TestScanReportsEstimatedModels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	line := `{"type":"assistant","timestamp":"2026-07-20T10:00:00Z","sessionId":"s1",` +
		`"message":{"id":"msg_1","model":"claude-opus-9","stop_reason":"end_turn",` +
		`"usage":{"input_tokens":1000000,"output_tokens":0}}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ScanSession(path)
	if err != nil {
		t.Fatalf("ScanSession: %v", err)
	}
	if res.Totals.CostUSD != 5.00 {
		t.Errorf("CostUSD = %v, want 5.00 from the opus family tier", res.Totals.CostUSD)
	}
	if res.EstimatedModels["claude-opus-9"] != 1 {
		t.Errorf("EstimatedModels = %+v, want claude-opus-9:1", res.EstimatedModels)
	}
	if len(res.UnpricedModels) != 0 {
		t.Errorf("an estimated model must not also be reported unpriced: %+v", res.UnpricedModels)
	}
	if !strings.Contains(res.PricingNote(), "estimated") {
		t.Errorf("PricingNote() = %q, want it to flag the estimate", res.PricingNote())
	}
}

// TestPricingNoteEmptyWhenHealthy — a fully-priced scan must stay silent,
// or the warning becomes noise everyone learns to ignore.
func TestPricingNoteEmptyWhenHealthy(t *testing.T) {
	var res ScanResult
	if note := res.PricingNote(); note != "" {
		t.Errorf("PricingNote() = %q on a healthy scan, want empty", note)
	}
}

// TestObserveModelsEmptyHomeIsSkipNotPass locks the CI semantics: a sweep
// that finds no transcripts must report "not verified", never "healthy".
func TestObserveModelsEmptyHomeIsSkipNotPass(t *testing.T) {
	rep, err := ObserveModels(t.TempDir(), time.Time{})
	if err != nil {
		t.Fatalf("ObserveModels: %v", err)
	}
	if rep.Observed() {
		t.Error("Observed() true with no transcripts")
	}
	if !strings.Contains(rep.Summary(), "not verified") {
		t.Errorf("Summary() = %q, want it to say coverage was not verified", rep.Summary())
	}
	// OK() is vacuously true with nothing observed — that is exactly why
	// callers must gate on Observed() first.
	if !rep.OK() {
		t.Error("OK() false on an empty sweep")
	}
}

// TestObserveModelsClassifiesLocalTranscripts drives the coverage sweep
// against a synthetic ~/.claude/projects tree.
func TestObserveModelsClassifiesLocalTranscripts(t *testing.T) {
	home := t.TempDir()
	projDir := filepath.Join(home, ".claude", "projects", "-tmp-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"assistant","timestamp":"2026-07-20T10:00:00Z","version":"2.1.220","message":{"id":"m1","model":"claude-opus-5","usage":{"input_tokens":10}}}`,
		`{"type":"assistant","timestamp":"2026-07-20T10:01:00Z","version":"2.1.220","message":{"id":"m2","model":"claude-opus-42","usage":{"input_tokens":10}}}`,
		`{"type":"assistant","timestamp":"2026-07-20T10:02:00Z","version":"2.1.220","message":{"id":"m3","model":"weird-thing","usage":{"input_tokens":10}}}`,
		`{"type":"user","message":{"content":"ignored"}}`,
	}
	if err := os.WriteFile(filepath.Join(projDir, "sid.jsonl"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err := ObserveModels(home, time.Time{})
	if err != nil {
		t.Fatalf("ObserveModels: %v", err)
	}
	if !rep.Observed() || rep.TranscriptsScanned != 1 {
		t.Fatalf("TranscriptsScanned = %d, want 1", rep.TranscriptsScanned)
	}
	if rep.ExactModels != 1 || rep.EstimatedModels != 1 || rep.UnpricedModels != 1 {
		t.Errorf("classification = exact:%d estimated:%d unpriced:%d, want 1/1/1",
			rep.ExactModels, rep.EstimatedModels, rep.UnpricedModels)
	}
	if rep.OK() {
		t.Error("OK() true despite an unpriced model")
	}
	if len(rep.Gaps()) != 2 {
		t.Errorf("Gaps() = %d, want 2", len(rep.Gaps()))
	}
	// Worst first: the unpriced model leads the report.
	if rep.Models[0].Model != "weird-thing" {
		t.Errorf("Models[0] = %q, want the unpriced model first", rep.Models[0].Model)
	}
	if rep.ClaudeVersion != "2.1.220" {
		t.Errorf("ClaudeVersion = %q, want 2.1.220", rep.ClaudeVersion)
	}
}

// TestBareFamilyResolvesToCurrentGeneration is the contract behind writing
// `model: sonnet` in a spec: ape substitutes the family's current concrete
// id rather than deferring to Claude Code.
func TestBareFamilyResolvesToCurrentGeneration(t *testing.T) {
	for _, family := range []string{"opus", "sonnet", "haiku", "fable", "mythos"} {
		want := ResolveFamilyAlias(family)
		if want == "" {
			t.Errorf("family %q has no alias target — a spec saying %q would not resolve", family, family)
			continue
		}
		if _, ok := Prices[want]; !ok {
			t.Errorf("alias %q → %q, which has no exact rate", family, want)
		}
		for _, spelling := range []string{family, "claude-" + family, strings.ToUpper(family)} {
			got, recognized := CanonicalModelArg(spelling)
			if !recognized || got != want {
				t.Errorf("CanonicalModelArg(%q) = (%q, %v), want (%q, true)", spelling, got, recognized, want)
			}
		}
	}
}

// TestDetectAliasDrift locks the safety net that makes pinning acceptable.
// Because a bare family word now selects a concrete model, a stale alias picks
// the WRONG MODEL rather than merely the wrong price — so drift has to be
// detected. The condition is "a strictly newer generation is in use", which is
// what separates real drift from a deliberate pin.
func TestDetectAliasDrift(t *testing.T) {
	opusTarget := ResolveFamilyAlias("opus") // claude-opus-5
	sonnetTarget := ResolveFamilyAlias("sonnet")

	t.Run("newer generation in use is drift", func(t *testing.T) {
		drifts := detectAliasDrift([]ObservedModel{
			{Model: "claude-opus-9", Turns: 100},
			{Model: sonnetTarget, Turns: 50},
		})
		if len(drifts) != 1 {
			t.Fatalf("drifts = %+v, want exactly one (opus)", drifts)
		}
		if drifts[0].Alias != "opus" || drifts[0].Target != opusTarget {
			t.Errorf("drift = %+v, want alias opus → %s", drifts[0], opusTarget)
		}
		if len(drifts[0].Newer) != 1 || drifts[0].Newer[0] != "claude-opus-9" {
			t.Errorf("drift.Newer = %v, want [claude-opus-9]", drifts[0].Newer)
		}
	})

	// THE FALSE POSITIVE THIS DESIGN EXISTS TO KILL. Pinning an older
	// generation everywhere means the alias target never appears — under a
	// mere presence check that failed the release gate over a deliberate
	// choice. An older sibling is not drift.
	t.Run("deliberately pinned older generation is not drift", func(t *testing.T) {
		for _, older := range []string{"claude-opus-4-8", "claude-opus-4-7", "claude-opus-4"} {
			if d := detectAliasDrift([]ObservedModel{{Model: older, Turns: 9999}}); len(d) != 0 {
				t.Errorf("%s (older than %s) reported as drift: %+v", older, opusTarget, d)
			}
		}
	})

	t.Run("alias target itself in use is healthy", func(t *testing.T) {
		if d := detectAliasDrift([]ObservedModel{{Model: opusTarget}}); len(d) != 0 {
			t.Errorf("drift reported when the target is what's running: %+v", d)
		}
	})

	t.Run("nothing observed proves nothing", func(t *testing.T) {
		if d := detectAliasDrift(nil); len(d) != 0 {
			t.Errorf("drift reported with nothing observed: %+v", d)
		}
	})

	// An id ape cannot order must produce no claim either way.
	t.Run("unorderable id is silent", func(t *testing.T) {
		for _, weird := range []string{"claude-3-5-sonnet", "claude-opus-next", "<synthetic>"} {
			if d := detectAliasDrift([]ObservedModel{{Model: weird}}); len(d) != 0 {
				t.Errorf("unorderable %q produced a drift claim: %+v", weird, d)
			}
		}
	})

	t.Run("mixed old and new reports only the newer", func(t *testing.T) {
		drifts := detectAliasDrift([]ObservedModel{
			{Model: "claude-opus-4-8", Turns: 500},
			{Model: opusTarget, Turns: 200},
			{Model: "claude-opus-6", Turns: 10},
		})
		if len(drifts) != 1 {
			t.Fatalf("drifts = %+v, want one", drifts)
		}
		if len(drifts[0].Newer) != 1 || drifts[0].Newer[0] != "claude-opus-6" {
			t.Errorf("Newer = %v, want only [claude-opus-6]", drifts[0].Newer)
		}
	})
}

// TestParseGeneration and TestCompareGenerations pin the ordering the drift
// check rests on. A string sort gets claude-opus-4-8 vs claude-opus-5
// backwards, which is exactly the pair that matters right now.
func TestParseGeneration(t *testing.T) {
	ok := map[string][]int{
		"claude-opus-5":     {5},
		"claude-opus-4-8":   {4, 8},
		"claude-haiku-4-5":  {4, 5},
		"claude-sonnet-4-6": {4, 6},
	}
	for id, want := range ok {
		fam := ModelFamily(id)
		got, parsed := parseGeneration(id, fam)
		if !parsed {
			t.Errorf("parseGeneration(%q, %q) failed to parse", id, fam)
			continue
		}
		if len(got) != len(want) {
			t.Errorf("parseGeneration(%q) = %v, want %v", id, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("parseGeneration(%q) = %v, want %v", id, got, want)
				break
			}
		}
	}
	// Not orderable → no claim. Includes the legacy generation-first scheme.
	for _, id := range []string{"claude-3-5-sonnet", "claude-opus-next", "claude-opus-", "<synthetic>", "opus"} {
		if _, parsed := parseGeneration(id, "opus"); parsed {
			t.Errorf("parseGeneration(%q) claimed to parse", id)
		}
	}
}

func TestCompareGenerations(t *testing.T) {
	cases := []struct {
		a, b []int
		want int // sign
	}{
		{[]int{5}, []int{4, 8}, +1}, // opus 5 newer than opus 4.8
		{[]int{4, 8}, []int{5}, -1}, // and the reverse
		{[]int{6}, []int{5}, +1},
		{[]int{5}, []int{5}, 0},
		{[]int{5, 1}, []int{5}, +1}, // point release newer than its base
		{[]int{5}, []int{5, 1}, -1},
		{[]int{4, 8}, []int{4, 7}, +1},
		{[]int{10}, []int{9}, +1}, // numeric, not lexical ("10" < "9" as text)
	}
	for _, c := range cases {
		got := compareGenerations(c.a, c.b)
		if (got > 0) != (c.want > 0) || (got < 0) != (c.want < 0) {
			t.Errorf("compareGenerations(%v, %v) = %d, want sign %d", c.a, c.b, got, c.want)
		}
	}
}

// TestCoverageOKFailsOnAliasDrift locks that drift blocks the strict gate.
func TestCoverageOKFailsOnAliasDrift(t *testing.T) {
	rep := CoverageReport{
		TranscriptsScanned: 1,
		ExactModels:        1,
		AliasDrifts:        []AliasDrift{{Alias: "opus", Target: "claude-opus-5", Newer: []string{"claude-opus-9"}}},
	}
	if rep.OK() {
		t.Error("OK() true despite alias drift — the release gate would pass a table that selects the wrong model")
	}
	if !strings.Contains(rep.Summary(), "alias drift") {
		t.Errorf("Summary() = %q, want it to mention alias drift", rep.Summary())
	}
}

// TestAliasPointsAtNewestTabledGeneration is the build-time half of the alias
// guard, and the stronger half: it needs no transcripts, so it fails in CI and
// on any developer's machine the moment the table becomes self-inconsistent.
//
// The failure it catches is adding a rate for a new generation and forgetting
// to repoint the alias — `prices:` gains claude-opus-6 but `aliases:` still
// says claude-opus-5. Nothing else notices: the new model is exactly priced,
// so the coverage sweep is clean, and drift detection only fires on a machine
// that has actually run the new model. Every spec saying `model: opus` would
// quietly keep selecting the older generation.
func TestAliasPointsAtNewestTabledGeneration(t *testing.T) {
	for alias, target := range FamilyAliases() {
		targetGen, ok := parseGeneration(target, alias)
		if !ok {
			// A target ape cannot order cannot be checked this way. Not a
			// failure, but say so — a family that silently opts out of this
			// guard should be visible rather than assumed covered.
			t.Logf("alias %q → %q: generation not parseable, ordering unchecked", alias, target)
			continue
		}
		for model := range Prices {
			if model == target || ModelFamily(model) != alias {
				continue
			}
			gen, parsed := parseGeneration(model, alias)
			if !parsed {
				continue
			}
			if compareGenerations(gen, targetGen) > 0 {
				t.Errorf("alias %q → %q, but the table also prices %q, a newer generation of the same family.\n"+
					"Repoint `aliases: %s:` in internal/cost/prices.yaml — every spec saying `model: %s` "+
					"is selecting the older model.", alias, target, model, alias, alias)
			}
		}
	}
}

// TestObserveModelsCachedRespectsTableStamp locks the two ways the cache must
// not lie: it must not outlive its TTL, and it must not survive an ape upgrade
// that ships a different price table. The second matters most — installing a
// fix and being told it did not work would be worse than the slow path.
func TestObserveModelsCachedRespectsTableStamp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// Seed a cache entry that claims a healthy sweep, stamped with a table
	// this binary does not have.
	saveCoverageCache(CoverageReport{TranscriptsScanned: 7, ExactModels: 1})
	entry, ok := loadCoverageCache()
	if !ok {
		t.Fatal("cache did not round-trip")
	}
	if entry.TableStamp != PriceTableUpdated {
		t.Fatalf("saved stamp %q != current %q", entry.TableStamp, PriceTableUpdated)
	}

	// Same stamp, within TTL → served from cache.
	rep, _, fromCache, err := ObserveModelsCached(home, time.Time{})
	if err != nil {
		t.Fatalf("ObserveModelsCached: %v", err)
	}
	if rep.TranscriptsScanned != 7 {
		t.Errorf("TranscriptsScanned = %d, want the cached 7", rep.TranscriptsScanned)
	}
	// fromCache, not age: a hit written moments ago has age ~0, so asserting
	// age > 0 would be a clock-resolution flake.
	if !fromCache {
		t.Error("fromCache false on a cache hit; a cached verdict must be distinguishable from a fresh one")
	}

	// A different table stamp must invalidate immediately, TTL notwithstanding.
	stale := entry
	stale.TableStamp = "1999-01-01"
	bs, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(coverageCachePath(), bs, 0o600); err != nil {
		t.Fatal(err)
	}
	rep, _, fromCache, err = ObserveModelsCached(home, time.Time{})
	if err != nil {
		t.Fatalf("ObserveModelsCached after stamp change: %v", err)
	}
	if fromCache {
		t.Error("a cache entry from a different price table was reused")
	}
	if rep.TranscriptsScanned == 7 {
		t.Error("stale-stamp cache value leaked into the result")
	}

	// TTL of zero disables the cache outright.
	t.Setenv(coverageCacheEnv, "0")
	if _, _, fromCache, err = ObserveModelsCached(home, time.Time{}); err != nil {
		t.Fatalf("ObserveModelsCached with TTL=0: %v", err)
	}
	if fromCache {
		t.Error("cache used despite TTL=0")
	}
}
