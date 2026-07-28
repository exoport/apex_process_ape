package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSpec drops a spec named "demo" under <root>/_apex/pipelines/.
func writeSpec(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, "_apex", "pipelines")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestEffectiveCanonicalizesModelSpelling locks the spec half of model-name
// resilience. A `model:` field is written by hand at three levels, so the
// same model arrives spelled several ways; all of them must produce an
// argument claude accepts.
//
// A bare family word resolves to the CURRENT generation of that family, so a
// spec can say `model: sonnet` and get a concrete, recorded model id. The
// staleness that pinning implies is covered by the alias-drift check in
// `ape costs coverage` / `ape doctor`, which fails the release gate when the
// table names a generation the local Claude Code is not running.
func TestEffectiveCanonicalizesModelSpelling(t *testing.T) {
	root := t.TempDir()
	writeSpec(t, root, `
name: demo
model: Claude_Opus_5
stages:
  alpha:
    chain:
      - skill: a
      - skill: b
        model: claude-sonnet
      - skill: c
        model: "claude-sonnet-4.6"
      - skill: d
        model: "Opus[1m]"
      - skill: e
        model: sonnet-5
`)
	spec, err := LoadSpec("demo", root)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}

	want := []string{
		"claude-opus-5",     // pipeline-level default, punctuation + case folded
		"claude-sonnet-5",   // `claude-sonnet` → the family's current generation
		"claude-sonnet-4-6", // dotted generation folded onto the canonical id
		"claude-opus-5[1m]", // bare family resolved, context suffix preserved
		"claude-sonnet-5",   // explicit generation stays as written
	}
	for i, w := range want {
		model, _, _, _, effErr := spec.Effective("alpha", i)
		if effErr != nil {
			t.Fatalf("Effective(alpha,%d): %v", i, effErr)
		}
		if model != w {
			t.Errorf("step %d model = %q, want %q", i, model, w)
		}
	}
}

// TestEffectivePassesUnknownModelThrough locks that ape does not become the
// thing that blocks a new model: an id this binary cannot attribute reaches
// claude unchanged (punctuation folded), and claude gives the verdict.
func TestEffectivePassesUnknownModelThrough(t *testing.T) {
	root := t.TempDir()
	writeSpec(t, root, `
name: demo
stages:
  alpha:
    chain:
      - skill: a
        model: claude-newthing-9
      - skill: b
        model: some-other-vendor-model
`)
	spec, err := LoadSpec("demo", root)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	for i, w := range []string{"claude-newthing-9", "some-other-vendor-model"} {
		model, _, _, _, effErr := spec.Effective("alpha", i)
		if effErr != nil {
			t.Fatalf("Effective(alpha,%d): %v", i, effErr)
		}
		if model != w {
			t.Errorf("step %d model = %q, want %q passed through unchanged", i, model, w)
		}
	}
}

// TestEffectiveEmptyModelStaysEmpty — no `model:` anywhere must not
// materialize one, or every step would start pinning a model the spec never
// asked for and claude's own default would become unreachable.
func TestEffectiveEmptyModelStaysEmpty(t *testing.T) {
	root := t.TempDir()
	writeSpec(t, root, `
name: demo
stages:
  alpha:
    chain:
      - skill: a
`)
	spec, err := LoadSpec("demo", root)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	model, _, _, _, effErr := spec.Effective("alpha", 0) //nolint:dogsled // only the model matters here
	if effErr != nil {
		t.Fatalf("Effective: %v", effErr)
	}
	if model != "" {
		t.Errorf("model = %q, want empty so claude uses its own default", model)
	}
}

// TestModelWarningsFlagsUnrecognizedSpecModels closes the asymmetry between a
// typo on the command line (warned immediately) and the same typo in a
// checked-in spec (previously silent until claude rejected it mid-run).
func TestModelWarningsFlagsUnrecognizedSpecModels(t *testing.T) {
	root := t.TempDir()
	writeSpec(t, root, `
name: demo
model: claude-sonet-5
stages:
  alpha:
    model: gpt-4
    chain:
      - skill: a
      - skill: b
        model: sonnet
      - skill: c
        model: claude-opus-9
`)
	spec, err := LoadSpec("demo", root)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	warnings := spec.ModelWarnings()
	got := map[string]string{}
	for _, w := range warnings {
		got[w.Model] = w.Location
	}
	// Two unrecognized values: a typo and a non-Claude id.
	for _, want := range []string{"claude-sonet-5", "gpt-4"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%q not reported; warnings = %+v", want, warnings)
		}
	}
	// A bare family and a not-yet-tabled generation of a KNOWN family are
	// both recognized — warning on them would train people to ignore this.
	for _, quiet := range []string{"sonnet", "claude-opus-9"} {
		if loc, ok := got[quiet]; ok {
			t.Errorf("%q reported at %s but ape recognizes it", quiet, loc)
		}
	}
	if len(warnings) != 2 {
		t.Errorf("got %d warnings, want exactly 2: %+v", len(warnings), warnings)
	}
	// The location must be specific enough to find the line.
	if loc := got["gpt-4"]; !strings.Contains(loc, "alpha") {
		t.Errorf("stage-level warning location = %q, want it to name the stage", loc)
	}
}

// TestModelWarningsSilentOnCleanSpec — a healthy spec must produce nothing.
func TestModelWarningsSilentOnCleanSpec(t *testing.T) {
	root := t.TempDir()
	writeSpec(t, root, `
name: demo
model: opus
stages:
  alpha:
    chain:
      - skill: a
        model: claude-sonnet-4-6
      - skill: b
`)
	spec, err := LoadSpec("demo", root)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if w := spec.ModelWarnings(); len(w) != 0 {
		t.Errorf("clean spec produced warnings: %+v", w)
	}
}
