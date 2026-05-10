package pipeline_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/diegosz/apex_process_ape/internal/pipeline"
)

// installPipelines materializes the canonical three pipeline yamls
// under <root>/_apex/pipelines/ so LoadSpec can find them. Mirrors the
// in-package helper in runner_test.go but is duplicated here because
// preflight_test is an external test package (pipeline_test).
func installPipelines(t *testing.T, root string) {
	t.Helper()
	dst := filepath.Join(root, "_apex", "pipelines")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dst, err)
	}
	for _, name := range []string{"design", "governance", "epics"} {
		src := filepath.Join("testdata", "_apex", "pipelines", name+".yaml")
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read fixture %s: %v", src, err)
		}
		if err := os.WriteFile(filepath.Join(dst, name+".yaml"), data, 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
}

func TestPreflight_AllPresent(t *testing.T) {
	// governance preflight checks for the sharded directories
	// produced by apex-shard-doc, not the original docs.
	dir := t.TempDir()
	installPipelines(t, dir)
	for _, rel := range []string{
		"development/planning/architecture",
		"development/planning/prd",
	} {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	spec, err := pipeline.LoadSpec("governance", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.Preflight(spec, dir); err != nil {
		t.Fatalf("expected pass, got: %v", err)
	}
}

func TestPreflight_MissingFile(t *testing.T) {
	dir := t.TempDir()
	installPipelines(t, dir)
	spec, err := pipeline.LoadSpec("governance", dir)
	if err != nil {
		t.Fatal(err)
	}
	err = pipeline.Preflight(spec, dir)
	if err == nil {
		t.Fatal("expected error")
	}
	var pfe *pipeline.PreflightError
	if !errors.As(err, &pfe) {
		t.Fatalf("expected *PreflightError, got %T: %v", err, err)
	}
	if pfe.Pipeline != "governance" {
		t.Errorf("pipeline: got %q, want governance", pfe.Pipeline)
	}
	if len(pfe.Missing) == 0 {
		t.Error("expected at least one missing path")
	}
}

func TestPreflight_NoRequires(t *testing.T) {
	dir := t.TempDir()
	installPipelines(t, dir)
	spec, err := pipeline.LoadSpec("design", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.Preflight(spec, dir); err != nil {
		t.Errorf("design has no requires.files; expected pass, got: %v", err)
	}
}
