package apexv1

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// The tools slot (D59 Q4): the skills a project adds to the design and
// review steps, each named with the condition that calls for it.
//
//	tools:
//	  design:
//	    - {name: ui-mockup, skill: frontend-design:mockup, when: "the change adds or changes a screen"}
//	  review:
//	    - {name: security, skill: security-lens, when: "the change touches auth, secrets or input parsing"}
//
// A tool is a skill only (no MCP): a plugin skill (<plugin>:<skill>, the
// plugin listed in plugins:), a project skill (.claude/skills/<skill>/) or
// a user skill (<config dir>/skills/<skill>/). apex-change adds a task
// naming a design tool whose when: the change matches; the orchestrator
// runs each tool as its own child session (`ape task <skill>`), so every
// skill named here joins the depth-1 allow-list (ChildSkillAllowList) and
// must be slash-invocable (not user-invocable: false). tools: is under the
// config.changed guard: it widens what the orchestrator may start.
//
// The block never makes the config unparseable: what is wrong with it is
// a tools.invalid finding (`ape project check`), and only its valid entries
// are used.

// Tool is one tools.<slot> entry.
type Tool struct {
	Name  string `json:"name"`
	Skill string `json:"skill"`
	When  string `json:"when"`
}

// ToolsConfig is tools:.
type ToolsConfig struct {
	Design []Tool `json:"design,omitempty"`
	Review []Tool `json:"review,omitempty"`
	// problems is what the parse refused, for tools.invalid.
	problems []string
}

// ToolSlots are the slots, in order.
var ToolSlots = []string{"design", "review"}

// Slot returns a slot's entries.
func (t ToolsConfig) Slot(slot string) []Tool {
	switch slot {
	case "design":
		return t.Design
	case "review":
		return t.Review
	}
	return nil
}

// Find returns the slot's entry named name, or nil.
func (t ToolsConfig) Find(slot, name string) *Tool {
	ts := t.Slot(slot)
	for i := range ts {
		if ts[i].Name == name {
			return &ts[i]
		}
	}
	return nil
}

// toolSkillRe is a skill name, optionally namespaced by its plugin.
var toolSkillRe = regexp.MustCompile(`^(?:[A-Za-z0-9][A-Za-z0-9._-]*:)?[A-Za-z0-9][A-Za-z0-9._-]*$`)

// UnmarshalYAML parses the block, recording what is wrong instead of
// failing.
func (t *ToolsConfig) UnmarshalYAML(n *yaml.Node) error {
	*t = ToolsConfig{}
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		t.problems = append(t.problems, "tools: is not a mapping of slots (design, review)")
		return nil
	}
	names := map[string]string{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		slot, val := n.Content[i].Value, n.Content[i+1]
		if slot != "design" && slot != "review" {
			t.problems = append(t.problems, fmt.Sprintf("tools.%s: unknown slot — the slots are design and review", slot))
			continue
		}
		if val.Kind == yaml.ScalarNode && val.Tag == "!!null" {
			continue
		}
		if val.Kind != yaml.SequenceNode {
			t.problems = append(t.problems, "tools."+slot+": is not a list of {name, skill, when}")
			continue
		}
		for j, item := range val.Content {
			tool, probs := parseTool(item, fmt.Sprintf("tools.%s[%d]", slot, j))
			if tool.Name != "" {
				if prev, dup := names[tool.Name]; dup {
					probs = append(probs, fmt.Sprintf("tools.%s[%d]: name %q is already used by tools.%s", slot, j, tool.Name, prev))
				} else {
					names[tool.Name] = fmt.Sprintf("%s[%d]", slot, j)
				}
			}
			if len(probs) > 0 {
				t.problems = append(t.problems, probs...)
				continue
			}
			if slot == "design" {
				t.Design = append(t.Design, tool)
			} else {
				t.Review = append(t.Review, tool)
			}
		}
	}
	return nil
}

// parseTool reads one entry, and what is wrong with it.
func parseTool(n *yaml.Node, at string) (Tool, []string) {
	var tool Tool
	if n.Kind != yaml.MappingNode {
		return tool, []string{at + ": is not a mapping {name, skill, when}"}
	}
	var probs []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		if v.Kind != yaml.ScalarNode {
			probs = append(probs, fmt.Sprintf("%s: %s is not a string", at, k))
			continue
		}
		val := strings.TrimSpace(v.Value)
		switch k {
		case "name":
			tool.Name = val
		case "skill":
			tool.Skill = val
		case "when":
			tool.When = val
		default:
			probs = append(probs, fmt.Sprintf("%s: unknown key %q — an entry is {name, skill, when}", at, k))
		}
	}
	for _, f := range []struct{ key, val string }{{"name", tool.Name}, {"skill", tool.Skill}, {"when", tool.When}} {
		if f.val == "" {
			probs = append(probs, at+": missing "+f.key)
		}
	}
	if tool.Skill != "" && !toolSkillRe.MatchString(tool.Skill) {
		probs = append(probs, fmt.Sprintf("%s: skill %q is not a skill name (<skill>, or <plugin>:<skill> for a plugin's; no leading /)", at, tool.Skill))
	}
	return tool, probs
}

// ToolStatus is one tools: entry as `ape plugins status` reports it.
type ToolStatus struct {
	Slot  string `json:"slot"`
	Name  string `json:"name"`
	Skill string `json:"skill"`
	// Source is where the skill was found: plugin, project or user.
	Source string `json:"source,omitempty"`
	Path   string `json:"path,omitempty"`
	// State: ok, not-installed, not-invocable, or unchecked (a plugin skill
	// while the installs cannot be read: claude is not on PATH).
	State string `json:"state"`
}

// The ToolStatus states.
const (
	ToolOK           = "ok"
	ToolNotInstalled = "not-installed"
	ToolNotInvocable = "not-invocable"
	ToolUnchecked    = "unchecked"
)

// claudeConfigDir is Claude Code's config directory: $CLAUDE_CONFIG_DIR,
// else ~/.claude ("" when there is no home).
func claudeConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// localSkill finds a user or project skill (or command) by name. A user
// skill wins over a project skill of the same name, as in Claude Code.
func (p *Project) localSkill(name string) (SkillInfo, string, bool) {
	bases := []struct{ source, dir string }{{"user", claudeConfigDir()}, {"project", filepath.Join(p.Root, ".claude")}}
	for _, b := range bases {
		if b.dir == "" {
			continue
		}
		if path := filepath.Join(b.dir, "skills", name, "SKILL.md"); fileExists(path) {
			return readSkill(path, name), b.source, true
		}
		if path := filepath.Join(b.dir, "commands", name+".md"); fileExists(path) {
			return readSkill(path, name), b.source, true
		}
	}
	return SkillInfo{}, "", false
}

// toolStatuses resolves every tools: entry and returns the tools.*
// findings: tools.invalid for the block's problems, tools.not-installed
// and tools.not-invocable per entry. rows are the plugins' statuses
// (pluginStatuses); checked says whether they were read.
func (p *Project) toolStatuses(cfg Config, rows []PluginStatus, checked bool) ([]ToolStatus, []Finding) {
	out := []ToolStatus{}
	var fs []Finding
	for _, prob := range cfg.Tools.problems {
		fs = append(fs, Finding{File: configYAML, Message: "tools config: " + prob + " — fix the tools: block (an entry is {name, skill, when})"})
	}
	for _, slot := range ToolSlots {
		for _, tool := range cfg.Tools.Slot(slot) {
			st := p.resolveTool(cfg, rows, checked, slot, tool)
			out = append(out, st)
			label := "tool " + slot + "/" + tool.Name + ": skill " + tool.Skill
			switch st.State {
			case ToolNotInstalled:
				fs = append(fs, Finding{File: configYAML, Message: label + " is not installed (" + st.Path + ") — " + installHint(tool.Skill)})
			case ToolNotInvocable:
				fs = append(fs, Finding{File: configYAML, Message: label + " is not slash-invocable (user-invocable: false in " + st.Path +
					") — ape task types it as a slash command: drop user-invocable: false, or name another skill"})
			}
		}
	}
	return out, fs
}

// installHint is the remediation for a tool skill that is not installed.
func installHint(skill string) string {
	if plugin, _, ok := strings.Cut(skill, ":"); ok {
		return "list the plugin " + plugin + " in plugins: and run `ape plugins sync`, or fix the skill name"
	}
	return "add .claude/skills/" + skill + "/SKILL.md (or a user skill), or fix the skill name"
}

// resolveTool finds one tool's skill. Path is where it was found or, when
// it was not, where it was looked for.
func (p *Project) resolveTool(cfg Config, rows []PluginStatus, checked bool, slot string, tool Tool) ToolStatus {
	st := ToolStatus{Slot: slot, Name: tool.Name, Skill: tool.Skill, State: ToolNotInstalled}
	judge := func(s SkillInfo) ToolStatus {
		st.Path, st.State = s.Path, ToolOK
		if !s.UserInvocable {
			st.State = ToolNotInvocable
		}
		return st
	}
	plugin, skill, namespaced := strings.Cut(tool.Skill, ":")
	if !namespaced {
		if s, source, ok := p.localSkill(tool.Skill); ok {
			st.Source = source
			return judge(s)
		}
		st.Path = "no skills/" + tool.Skill + "/SKILL.md in the project's or the user's .claude"
		return st
	}
	st.Source = "plugin"
	listed := false
	for i, pl := range cfg.Plugins {
		if pl.Name != plugin || i >= len(rows) {
			continue
		}
		listed = true
		for _, s := range rows[i].contents.Skills {
			if s.answersTo(skill) {
				return judge(s)
			}
		}
	}
	switch {
	case !listed:
		st.Path = "no plugin " + plugin + " in plugins:"
	case !checked:
		st.State, st.Path = ToolUnchecked, "claude is not on PATH: the installs were not read"
	default:
		st.Path = "plugin " + plugin + " is not installed, or has no skill " + skill
	}
	return st
}
