package apexv1

import (
	"os"

	"gopkg.in/yaml.v3"
)

// rc3-f STUB — the integrator deletes this file: rc3-d owns the tools:
// schema (tools.go: Tool, ToolsConfig with Find, Config.Tools). After the
// merge, toolsOf's one call (toolrun.go) becomes cfg.Tools.

// Tool is one tools.<slot> entry (rc3-d's shape).
type Tool struct {
	Name   string `json:"name"             yaml:"name"`
	Skill  string `json:"skill"            yaml:"skill"`
	When   string `json:"when"             yaml:"when"`
	Args   string `json:"args,omitempty"   yaml:"args"`
	Model  string `json:"model,omitempty"  yaml:"model"`
	Effort string `json:"effort,omitempty" yaml:"effort"`
}

// ToolsConfig is tools: (rc3-d's shape).
type ToolsConfig struct {
	Design []Tool `json:"design,omitempty" yaml:"design"`
	Review []Tool `json:"review,omitempty" yaml:"review"`
}

// Find returns the slot's entry named name, or nil.
func (t ToolsConfig) Find(slot, name string) *Tool {
	ts := t.Design
	if slot == "review" {
		ts = t.Review
	}
	for i := range ts {
		if ts[i].Name == name {
			return &ts[i]
		}
	}
	return nil
}

// toolsOf is the project's tools: block — after the merge, cfg.Tools.
func toolsOf(_ Config, root string) ToolsConfig {
	var c struct {
		Tools ToolsConfig `yaml:"tools"`
	}
	if data, err := os.ReadFile(ConfigPath(root)); err == nil {
		_ = yaml.Unmarshal(data, &c)
	}
	return c.Tools
}
