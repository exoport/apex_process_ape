package pipeline //nolint:testpackage // tests white-box test unexported buildArgv; moving to pipeline_test would require exporting it

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const (
	testAgentPM    = "apex-agent-pm"
	testPromptFlag = "--prompt"
)

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

// promptOf returns the value of the -p flag in argv, plus the trailing
// flags (after the prompt) joined by spaces. Lets tests assert on the
// inner prompt string and the outer claude flags separately.
func promptOf(t *testing.T, argv []string) (prompt string, tail []string) {
	t.Helper()
	for i, a := range argv {
		if a == "-p" && i+1 < len(argv) {
			return argv[i+1], argv[i+2:]
		}
	}
	t.Fatalf("argv missing -p: %v", argv)
	return "", nil
}

func TestBuildArgv_DirectSkill(t *testing.T) {
	got, err := buildArgv("claude", Step{Skill: "apex-shard-doc", Args: "--doc prd"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0] != "claude" || got[1] != "--dangerously-skip-permissions" {
		t.Fatalf("outer argv head wrong: %v", got)
	}
	prompt, tail := promptOf(t, got)
	wantPrompt := "/apex-shard-doc --autonomous --no-commit --doc prd"
	if prompt != wantPrompt {
		t.Errorf("prompt mismatch:\n  got:  %q\n  want: %q", prompt, wantPrompt)
	}
	wantTail := []string{flagOutputFormat, flagOutputStreamJSON, flagVerbose}
	if !reflect.DeepEqual(tail, wantTail) {
		t.Errorf("tail mismatch:\n  got:  %v\n  want: %v", tail, wantTail)
	}
}

func TestBuildArgv_AgentPassthrough(t *testing.T) {
	got, err := buildArgv("claude", Step{
		Skill: "apex-create-prd",
		Agent: testAgentPM,
		Model: "opus[1m]",
	}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prompt, tail := promptOf(t, got)
	wantPrompt := "/apex-agent-pm --autonomous -- apex-create-prd --autonomous"
	if prompt != wantPrompt {
		t.Errorf("prompt mismatch:\n  got:  %q\n  want: %q", prompt, wantPrompt)
	}
	wantTail := []string{flagOutputFormat, flagOutputStreamJSON, flagVerbose, "--model", "opus[1m]"}
	if !reflect.DeepEqual(tail, wantTail) {
		t.Errorf("tail mismatch:\n  got:  %v\n  want: %v", tail, wantTail)
	}
}

func TestBuildArgv_PromptFlag_NoPrompt(t *testing.T) {
	step := Step{
		Skill:      "apex-create-epics-and-stories",
		Agent:      testAgentPM,
		PromptFlag: testPromptFlag,
	}
	got, err := buildArgv("claude", step, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prompt, _ := promptOf(t, got)
	wantPrompt := "/apex-agent-pm --autonomous -- apex-create-epics-and-stories --autonomous"
	if prompt != wantPrompt {
		t.Errorf("prompt mismatch:\n  got:  %q\n  want: %q", prompt, wantPrompt)
	}
}

func TestBuildArgv_PromptFlag_WithPrompt(t *testing.T) {
	step := Step{
		Skill:      "apex-create-epics-and-stories",
		Agent:      testAgentPM,
		PromptFlag: testPromptFlag,
	}
	got, err := buildArgv("claude", step, "minimal greeter — add settings page")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prompt, _ := promptOf(t, got)
	wantPrompt := "/apex-agent-pm --autonomous -- apex-create-epics-and-stories --autonomous --prompt minimal greeter — add settings page"
	if prompt != wantPrompt {
		t.Errorf("prompt mismatch:\n  got:  %q\n  want: %q", prompt, wantPrompt)
	}
}

func TestBuildArgv_PromptFlag_IgnoredWhenAbsent(t *testing.T) {
	// Step has no prompt_flag — runtime prompt is silently ignored.
	step := Step{Skill: "apex-shard-doc", Args: "--doc prd"}
	got, err := buildArgv("claude", step, "ignored")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prompt, _ := promptOf(t, got)
	wantPrompt := "/apex-shard-doc --autonomous --no-commit --doc prd"
	if prompt != wantPrompt {
		t.Errorf("prompt mismatch:\n  got:  %q\n  want: %q", prompt, wantPrompt)
	}
}

func TestBuildArgv_EmptySkill(t *testing.T) {
	_, err := buildArgv("claude", Step{}, "")
	if err == nil {
		t.Fatal("expected error when skill is empty")
	}
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
