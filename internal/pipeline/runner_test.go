package pipeline //nolint:testpackage // white-box: shares unexported test helpers (stubSpecSkills, setupTestProject) with sibling _test.go files

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const (
	testAgentPM     = "apex-agent-pm"
	testPromptFlag  = "--prompt"
	testModelOpus1M = "opus[1m]"
)

// stubSpecSkills writes empty SKILL.md files under
// <root>/.claude/skills/<name>/ for every skill and agent referenced by
// the spec's stage chains. Required because PreflightSkills (called at
// the top of Run) refuses to launch when a referenced skill has no
// SKILL.md on disk — production behaviour we want, but every TestRun_*
// in this package uses synthetic skill names like "apex-fake" or
// "step-one" against a claude shim, so the stubs satisfy the check
// without exercising the framework.
func stubSpecSkills(t *testing.T, root string, spec *Spec) {
	t.Helper()
	seen := make(map[string]bool)
	for _, stage := range spec.Stages() {
		for _, step := range stage.Chain {
			for _, name := range []string{step.Skill, step.Agent} {
				if name == "" || seen[name] {
					continue
				}
				seen[name] = true
				dir := filepath.Join(root, ".claude", "skills", name)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("stub skill dir %s: %v", dir, err)
				}
				if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("stub\n"), 0o644); err != nil {
					t.Fatalf("stub SKILL.md for %s: %v", name, err)
				}
			}
		}
	}
	// Commit the stubs when the test has already initialized a git
	// repo, so they don't show up as untracked files and trip the
	// dirty-tree gate that the commit-flow tests rely on. Tests that
	// don't init git skip this branch silently.
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		for _, args := range [][]string{
			{"add", ".claude"},
			{
				"-c", "user.email=ape-test@example.com", "-c", "user.name=ape test",
				"commit", "-m", "test: stub skill SKILL.md files", "--quiet",
			},
		} {
			cmd := exec.Command("git", args...)
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
	}
}

// setupTestProject materializes a fake project root with the canonical
// three pipeline yamls under <root>/_apex/pipelines/. Sourced from
// internal/pipeline/testdata/_apex/pipelines/. Returns the root.
func setupTestProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
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
	return root
}

func TestLoadSpec_Design(t *testing.T) {
	root := setupTestProject(t)
	spec, err := LoadSpec("design", root)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if spec.Name != "design" {
		t.Errorf("name: got %q, want design", spec.Name)
	}
	stages := spec.Stages()
	wantStages := []string{
		"create-prd", "shard-prd", "create-ux-design",
		"shard-ux-design", "create-architecture", "shard-architecture",
	}
	if len(stages) != len(wantStages) {
		t.Fatalf("stage count: got %d, want %d", len(stages), len(wantStages))
	}
	for i, want := range wantStages {
		if stages[i].Name != want {
			t.Errorf("stage[%d]: got %q, want %q", i, stages[i].Name, want)
		}
	}
}

func TestLoadSpec_Governance_HasRequires(t *testing.T) {
	root := setupTestProject(t)
	spec, err := LoadSpec("governance", root)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if len(spec.Requires.Files) == 0 {
		t.Fatal("governance pipeline must declare requires.files")
	}
	wantPath := "development/planning/architecture"
	if !slices.Contains(spec.Requires.Files, wantPath) {
		t.Errorf("requires.files missing %q; got %v", wantPath, spec.Requires.Files)
	}
}

func TestLoadSpec_Epics_PromptFlagWired(t *testing.T) {
	root := setupTestProject(t)
	spec, err := LoadSpec("epics", root)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	stages := spec.Stages()
	if len(stages) == 0 {
		t.Fatal("expected at least one stage")
	}
	first := stages[0]
	if len(first.Chain) == 0 {
		t.Fatal("expected a chain on first stage")
	}
	if first.Chain[0].PromptFlag != testPromptFlag {
		t.Errorf("expected create-epics step to declare prompt_flag=--prompt, got %q",
			first.Chain[0].PromptFlag)
	}
}

func TestLoadSpec_Unknown(t *testing.T) {
	root := setupTestProject(t)
	_, err := LoadSpec("does-not-exist", root)
	if err == nil {
		t.Fatal("expected error for unknown pipeline")
	}
}

// TestLoadSpec_MissingFile_ActionableError guards the user-facing error
// string: a missing pipeline file must name the resolved path and tell
// the user how to fix it (`ape framework update`). This is the contract
// users will see on a v0.1.0 fresh install before they run framework
// update; the wording is a regression target.
func TestLoadSpec_MissingFile_ActionableError(t *testing.T) {
	root := t.TempDir() // empty project: no _apex/pipelines/ at all
	_, err := LoadSpec("design", root)
	if err == nil {
		t.Fatal("expected error for missing pipeline file")
	}
	msg := err.Error()
	wantSubstrings := []string{
		`pipeline "design"`,
		filepath.Join(root, "_apex", "pipelines", "design.yaml"),
		`ape framework update`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q; got: %s", want, msg)
		}
	}
}

func TestAvailablePipelines(t *testing.T) {
	root := setupTestProject(t)
	got := AvailablePipelines(root)
	want := []string{"design", "epics", "governance"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AvailablePipelines = %v, want %v", got, want)
	}
}

// TestAvailablePipelines_MissingDir verifies the soft-fail contract:
// when <projectRoot>/_apex/pipelines/ does not exist, AvailablePipelines
// returns an empty slice rather than erroring. Callers (cobra completion,
// help text) rely on this.
func TestAvailablePipelines_MissingDir(t *testing.T) {
	root := t.TempDir() // no _apex/pipelines/
	got := AvailablePipelines(root)
	if len(got) != 0 {
		t.Errorf("AvailablePipelines on empty project = %v, want empty slice", got)
	}
}
