// Package pipeline implements the ape pipeline runner: it loads named
// pipeline specifications (embedded YAML), validates prerequisites,
// and drives the underlying claude CLI through each stage's skill chain.
package pipeline

import (
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed spec/*.yaml
var specsFS embed.FS

// Spec describes a named pipeline.
type Spec struct {
	Name      string            `yaml:"name"`
	Requires  Requires          `yaml:"requires,omitempty"`
	StagesRaw yaml.Node         `yaml:"stages"` //nolint:tagliatelle // embedded YAML files use "stages"; field name includes "Raw" suffix to signal internal use
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
type Stage struct {
	Name  string
	Chain []Step
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
	PromptFlag string `yaml:"prompt_flag,omitempty"` //nolint:tagliatelle // embedded spec YAML files use snake_case prompt_flag
}

// Stages returns the pipeline's stages in declaration order.
func (s *Spec) Stages() []Stage {
	return s.stages
}

// LoadSpec reads a pipeline spec by name from the embedded registry.
// Returns an error if the name is unknown or the YAML fails to parse.
func LoadSpec(name string) (*Spec, error) {
	data, err := specsFS.ReadFile("spec/" + name + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("unknown pipeline %q: %w", name, err)
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

// AvailablePipelines returns the registered pipeline names sorted.
func AvailablePipelines() []string {
	entries, err := specsFS.ReadDir("spec")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
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
			Chain []Step `yaml:"chain"`
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
		stage.Chain = body.Chain
		stages = append(stages, stage)
	}
	return stages, nil
}
