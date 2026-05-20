// Package pipeline implements the ape pipeline runner: it loads named
// pipeline specifications from <projectRoot>/_apex/pipelines/, validates
// prerequisites, and drives the underlying claude CLI through each
// stage's skill chain.
package pipeline

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec describes a named pipeline.
//
// PLAN-6 / C2 adds pipeline-level defaults for Model, Agent, and Commit.
// These cascade down to stages and steps via the precedence chain
// `step > stage > pipeline > default` (see Effective). The fields are
// optional; absence at the pipeline level just means the chain falls
// through to the stage/step levels with no contribution.
type Spec struct {
	Name string `yaml:"name"`
	// Model is the pipeline-level default model passed to claude when a
	// step does not override at its own or its stage's level. Empty
	// string means "no pipeline-level default".
	Model string `yaml:"model,omitempty"`
	// Agent is the pipeline-level default agent. Same precedence rules
	// as Model.
	Agent string `yaml:"agent,omitempty"`
	// Commit is the pipeline-level commit policy. Nil means "absent"
	// (no pipeline-level opinion); non-nil values propagate to stages
	// that don't override. Phase A does not yet wire this into the
	// runner — the stage-boundary default lights up in Phase D.
	Commit    *CommitDirective  `yaml:"commit,omitempty"`
	Requires  Requires          `yaml:"requires,omitempty"`
	StagesRaw yaml.Node         `yaml:"stages"` //nolint:tagliatelle // on-disk YAML files use "stages"; field name includes "Raw" suffix to signal internal use
	stages    []Stage           `yaml:"-"`
	stageMap  map[string]*Stage `yaml:"-"`
}

// Requires lists pre-flight conditions for a pipeline.
type Requires struct {
	Files []string `yaml:"files,omitempty"`
}

// Stage is one logical step inside a pipeline. A stage executes a chain
// of skill steps in order. Stage boundaries are what the TUI displays
// as top-level rows.
//
// PLAN-6 / C2: Stage carries optional defaults for Model, Agent, and
// Commit that override the pipeline level and feed into the precedence
// chain consumed by Effective().
type Stage struct {
	Name string
	// Model overrides the pipeline-level model for steps within this
	// stage. Steps may further override with their own Model.
	Model string
	// Agent overrides the pipeline-level agent for steps within this
	// stage. Steps may further override with their own Agent.
	Agent string
	// Commit is the stage-level commit policy. Nil means "absent"
	// (inherit from the pipeline level). When set, fires at the
	// stage boundary by default; step-level Commit is the escape
	// hatch for mid-chain commits (see Effective).
	Commit *CommitDirective
	Chain  []Step
}

// Step is one invocation inside a stage's chain.
type Step struct {
	Skill string `yaml:"skill"`
	Agent string `yaml:"agent,omitempty"`
	Model string `yaml:"model,omitempty"`
	// Args is a whitespace-separated string of literal CLI flags.
	// Use this for fixed flags like "--from-status draft".
	Args string `yaml:"args,omitempty"`
	// PromptFlag, when set together with the runner's Prompt option,
	// appends the flag name + the prompt value as two argv elements.
	// This is how the user-supplied --prompt reaches the underlying
	// skill (currently apex-create-epics-and-stories). Passing the
	// prompt as a structured argv element avoids any shell-quoting
	// round-trip — argv is never serialized to a shell string.
	PromptFlag string `yaml:"prompt_flag,omitempty"` //nolint:tagliatelle // on-disk spec YAML files use snake_case prompt_flag
	// Commit configures the per-step commit boundary (PLAN-4). Omit
	// to inherit the pipeline-level default (commit with a derived
	// message); `commit: false` to skip; `commit: "msg"` to override
	// the message. See CommitDirective.
	//
	// PLAN-6 / C2: when CommitSet is true the step explicitly opted
	// into a step-boundary commit; when false the stage/pipeline level
	// applies and the commit (if any) fires at stage boundary instead.
	// PLAN-4 / runner.go:628 still treats the zero value as
	// CommitModeDefault, so today's behaviour is preserved until
	// Phase D rewires the runner.
	Commit CommitDirective `yaml:"commit,omitempty"`
	// CommitSet is true when the step's YAML had an explicit `commit:`
	// field. Populated by Step.UnmarshalYAML; zero-valued in unit tests
	// that construct Step literals directly. PLAN-6 precedence uses
	// this to distinguish "step opts in" from "step inherits".
	CommitSet bool `yaml:"-"`
	// NoClear opts the step out of the per-step `/clear` that the
	// bridge step contract (PLAN-6 / C4) enforces by default. Used by
	// multi-step chains that need to share context within a stage
	// (e.g., apex-create-prd's elicit/respond loop). Step-level only.
	NoClear bool `yaml:"no-clear,omitempty"` //nolint:tagliatelle // on-disk spec YAML uses kebab-case "no-clear"
}

// stepYAML mirrors Step for YAML decoding so Step.UnmarshalYAML can
// detect which optional fields were present without re-declaring
// every tag. Keep in sync with Step.
type stepYAML struct {
	Skill      string           `yaml:"skill"`
	Agent      string           `yaml:"agent,omitempty"`
	Model      string           `yaml:"model,omitempty"`
	Args       string           `yaml:"args,omitempty"`
	PromptFlag string           `yaml:"prompt_flag,omitempty"` //nolint:tagliatelle // matches Step.PromptFlag
	Commit     *CommitDirective `yaml:"commit,omitempty"`
	NoClear    bool             `yaml:"no-clear,omitempty"` //nolint:tagliatelle // matches Step.NoClear
}

// UnmarshalYAML decodes a Step and records whether the optional
// `commit:` field was present. The presence flag is needed by
// Spec.Effective (PLAN-6 / C2 precedence) to distinguish
// "step opts in to a step-boundary commit" from
// "step inherits from stage/pipeline".
func (s *Step) UnmarshalYAML(node *yaml.Node) error {
	var raw stepYAML
	if err := node.Decode(&raw); err != nil {
		return err
	}
	s.Skill = raw.Skill
	s.Agent = raw.Agent
	s.Model = raw.Model
	s.Args = raw.Args
	s.PromptFlag = raw.PromptFlag
	s.NoClear = raw.NoClear
	if raw.Commit != nil {
		s.Commit = *raw.Commit
		s.CommitSet = true
	} else {
		s.Commit = CommitDirective{}
		s.CommitSet = false
	}
	return nil
}

// Stages returns the pipeline's stages in declaration order.
func (s *Spec) Stages() []Stage {
	return s.stages
}

// PipelinesDir returns the absolute path of the pipelines directory
// inside a project root: <projectRoot>/_apex/pipelines.
func PipelinesDir(projectRoot string) string {
	return filepath.Join(projectRoot, "_apex", "pipelines")
}

// LoadSpec reads a pipeline spec by name from <projectRoot>/_apex/pipelines/<name>.yaml.
// Returns a wrapped fs.ErrNotExist when the file is missing, with a
// message that points the user at `ape framework update`.
func LoadSpec(name, projectRoot string) (*Spec, error) {
	path := filepath.Join(PipelinesDir(projectRoot), name+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf(
				"pipeline %q not found at %s — run \"ape framework update\" to install pipelines from the framework repo",
				name, path,
			)
		}
		return nil, fmt.Errorf("read pipeline %q: %w", name, err)
	}
	var spec Spec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse pipeline %q: %w", name, err)
	}
	if spec.Name != name {
		return nil, fmt.Errorf("pipeline %q: name field %q does not match filename", name, spec.Name)
	}
	stages, err := decodeStages(&spec.StagesRaw)
	if err != nil {
		return nil, fmt.Errorf("pipeline %q stages: %w", name, err)
	}
	spec.stages = stages
	spec.stageMap = make(map[string]*Stage, len(stages))
	for i := range spec.stages {
		spec.stageMap[spec.stages[i].Name] = &spec.stages[i]
	}
	return &spec, nil
}

// AvailablePipelines returns the pipeline names found in
// <projectRoot>/_apex/pipelines/, sorted. Returns an empty slice when
// the directory is missing or unreadable — callers that need a hard
// error should stat the dir themselves.
func AvailablePipelines(projectRoot string) []string {
	entries, err := os.ReadDir(PipelinesDir(projectRoot))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := strings.TrimSuffix(e.Name(), ".yaml")
		if n == e.Name() {
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// decodeStages walks the ordered yaml.Node representing the stages
// mapping and returns Stage values in declaration order. Using a raw
// node preserves order, which a plain map[string]Stage would not.
func decodeStages(node *yaml.Node) ([]Stage, error) {
	if node == nil || node.Kind == 0 {
		return nil, errors.New("missing stages")
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("stages must be a mapping, got kind %d", node.Kind)
	}
	if len(node.Content)%2 != 0 {
		return nil, errors.New("stages mapping malformed")
	}
	var stages []Stage
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		val := node.Content[i+1]
		if key.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("stage key must be scalar at line %d", key.Line)
		}
		stage := Stage{Name: key.Value}
		var body struct {
			Model  string           `yaml:"model,omitempty"`
			Agent  string           `yaml:"agent,omitempty"`
			Commit *CommitDirective `yaml:"commit,omitempty"`
			Chain  []Step           `yaml:"chain"`
		}
		if err := val.Decode(&body); err != nil {
			return nil, fmt.Errorf("stage %q: %w", stage.Name, err)
		}
		if len(body.Chain) == 0 {
			return nil, fmt.Errorf("stage %q: chain is empty", stage.Name)
		}
		for j, step := range body.Chain {
			if step.Skill == "" {
				return nil, fmt.Errorf("stage %q step %d: missing skill", stage.Name, j)
			}
		}
		stage.Model = body.Model
		stage.Agent = body.Agent
		stage.Commit = body.Commit
		stage.Chain = body.Chain
		stages = append(stages, stage)
	}
	return stages, nil
}

// EffectiveCommit captures the resolved commit policy for a step under
// PLAN-6 / C2 precedence. Boundary names which boundary the commit
// fires on; Directive carries the mode + message to apply when Boundary
// is not None. Phase D consumes this in the runner.
type EffectiveCommit struct {
	Boundary  CommitBoundary
	Directive CommitDirective
}

// CommitBoundary tells the runner which boundary an effective commit
// fires on.
type CommitBoundary int

const (
	// CommitBoundaryNone — no commit fires for this step / stage.
	CommitBoundaryNone CommitBoundary = iota
	// CommitBoundaryStep — commit fires after this step. Set when the
	// step itself opts in via step-level `commit:`.
	CommitBoundaryStep
	// CommitBoundaryStage — commit fires at the end of the stage,
	// capturing the chain's accumulated diff. Set when stage or
	// pipeline level set `commit:` and no step in the stage opted in
	// to a step-boundary commit.
	CommitBoundaryStage
)

// Effective returns the resolved Model, Agent, and EffectiveCommit for
// a (stage, step) under PLAN-6 / C2 precedence:
//
//	model:  step.Model  ?? stage.Model  ?? spec.Model  ?? ""
//	agent:  step.Agent  ?? stage.Agent  ?? spec.Agent  ?? ""
//	commit: step.Commit (if CommitSet) → step boundary
//	        else stage.Commit          → stage boundary
//	        else spec.Commit           → stage boundary
//	        else CommitBoundaryNone (no commit)
//
// "?? X" means "fall through to X when the higher level is empty/nil".
//
// commit precedence also honours `commit: false` at any level as the
// authoritative "skip" — once a level says skip, lower levels do not
// re-enable. This matches the plan's "commit: false at any level
// disables commits at that scope" rule with one practical refinement:
// because step.Commit only has effect when CommitSet is true,
// `commit: false` at step level disables the step boundary *and* the
// stage boundary for that step's stage (the step opted in to "no
// commit"); at stage or pipeline level, false disables the stage
// commit unconditionally.
//
// Phase A returns the value; runner.go does not yet consume it. The
// runner switches in Phase D.
func (s *Spec) Effective(stageName string, stepIdx int) (model, agent string, commit EffectiveCommit, err error) {
	stage, ok := s.stageMap[stageName]
	if !ok || stage == nil {
		return "", "", EffectiveCommit{}, fmt.Errorf("unknown stage %q", stageName)
	}
	if stepIdx < 0 || stepIdx >= len(stage.Chain) {
		return "", "", EffectiveCommit{}, fmt.Errorf("stage %q: step index %d out of range [0,%d)", stageName, stepIdx, len(stage.Chain))
	}
	step := stage.Chain[stepIdx]

	model = firstNonEmpty(step.Model, stage.Model, s.Model)
	agent = firstNonEmpty(step.Agent, stage.Agent, s.Agent)
	commit = resolveCommit(step, stage, s)
	return model, agent, commit, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func resolveCommit(step Step, stage *Stage, spec *Spec) EffectiveCommit {
	if step.CommitSet {
		if step.Commit.Mode == CommitModeSkip {
			return EffectiveCommit{Boundary: CommitBoundaryNone}
		}
		return EffectiveCommit{Boundary: CommitBoundaryStep, Directive: step.Commit}
	}
	if stage.Commit != nil {
		if stage.Commit.Mode == CommitModeSkip {
			return EffectiveCommit{Boundary: CommitBoundaryNone}
		}
		return EffectiveCommit{Boundary: CommitBoundaryStage, Directive: *stage.Commit}
	}
	if spec.Commit != nil {
		if spec.Commit.Mode == CommitModeSkip {
			return EffectiveCommit{Boundary: CommitBoundaryNone}
		}
		return EffectiveCommit{Boundary: CommitBoundaryStage, Directive: *spec.Commit}
	}
	return EffectiveCommit{Boundary: CommitBoundaryNone}
}
