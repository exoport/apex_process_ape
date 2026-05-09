package pipeline_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/diegosz/apex_process_ape/internal/pipeline"
)

func TestPreflight_AllPresent(t *testing.T) {
	// governance preflight checks for the sharded directories
	// produced by apex-shard-doc, not the original docs.
	dir := t.TempDir()
	for _, rel := range []string{
		"development/planning/architecture",
		"development/planning/prd",
	} {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	spec, err := pipeline.LoadSpec("governance")
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.Preflight(spec, dir); err != nil {
		t.Fatalf("expected pass, got: %v", err)
	}
}

func TestPreflight_MissingFile(t *testing.T) {
	dir := t.TempDir()
	spec, err := pipeline.LoadSpec("governance")
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
	spec, err := pipeline.LoadSpec("design")
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.Preflight(spec, dir); err != nil {
		t.Errorf("design has no requires.files; expected pass, got: %v", err)
	}
}
