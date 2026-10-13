package pipeline //nolint:testpackage // white-box: reads the unexported skip plan helpers

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// extProject materializes a project holding the extgates fixture and, when
// enabled is non-nil, an `_apex/config.yaml` enabling exactly those ids.
func extProject(t *testing.T, enabled []string) string {
	t.Helper()
	root := t.TempDir()
	dst := filepath.Join(root, "_apex", "pipelines")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("testdata", "extensions", "_apex", "pipelines", "extgates.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "extgates.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if enabled != nil {
		writeExtConfig(t, root, enabled)
	}
	return root
}

func writeExtConfig(t *testing.T, root string, enabled []string) {
	t.Helper()
	body := "project_name: ext-test\nextensions: [" + strings.Join(enabled, ", ") + "]\n"
	if err := os.WriteFile(filepath.Join(root, "_apex", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func missingOf(s *Skip) []string {
	if s == nil {
		return nil
	}
	return s.Missing
}

func TestPlanSkips_Extgates(t *testing.T) {
	root := extProject(t, nil)
	spec, err := LoadSpec("extgates", root)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if !spec.HasExtensionGates() {
		t.Fatal("HasExtensionGates = false")
	}
	plan := spec.PlanSkips(NewExtensions([]string{"ext-adrs", "ext-capabilities"}, false))

	if plan["prep"].Stage != nil || plan["adrs"].Stage != nil {
		t.Errorf("prep/adrs must run: %+v %+v", plan["prep"].Stage, plan["adrs"].Stage)
	}
	if got := missingOf(plan["patterns-only"].Stage); !slices.Equal(got, []string{"ext-patterns"}) {
		t.Errorf("patterns-only missing = %v", got)
	}
	for i, s := range plan["patterns-only"].Steps {
		if s == nil {
			t.Errorf("patterns-only step %d not marked skipped", i)
		}
	}

	refresh := plan["refresh"]
	if refresh.Stage != nil {
		t.Errorf("refresh must run: %+v", refresh.Stage)
	}
	if got := missingOf(refresh.Steps[0]); !slices.Equal(got, []string{"ext-features"}) {
		t.Errorf("refresh step 0 missing = %v", got)
	}
	if refresh.Steps[1] != nil || refresh.firstRun() != 1 || refresh.lastRun() != 1 {
		t.Errorf("refresh: step 1 must be the only one that runs (first %d, last %d)", refresh.firstRun(), refresh.lastRun())
	}
	if tail := plan["tail-skip"]; tail.lastRun() != 0 {
		t.Errorf("tail-skip lastRun = %d, want 0", tail.lastRun())
	}

	// Every step skipped by its own gate: the stage is skipped, and says
	// which ids it would have needed — the union, in first-seen order.
	if got := missingOf(plan["all-gated"].Stage); !slices.Equal(got, []string{"ext-patterns", "ext-features"}) {
		t.Errorf("all-gated missing = %v", got)
	}
}

func TestPlanSkips_IgnoredAndNil(t *testing.T) {
	root := extProject(t, nil)
	spec, err := LoadSpec("extgates", root)
	if err != nil {
		t.Fatal(err)
	}
	for name, ext := range map[string]*Extensions{
		"ignored": NewExtensions(nil, true),
		"nil":     nil,
	} {
		for stage, p := range spec.PlanSkips(ext) {
			if p.Stage != nil || slices.ContainsFunc(p.Steps, func(s *Skip) bool { return s != nil }) {
				t.Errorf("%s: stage %s has a skip", name, stage)
			}
		}
	}
}

func TestResolveFromStage(t *testing.T) {
	root := extProject(t, nil)
	spec, err := LoadSpec("extgates", root)
	if err != nil {
		t.Fatal(err)
	}
	plan := spec.PlanSkips(NewExtensions([]string{"ext-adrs", "ext-capabilities"}, false))
	cases := []struct {
		from, started string
		skipped       bool
	}{
		{"prep", "prep", false},
		{"patterns-only", "adrs", true},
		{"refresh", "refresh", false},
		{"all-gated", "", true},
	}
	for _, c := range cases {
		started, skipped := spec.ResolveFromStage(c.from, plan)
		if started != c.started || skipped != c.skipped {
			t.Errorf("--from %s: got (%q, %v), want (%q, %v)", c.from, started, skipped, c.started, c.skipped)
		}
	}
}

func TestLoadSpec_RejectsUnknownExtensionIDs(t *testing.T) {
	cases := map[string]string{
		"stage gate": "stages:\n  s:\n    requires-extensions: [ext-pattern]\n    chain:\n      - skill: a\n",
		"step gate":  "stages:\n  s:\n    chain:\n      - skill: a\n        requires-extensions: [ext-adr]\n",
		"conditional file": "requires:\n  conditional-files:\n    - path: x\n      extensions-any: [ext-feature]\n" +
			"stages:\n  s:\n    chain:\n      - skill: a\n",
		"conditional file without ids": "requires:\n  conditional-files:\n    - path: x\n" +
			"stages:\n  s:\n    chain:\n      - skill: a\n",
		"conditional file without path": "requires:\n  conditional-files:\n    - extensions-any: [ext-adrs]\n" +
			"stages:\n  s:\n    chain:\n      - skill: a\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "_apex", "pipelines")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("name: bad\n"+body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadSpec("bad", root)
			if err == nil || !strings.Contains(err.Error(), "ext-adrs, ext-patterns, ext-capabilities, ext-features") {
				t.Fatalf("LoadSpec error = %v, want an invalid-gate error naming the known ids", err)
			}
		})
	}
}

func TestLoadSpec_ExtensionKeysAreKnown(t *testing.T) {
	root := extProject(t, nil)
	spec, err := LoadSpec("extgates", root)
	if err != nil {
		t.Fatal(err)
	}
	if w := spec.UnknownKeyWarnings(); len(w) > 0 {
		t.Errorf("requires-extensions / conditional-files reported as unknown: %+v", w)
	}
}

func TestPreflight_ConditionalFiles(t *testing.T) {
	root := extProject(t, nil)
	spec, err := LoadSpec("extgates", root)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	missing := func(ext *Extensions) []string {
		err := Preflight(spec, root, ext)
		if err == nil {
			return nil
		}
		var pfe *PreflightError
		if !errors.As(err, &pfe) {
			t.Fatalf("Preflight error %T: %v", err, err)
		}
		return pfe.Missing
	}

	// Nothing enabled: only the unconditional file is required.
	if got := missing(NewExtensions(nil, false)); !slices.Equal(got, []string{"always.md"}) {
		t.Errorf("no extensions: missing = %v", got)
	}
	// Capabilities on: functionality is required too, and both are
	// reported in one error.
	if got := missing(NewExtensions([]string{"ext-capabilities"}, false)); !slices.Equal(got, []string{"always.md", "development/functionality"}) {
		t.Errorf("capabilities: missing = %v", got)
	}
	seed("always.md")
	seed("development/functionality/x.md")
	if got := missing(NewExtensions([]string{"ext-capabilities"}, false)); got != nil {
		t.Errorf("capabilities, all present: missing = %v", got)
	}
	// --ignore-extensions checks every conditional file.
	if got := missing(NewExtensions(nil, true)); !slices.Equal(got, []string{"development/patterns-only"}) {
		t.Errorf("ignored: missing = %v", got)
	}
}

func TestExtensionsFor(t *testing.T) {
	t.Run("gated spec without config refuses", func(t *testing.T) {
		root := extProject(t, nil)
		spec, err := LoadSpec("extgates", root)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ExtensionsFor(spec, root, false)
		if err == nil || !strings.Contains(err.Error(), "--ignore-extensions") {
			t.Fatalf("err = %v, want a refusal that names --ignore-extensions", err)
		}
		ext, err := ExtensionsFor(spec, root, true)
		if err != nil || ext == nil || !ext.Ignored {
			t.Fatalf("ignored: ext=%+v err=%v", ext, err)
		}
	})
	t.Run("ungated spec without config runs ungated", func(t *testing.T) {
		spec := NewSingleStepSpec("task", Step{Skill: "a"}, nil)
		ext, err := ExtensionsFor(spec, t.TempDir(), false)
		if err != nil || ext != nil {
			t.Fatalf("ext=%+v err=%v, want nil, nil", ext, err)
		}
	})
	t.Run("config overlay is honoured", func(t *testing.T) {
		root := extProject(t, []string{"ext-adrs"})
		local := "extensions: [ext-patterns]\n"
		if err := os.WriteFile(filepath.Join(root, "_apex", "config.local.yaml"), []byte(local), 0o644); err != nil {
			t.Fatal(err)
		}
		spec, err := LoadSpec("extgates", root)
		if err != nil {
			t.Fatal(err)
		}
		ext, err := ExtensionsFor(spec, root, false)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(ext.Enabled, []string{"ext-patterns"}) {
			t.Errorf("enabled = %v, want the overlay's", ext.Enabled)
		}
	})
}
