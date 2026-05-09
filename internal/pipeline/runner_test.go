package pipeline //nolint:testpackage // tests white-box test unexported buildArgv; moving to pipeline_test would require exporting it

import (
	"reflect"
	"slices"
	"testing"
)

const (
	testAgentPM    = "apex-agent-pm"
	testPromptFlag = "--prompt"
)

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
	spec, err := LoadSpec("design")
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
	spec, err := LoadSpec("governance")
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
	spec, err := LoadSpec("epics")
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
	_, err := LoadSpec("does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown pipeline")
	}
}

func TestAvailablePipelines(t *testing.T) {
	got := AvailablePipelines()
	want := []string{"design", "epics", "governance"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AvailablePipelines = %v, want %v", got, want)
	}
}
