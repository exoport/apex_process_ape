package pipeline //nolint:testpackage // white-box tests touch unexported decoder helpers

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSpec_UnmarshalPipelineLevelDefaults verifies the new PLAN-6 / C2
// pipeline-level Model, Agent, and Commit fields decode from YAML.
func TestSpec_UnmarshalPipelineLevelDefaults(t *testing.T) {
	src := `
name: design
model: "opus[1m]"
agent: apex-agent-pm
commit: true
stages:
  create-prd:
    chain:
      - skill: apex-create-prd
`
	var spec Spec
	if err := yaml.Unmarshal([]byte(src), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if spec.Model != "opus[1m]" {
		t.Errorf("spec.Model = %q, want %q", spec.Model, "opus[1m]")
	}
	if spec.Agent != "apex-agent-pm" {
		t.Errorf("spec.Agent = %q, want %q", spec.Agent, "apex-agent-pm")
	}
	if spec.Commit == nil {
		t.Fatal("spec.Commit = nil, want non-nil")
	}
	if spec.Commit.Mode != CommitModeDefault {
		t.Errorf("spec.Commit.Mode = %v, want CommitModeDefault", spec.Commit.Mode)
	}
}

// TestStage_UnmarshalStageLevelDefaults verifies the new PLAN-6 / C2
// stage-level Model, Agent, and Commit fields decode from YAML.
func TestStage_UnmarshalStageLevelDefaults(t *testing.T) {
	src := `
name: design
stages:
  create-prd:
    model: "opus[1m]"
    agent: apex-agent-pm
    commit: "feat: PRD"
    chain:
      - skill: apex-create-prd
`
	var spec Spec
	if err := yaml.Unmarshal([]byte(src), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	stages, err := decodeStages(&spec.StagesRaw)
	if err != nil {
		t.Fatalf("decodeStages: %v", err)
	}
	if len(stages) != 1 {
		t.Fatalf("len(stages) = %d, want 1", len(stages))
	}
	st := stages[0]
	if st.Model != "opus[1m]" {
		t.Errorf("stage.Model = %q", st.Model)
	}
	if st.Agent != "apex-agent-pm" {
		t.Errorf("stage.Agent = %q", st.Agent)
	}
	if st.Commit == nil {
		t.Fatal("stage.Commit = nil, want non-nil")
	}
	if st.Commit.Mode != CommitModeExplicit || st.Commit.Message != "feat: PRD" {
		t.Errorf("stage.Commit = %+v, want Explicit/feat: PRD", *st.Commit)
	}
}

// TestStep_UnmarshalRecordsCommitSet covers Step.UnmarshalYAML's
// presence-tracking flag. PLAN-6 / C2 precedence uses CommitSet to
// distinguish "step opts into a step-boundary commit" from
// "step inherits".
func TestStep_UnmarshalRecordsCommitSet(t *testing.T) {
	cases := []struct {
		name      string
		yamlText  string
		wantSet   bool
		wantMode  CommitMode
		wantMsg   string
		wantClear bool
	}{
		{"omitted", "skill: foo\n", false, CommitModeDefault, "", false},
		{"true", "skill: foo\ncommit: true\n", true, CommitModeDefault, "", false},
		{"false", "skill: foo\ncommit: false\n", true, CommitModeSkip, "", false},
		{"explicit", "skill: foo\ncommit: \"docs: msg\"\n", true, CommitModeExplicit, "docs: msg", false},
		{"no-clear", "skill: foo\nno-clear: true\n", false, CommitModeDefault, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var step Step
			if err := yaml.Unmarshal([]byte(tc.yamlText), &step); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if step.CommitSet != tc.wantSet {
				t.Errorf("CommitSet = %v, want %v", step.CommitSet, tc.wantSet)
			}
			if step.Commit.Mode != tc.wantMode {
				t.Errorf("Commit.Mode = %v, want %v", step.Commit.Mode, tc.wantMode)
			}
			if step.Commit.Message != tc.wantMsg {
				t.Errorf("Commit.Message = %q, want %q", step.Commit.Message, tc.wantMsg)
			}
			if step.NoClear != tc.wantClear {
				t.Errorf("NoClear = %v, want %v", step.NoClear, tc.wantClear)
			}
		})
	}
}

// TestSpec_Effective_ModelAgentPrecedence covers PLAN-6 / C2 precedence
// for the Model and Agent fields: step > stage > pipeline > "".
func TestSpec_Effective_ModelAgentPrecedence(t *testing.T) {
	cases := []struct {
		name      string
		yamlText  string
		wantModel string
		wantAgent string
	}{
		{
			name: "pipeline-only",
			yamlText: `
name: design
model: "opus[1m]"
agent: apex-agent-pm
stages:
  s1:
    chain:
      - skill: foo
`,
			wantModel: "opus[1m]",
			wantAgent: "apex-agent-pm",
		},
		{
			name: "stage-overrides-pipeline",
			yamlText: `
name: design
model: "opus[1m]"
agent: apex-agent-pm
stages:
  s1:
    model: "sonnet[1m]"
    agent: apex-agent-ux-designer
    chain:
      - skill: foo
`,
			wantModel: "sonnet[1m]",
			wantAgent: "apex-agent-ux-designer",
		},
		{
			name: "step-overrides-stage-and-pipeline",
			yamlText: `
name: design
model: "opus[1m]"
agent: apex-agent-pm
stages:
  s1:
    model: "sonnet[1m]"
    agent: apex-agent-ux-designer
    chain:
      - skill: foo
        model: "haiku"
        agent: apex-agent-dev
`,
			wantModel: "haiku",
			wantAgent: "apex-agent-dev",
		},
		{
			name: "all-empty",
			yamlText: `
name: design
stages:
  s1:
    chain:
      - skill: foo
`,
			wantModel: "",
			wantAgent: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := mustLoadInline(t, tc.yamlText)
			model, agent, _, err := spec.Effective("s1", 0)
			if err != nil {
				t.Fatalf("Effective: %v", err)
			}
			if model != tc.wantModel {
				t.Errorf("model = %q, want %q", model, tc.wantModel)
			}
			if agent != tc.wantAgent {
				t.Errorf("agent = %q, want %q", agent, tc.wantAgent)
			}
		})
	}
}

// TestSpec_Effective_CommitPrecedence covers PLAN-6 / C2 commit
// precedence. The boundary matters as much as the directive: step-level
// opt-in fires at step boundary; stage- and pipeline-level fire at
// stage boundary.
func TestSpec_Effective_CommitPrecedence(t *testing.T) {
	cases := []struct {
		name         string
		yamlText     string
		wantBoundary CommitBoundary
		wantMode     CommitMode
		wantMsg      string
	}{
		{
			name: "nothing-set-no-commit",
			yamlText: `
name: design
stages:
  s1:
    chain:
      - skill: foo
`,
			wantBoundary: CommitBoundaryNone,
		},
		{
			name: "pipeline-true-stage-boundary",
			yamlText: `
name: design
commit: true
stages:
  s1:
    chain:
      - skill: foo
`,
			wantBoundary: CommitBoundaryStage,
			wantMode:     CommitModeDefault,
		},
		{
			name: "stage-overrides-pipeline-with-message",
			yamlText: `
name: design
commit: true
stages:
  s1:
    commit: "feat: PRD"
    chain:
      - skill: foo
`,
			wantBoundary: CommitBoundaryStage,
			wantMode:     CommitModeExplicit,
			wantMsg:      "feat: PRD",
		},
		{
			name: "stage-false-disables-pipeline",
			yamlText: `
name: design
commit: true
stages:
  s1:
    commit: false
    chain:
      - skill: foo
`,
			wantBoundary: CommitBoundaryNone,
		},
		{
			name: "step-explicit-overrides-stage-to-step-boundary",
			yamlText: `
name: design
commit: true
stages:
  s1:
    commit: "feat: stage"
    chain:
      - skill: foo
        commit: "feat: mid-chain"
`,
			wantBoundary: CommitBoundaryStep,
			wantMode:     CommitModeExplicit,
			wantMsg:      "feat: mid-chain",
		},
		{
			name: "step-true-overrides-to-step-boundary",
			yamlText: `
name: design
commit: true
stages:
  s1:
    chain:
      - skill: foo
        commit: true
`,
			wantBoundary: CommitBoundaryStep,
			wantMode:     CommitModeDefault,
		},
		{
			name: "step-false-suppresses-stage",
			yamlText: `
name: design
commit: true
stages:
  s1:
    commit: true
    chain:
      - skill: foo
        commit: false
`,
			wantBoundary: CommitBoundaryNone,
		},
		{
			name: "pipeline-false-stage-true-stage-wins",
			yamlText: `
name: design
commit: false
stages:
  s1:
    commit: true
    chain:
      - skill: foo
`,
			wantBoundary: CommitBoundaryStage,
			wantMode:     CommitModeDefault,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := mustLoadInline(t, tc.yamlText)
			_, _, eff, err := spec.Effective("s1", 0)
			if err != nil {
				t.Fatalf("Effective: %v", err)
			}
			if eff.Boundary != tc.wantBoundary {
				t.Errorf("Boundary = %v, want %v", eff.Boundary, tc.wantBoundary)
			}
			if tc.wantBoundary != CommitBoundaryNone {
				if eff.Directive.Mode != tc.wantMode {
					t.Errorf("Directive.Mode = %v, want %v", eff.Directive.Mode, tc.wantMode)
				}
				if eff.Directive.Message != tc.wantMsg {
					t.Errorf("Directive.Message = %q, want %q", eff.Directive.Message, tc.wantMsg)
				}
			}
		})
	}
}

// TestSpec_Effective_RejectsBadStageOrIndex guards the error-return
// paths.
func TestSpec_Effective_RejectsBadStageOrIndex(t *testing.T) {
	spec := mustLoadInline(t, `
name: design
stages:
  s1:
    chain:
      - skill: foo
`)
	if _, _, _, err := spec.Effective("missing", 0); err == nil || !strings.Contains(err.Error(), "unknown stage") {
		t.Errorf("expected 'unknown stage' error, got %v", err)
	}
	if _, _, _, err := spec.Effective("s1", 5); err == nil || !strings.Contains(err.Error(), "step index") {
		t.Errorf("expected 'step index' error, got %v", err)
	}
}

// TestSpec_BackwardCompat verifies pipelines authored against PLAN-4
// (step-only commit / model / agent) parse identically to before
// PLAN-6 / C2 — the existing design.yaml fixture is the contract.
func TestSpec_BackwardCompat(t *testing.T) {
	spec, err := LoadSpec("design", "testdata")
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if spec.Model != "" || spec.Agent != "" || spec.Commit != nil {
		t.Errorf("pipeline-level defaults should be empty; got model=%q agent=%q commit=%+v",
			spec.Model, spec.Agent, spec.Commit)
	}
	stages := spec.Stages()
	if len(stages) == 0 {
		t.Fatal("no stages parsed")
	}
	for _, st := range stages {
		if st.Model != "" || st.Agent != "" || st.Commit != nil {
			t.Errorf("stage %q: stage-level defaults should be empty; got model=%q agent=%q commit=%+v",
				st.Name, st.Model, st.Agent, st.Commit)
		}
		for i, step := range st.Chain {
			if step.CommitSet {
				t.Errorf("stage %q step %d: design.yaml has no commit: fields; CommitSet should be false", st.Name, i)
			}
		}
	}
}

// mustLoadInline parses inline YAML into a Spec for tests that don't
// need a file on disk. Reuses LoadSpec's stage-decoding path so the
// stageMap is populated.
func mustLoadInline(t *testing.T, src string) *Spec {
	t.Helper()
	var spec Spec
	if err := yaml.Unmarshal([]byte(src), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	stages, err := decodeStages(&spec.StagesRaw)
	if err != nil {
		t.Fatalf("decodeStages: %v", err)
	}
	spec.stages = stages
	spec.stageMap = make(map[string]*Stage, len(stages))
	for i := range spec.stages {
		spec.stageMap[spec.stages[i].Name] = &spec.stages[i]
	}
	return &spec
}
