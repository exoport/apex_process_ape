package cost

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// manifestFixture is a run manifest recorded while the price table had no
// rate for claude-opus-5 — every cost_usd is the $0.00 the 2026-07-14 gap
// produced, while the token counts are intact. That intactness is what makes
// the run recoverable.
//
// The indentation here is deliberately 4-space: that is what yaml.Marshal
// emits, and therefore what ape's own manifest writer puts on disk. A fixture
// written at 2-space would silently hide a whole-document reformat, which is
// exactly what the round-trip test below exists to catch.
const manifestFixture = `run_id: 20260720-120000-abc123
started_at: 2026-07-20T12:00:00Z
# a comment that must survive the rewrite
totals:
    cost_usd: 0
    tokens_input: 1000
    tokens_output: 500
    num_turns: 2
    model_usage:
        claude-opus-5:
            cost_usd: 0
            tokens_input: 1000000
            tokens_output: 0
            tokens_cache_read: 0
            tokens_cache_creation: 0
            tokens_cache_creation_5m: 0
            tokens_cache_creation_1h: 0
            num_turns: 1
steps:
    - index: 0
      skill: some-skill
      cost_usd: 0
      model_usage:
        claude-opus-5:
            cost_usd: 0
            tokens_input: 1000000
            tokens_output: 0
            num_turns: 1
`

// The byte-exact comparison below needs no line-ending normalization: the Go
// spec discards carriage returns inside raw string literals, so manifestFixture
// holds LF only even from an autocrlf=true Windows checkout. (Verified by
// converting this file to CRLF and re-running.) A fixture kept in a testdata
// FILE would not be safe that way — that is why internal/apecmd/testdata/update
// is pinned `-text` in .gitattributes.
func writeManifest(t *testing.T, root, content string) string {
	t.Helper()
	dir := filepath.Join(root, "_output", "pipelines", "demo", "20260720-120000-abc123")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRepriceDryRunLeavesFilesAlone locks the default: a preview must not
// touch a byte on disk.
func TestRepriceDryRunLeavesFilesAlone(t *testing.T) {
	root := t.TempDir()
	path := writeManifest(t, root, manifestFixture)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	rep, err := Reprice(root, false)
	if err != nil {
		t.Fatalf("Reprice: %v", err)
	}
	if rep.Scanned != 1 || rep.Changed != 1 {
		t.Errorf("scanned=%d changed=%d, want 1/1", rep.Scanned, rep.Changed)
	}
	if rep.Written != 0 {
		t.Errorf("dry run wrote %d file(s)", rep.Written)
	}
	// 1M input tokens on claude-opus-5 at $5/1M = $5.00.
	if !floatNear(rep.NewTotal, 5.00) {
		t.Errorf("NewTotal = %v, want 5.00", rep.NewTotal)
	}
	if !floatNear(rep.OldTotal, 0) {
		t.Errorf("OldTotal = %v, want 0", rep.OldTotal)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("dry run modified the manifest on disk")
	}
}

// TestRepriceWriteRecomputesAndPreservesStructure locks the round-trip:
// costs are corrected everywhere they appear, and nothing else in the file
// is disturbed.
func TestRepriceWriteRecomputesAndPreservesStructure(t *testing.T) {
	root := t.TempDir()
	path := writeManifest(t, root, manifestFixture)

	if _, err := Reprice(root, true); err != nil {
		t.Fatalf("Reprice: %v", err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	// Compare line by line rather than by substring. A `strings.Contains`
	// check passes even when the whole document has been re-indented or
	// re-ordered — and that is precisely the regression worth guarding,
	// because a reprice should be invisible in a diff apart from the figures
	// that actually moved.
	before := strings.Split(manifestFixture, "\n")
	after := strings.Split(got, "\n")
	if len(before) != len(after) {
		t.Fatalf("line count %d → %d; the document was reshaped:\n%s", len(before), len(after), got)
	}
	changed := 0
	for i := range before {
		if before[i] == after[i] {
			continue
		}
		changed++
		if !strings.Contains(after[i], "cost_usd:") {
			t.Errorf("line %d changed but is not a cost line:\n  before: %q\n  after:  %q", i, before[i], after[i])
		}
		if leadingSpace(before[i]) != leadingSpace(after[i]) {
			t.Errorf("line %d re-indented: %q → %q", i, before[i], after[i])
		}
	}
	// Four cost_usd fields exist in the fixture and all four must move:
	// totals, totals' model record, the step, and the step's model record.
	// Missing any one of them would leave the file internally inconsistent —
	// a corrected run total over stale per-step figures.
	if changed != 4 {
		t.Errorf("changed %d line(s), want 4 (totals, its model record, the step, the step's model record)", changed)
	}
	// Written the way ape's own manifest writer would: the shortest form of
	// the float, not a fixed-precision string.
	if !strings.Contains(got, "cost_usd: 5\n") {
		t.Errorf("cost not in ape's own float form (want `cost_usd: 5`):\n%s", got)
	}

	// Re-reading must now yield the corrected total, and a second pass must
	// be a no-op (idempotence — otherwise repeated runs drift).
	rep2, err := Reprice(root, true)
	if err != nil {
		t.Fatalf("second Reprice: %v", err)
	}
	if rep2.Changed != 0 || rep2.Written != 0 {
		t.Errorf("reprice is not idempotent: changed=%d written=%d", rep2.Changed, rep2.Written)
	}
	if !floatNear(rep2.OldTotal, 5.00) {
		t.Errorf("post-write total = %v, want 5.00", rep2.OldTotal)
	}
}

// TestRepriceLeavesUnpricedModelsAlone locks the honesty rule: repricing
// cannot invent a rate, so an unpriced model keeps its stored cost and is
// named in the report rather than being quietly zeroed again.
func TestRepriceLeavesUnpricedModelsAlone(t *testing.T) {
	root := t.TempDir()
	fixture := strings.ReplaceAll(manifestFixture, "claude-opus-5", "who-knows-model")
	writeManifest(t, root, fixture)

	rep, err := Reprice(root, false)
	if err != nil {
		t.Fatalf("Reprice: %v", err)
	}
	if len(rep.StillUnpriced) != 1 || rep.StillUnpriced[0] != "who-knows-model" {
		t.Errorf("StillUnpriced = %v, want [who-knows-model]", rep.StillUnpriced)
	}
	if rep.Changed != 0 {
		t.Errorf("changed=%d, want 0 — there is no rate to apply", rep.Changed)
	}
}

// TestRepriceNoArtefactsIsNotAnError — a project that has never run must
// report nothing rather than fail.
func TestRepriceNoArtefactsIsNotAnError(t *testing.T) {
	rep, err := Reprice(t.TempDir(), true)
	if err != nil {
		t.Fatalf("Reprice on an empty project: %v", err)
	}
	if rep.Scanned != 0 || rep.Changed != 0 {
		t.Errorf("empty project reported scanned=%d changed=%d", rep.Scanned, rep.Changed)
	}
}

// leadingSpace returns a line's indentation, so a re-indent is detectable.
func leadingSpace(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

// TestRepriceDedupesLatestSymlink locks the fix for a real double-count.
// Every pipeline and task name carries a `latest` symlink to its most recent
// run dir, so the manifest glob matched the same file twice and the preview
// reported twice the true delta — directly misleading the decision to --write.
func TestRepriceDedupesLatestSymlink(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, manifestFixture)
	linkParent := filepath.Join(root, "_output", "pipelines", "demo")
	if err := os.Symlink("20260720-120000-abc123", filepath.Join(linkParent, "latest")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	rep, err := Reprice(root, false)
	if err != nil {
		t.Fatalf("Reprice: %v", err)
	}
	if rep.Scanned != 1 {
		t.Errorf("scanned = %d, want 1 — the `latest` symlink was counted as a second artefact", rep.Scanned)
	}
	if len(rep.Files) != 1 {
		t.Errorf("reported %d file(s), want 1: %+v", len(rep.Files), rep.Files)
	}
	// 1M input tokens at $5/1M — not $10.
	if !floatNear(rep.NewTotal, 5.00) {
		t.Errorf("NewTotal = %v, want 5.00 (doubled means the symlink was folded in twice)", rep.NewTotal)
	}
}
