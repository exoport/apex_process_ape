package pipeline //nolint:testpackage // tests white-box test unexported buildArgv; moving to pipeline_test would require exporting it

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestBuildArgv_PrependFlagsWebMode(t *testing.T) {
	// PLAN-5 / C1 — web mode prepends --strict-mcp-config + the two
	// inline blobs after argv[0] and before --dangerously-skip-permissions.
	prepend := []string{"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--settings", "{}"}
	got, err := buildArgv("claude", Step{Skill: "x"}, "", prepend)
	if err != nil {
		t.Fatalf("buildArgv: %v", err)
	}
	if got[0] != "claude" {
		t.Errorf("argv[0] = %q, want claude", got[0])
	}
	for i, want := range prepend {
		if got[1+i] != want {
			t.Errorf("argv[%d] = %q, want %q", 1+i, got[1+i], want)
		}
	}
	pos := 1 + len(prepend)
	if got[pos] != "--dangerously-skip-permissions" {
		t.Errorf("argv[%d] = %q, want --dangerously-skip-permissions", pos, got[pos])
	}
}

func TestBuildArgv_DirectSkill(t *testing.T) {
	got, err := buildArgv("claude", Step{Skill: "apex-shard-doc", Args: "--doc prd"}, "", nil)
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
	}, "", nil)
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
	got, err := buildArgv("claude", step, "", nil)
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
	got, err := buildArgv("claude", step, "minimal greeter — add settings page", nil)
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
	got, err := buildArgv("claude", step, "ignored", nil)
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
	_, err := buildArgv("claude", Step{}, "", nil)
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

// captureObserver records every Observer event into in-memory slices
// so tests can assert on the live event stream produced by runClaude.
type captureObserver struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureObserver) OnStageStart(_ string)                                                 {}
func (c *captureObserver) OnStageEnd(_ string, _ time.Duration, _ error)                         {}
func (c *captureObserver) OnStepStart(_ string, _ int, _ Step)                                   {} //nolint:gocritic // Step is passed by value to match Observer interface
func (c *captureObserver) OnStepEnd(_ string, _ int, _ Step, _ time.Duration, _ string, _ error) {} //nolint:gocritic // Step is passed by value to match Observer interface

func (c *captureObserver) OnStepLine(_ string, _ int, line string) {
	c.mu.Lock()
	c.lines = append(c.lines, line)
	c.mu.Unlock()
}

// TestRunClaude_StreamsLineByLine verifies that newline-delimited
// chunks from the subprocess's stdout reach the Observer via
// OnStepLine in order, and that the accumulated return value mirrors
// the full output exactly. The "claude" binary is replaced by a tiny
// shell script for hermeticity.
func TestRunClaude_StreamsLineByLine(t *testing.T) {
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("/bin/sh required: " + err.Error())
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "shim.sh")
	body := "#!/bin/sh\n" +
		"printf '%s\\n' '{\"type\":\"a\"}' '{\"type\":\"b\"}' '{\"type\":\"c\"}'\n" +
		"exit 0\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	obs := &captureObserver{}
	out, err := runClaude(context.Background(), []string{shim}, dir, obs, "stage1", 0, nil)
	if err != nil {
		t.Fatalf("runClaude: %v", err)
	}

	want := []string{`{"type":"a"}`, `{"type":"b"}`, `{"type":"c"}`}
	if !reflect.DeepEqual(obs.lines, want) {
		t.Errorf("OnStepLine sequence = %v, want %v", obs.lines, want)
	}
	const wantOut = "{\"type\":\"a\"}\n{\"type\":\"b\"}\n{\"type\":\"c\"}\n"
	if out != wantOut {
		t.Errorf("accumulated output = %q, want %q", out, wantOut)
	}
}

// TestRunClaude_InterleavesStderr verifies that lines written to the
// subprocess's stderr also reach the Observer (currently merged into
// the same stream as stdout). This guarantees error output isn't
// silently dropped while we wait for OnStepEnd.
func TestRunClaude_InterleavesStderr(t *testing.T) {
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("/bin/sh required: " + err.Error())
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "shim.sh")
	body := "#!/bin/sh\n" +
		"echo stdout-line\n" +
		"echo stderr-line 1>&2\n" +
		"exit 0\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	obs := &captureObserver{}
	if _, err := runClaude(context.Background(), []string{shim}, dir, obs, "stage", 0, nil); err != nil {
		t.Fatalf("runClaude: %v", err)
	}
	if !slices.Contains(obs.lines, "stdout-line") {
		t.Errorf("stdout line missing from OnStepLine, got %v", obs.lines)
	}
	if !slices.Contains(obs.lines, "stderr-line") {
		t.Errorf("stderr line missing from OnStepLine, got %v", obs.lines)
	}
}

// TestRunClaude_PropagatesNonZeroExit verifies that a failing
// subprocess returns the wait error AND still flushes its captured
// output to the caller (so callers can render the failure cause).
func TestRunClaude_PropagatesNonZeroExit(t *testing.T) {
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("/bin/sh required: " + err.Error())
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "shim.sh")
	body := "#!/bin/sh\necho about-to-fail\nexit 7\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	obs := &captureObserver{}
	out, err := runClaude(context.Background(), []string{shim}, dir, obs, "stage", 0, nil)
	if err == nil {
		t.Fatal("expected non-nil error from failing subprocess")
	}
	if !strings.Contains(out, "about-to-fail") {
		t.Errorf("accumulated output must include pre-exit content, got %q", out)
	}
	if !slices.Contains(obs.lines, "about-to-fail") {
		t.Errorf("OnStepLine must fire for output flushed before exit, got %v", obs.lines)
	}
}
