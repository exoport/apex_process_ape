package apexv1

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/exoport/apex_process_ape/internal/effort"
	"gopkg.in/yaml.v3"
)

// The tools slot (D59 Q4): the skills a project adds to the design and
// review steps, each named with the condition that calls for it, and
// (v1.1 W6, Q-E1) the expert files the change, apply and review sessions
// read before they work.
//
//	tools:
//	  design:
//	    - {name: ui-mockup, skill: frontend-design:mockup, when: "the change adds or changes a screen",
//	       args: "{change} --out {assets}", model: sonnet, effort: high}
//	  review:
//	    - {name: security, skill: security-lens, when: "the change touches auth, secrets or input parsing",
//	       paths: ["internal/auth/**"], touches: [auth]}
//	  advise:
//	    - {name: nats, skill: nats-expert:nats-expert, when: "the change publishes, consumes or configures NATS",
//	       paths: ["internal/bus/**"], load: [advise.md], for: [change, apply, review]}
//
// args: (D77, optional) is the template ape fills into `ape task <skill>
// --args "…"`: {id}, {task}, {assets} and {change} are its placeholders
// (ValidateArgsTemplate); model: and effort: (optional) pick the tool
// session's model and effort, else the D65 defaults.
//
// paths: (globs) and touches: (capability slugs), optional on every entry
// (W6, Q-E2), make when: checkable: a change whose planned paths: or
// touches: (at change check) or whose diff (at verify) matches an entry it
// does not list is the notice tools.unmatched. when: stays the planner's
// judgement, so it warns and never refuses.
//
// An advise entry starts no session (W6, Q-E1): load: names files under
// its skill's folder, which ape resolves, sizes and hashes — never reads
// for meaning — and `ape status --json` lists for each change that names
// the entry in its advise: frontmatter (changes[].advise). for: says which
// of the change, apply and review sessions read them (default all three;
// never the delegate, Q-E12). caps.advise bounds an entry's files,
// caps.advise_session what one session reads (tools.too-big).
//
// A tool is a skill only (no MCP): a plugin skill (<plugin>:<skill>, the
// plugin listed in plugins:), a project skill (.claude/skills/<skill>/) or
// a user skill (<config dir>/skills/<skill>/). apex-change adds a task
// naming a design tool whose when: the change matches; the orchestrator
// runs each design or review tool as its own child session (`ape task
// <skill>`), so every skill those slots name joins the depth-1 allow-list
// (ChildSkillAllowList) and must be slash-invocable (not user-invocable:
// false); an advise skill does neither. tools: is under the
// config.changed guard: it widens what the orchestrator may start, and
// what text enters every apply session.
//
// The block never makes the config unparseable: what is wrong with it is
// a tools.invalid finding (`ape project check`), and only its valid entries
// are used.

// Tool is one tools.<slot> entry.
type Tool struct {
	Name   string `json:"name"             yaml:"name"`
	Skill  string `json:"skill"            yaml:"skill"`
	When   string `json:"when"             yaml:"when"`
	Args   string `json:"args,omitempty"   yaml:"args"`
	Model  string `json:"model,omitempty"  yaml:"model"`
	Effort string `json:"effort,omitempty" yaml:"effort"`
	// Paths and Touches (W6, Q-E2): globs and capability slugs a change
	// matching the entry would plan or touch (tools.unmatched).
	Paths   []string `json:"paths,omitempty"   yaml:"paths"`
	Touches []string `json:"touches,omitempty" yaml:"touches"`
	// Load and For are an advise entry's (W6, Q-E1): its files, relative
	// to the skill's folder, and the sessions that read them.
	Load []string `json:"load,omitempty" yaml:"load"`
	For  []string `json:"for,omitempty"  yaml:"for"`
}

// ToolsConfig is tools:.
type ToolsConfig struct {
	Design []Tool `json:"design,omitempty"`
	Review []Tool `json:"review,omitempty"`
	Advise []Tool `json:"advise,omitempty"`
	// problems is what the parse refused, for tools.invalid.
	problems []string
}

// ToolSlots are the slots whose skills run as sessions, in order: the
// allow-list and models: read these.
var ToolSlots = []string{"design", "review"}

// AllToolSlots are every slot, the advise one (W6) last.
var AllToolSlots = []string{"design", "review", "advise"}

// The advise sessions (W6, Q-E1): who reads an advise entry's files.
// apex-accept's delegate never does (Q-E12).
const (
	AdviseChange = "change"
	AdviseApply  = "apply"
	AdviseReview = "review"
	// AdvisePlan (v1.4.1, D611): the planning session reads the entry when
	// it plans a milestone. Never a default: an entry reads at plan time
	// only when its for: names plan.
	AdvisePlan = "plan"
)

// AdviseSessions are a change's sessions, in order: the for: an entry
// without one reads in.
var AdviseSessions = []string{AdviseChange, AdviseApply, AdviseReview}

// AdviseForValues are every value for: may hold.
var AdviseForValues = []string{AdvisePlan, AdviseChange, AdviseApply, AdviseReview}

// PlanAdvise are the tools.advise entries whose for: names plan, in order.
func (t ToolsConfig) PlanAdvise() []string {
	var out []string
	for _, a := range t.Advise {
		if slices.Contains(a.For, AdvisePlan) {
			out = append(out, a.Name)
		}
	}
	return out
}

// Slot returns a slot's entries.
func (t ToolsConfig) Slot(slot string) []Tool {
	switch slot {
	case "design":
		return t.Design
	case "review":
		return t.Review
	case "advise":
		return t.Advise
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

// Sessions is an advise entry's for:, all three when unset.
func (t Tool) Sessions() []string {
	if len(t.For) == 0 {
		return AdviseSessions
	}
	return t.For
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
		t.problems = append(t.problems, "tools: is not a mapping of slots (design, review, advise)")
		return nil
	}
	names := map[string]string{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		slot, val := n.Content[i].Value, n.Content[i+1]
		if !slices.Contains(AllToolSlots, slot) {
			t.problems = append(t.problems, fmt.Sprintf("tools.%s: unknown slot — the slots are design, review and advise", slot))
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
			tool, probs := parseTool(item, slot, fmt.Sprintf("tools.%s[%d]", slot, j))
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
			switch slot {
			case "design":
				t.Design = append(t.Design, tool)
			case "review":
				t.Review = append(t.Review, tool)
			default:
				t.Advise = append(t.Advise, tool)
			}
		}
	}
	return nil
}

// entryShape is a slot's entry, for the unknown-key message.
func entryShape(slot string) string {
	if slot == "advise" {
		return "{name, skill, when, load, for?, paths?, touches?}"
	}
	return "{name, skill, when, args?, model?, effort?, paths?, touches?}"
}

// listValue reads a list key: a sequence of strings, or one string.
func listValue(v *yaml.Node) ([]string, bool) {
	switch v.Kind {
	case yaml.ScalarNode:
		if v.Tag == "!!null" {
			return nil, true
		}
		return []string{strings.TrimSpace(v.Value)}, true
	case yaml.SequenceNode:
		out := make([]string, 0, len(v.Content))
		for _, item := range v.Content {
			if item.Kind != yaml.ScalarNode {
				return nil, false
			}
			out = append(out, strings.TrimSpace(item.Value))
		}
		return out, true
	default:
		return nil, false
	}
}

// parseTool reads one entry of slot, and what is wrong with it.
//
//nolint:gocyclo // one case per key, then one rule per field
func parseTool(n *yaml.Node, slot, at string) (tool Tool, probs []string) {
	if n.Kind != yaml.MappingNode {
		return tool, []string{at + ": is not a mapping " + entryShape(slot)}
	}
	advise := slot == "advise"
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		switch k {
		case "paths", "touches", "load", "for":
			if (k == "load" || k == "for") && !advise {
				probs = append(probs, fmt.Sprintf("%s: unknown key %q — an entry is %s (load and for are an advise entry's)", at, k, entryShape(slot)))
				continue
			}
			list, ok := listValue(v)
			if !ok {
				probs = append(probs, fmt.Sprintf("%s: %s is not a list of strings", at, k))
				continue
			}
			switch k {
			case "paths":
				tool.Paths = list
			case "touches":
				tool.Touches = list
			case "load":
				tool.Load = list
			default:
				tool.For = list
			}
			continue
		}
		if v.Kind != yaml.ScalarNode {
			probs = append(probs, fmt.Sprintf("%s: %s is not a string", at, k))
			continue
		}
		val := strings.TrimSpace(v.Value)
		switch {
		case k == "name":
			tool.Name = val
		case k == "skill":
			tool.Skill = val
		case k == "when":
			tool.When = val
		case k == "args" && !advise:
			tool.Args = val
		case k == "model" && !advise:
			tool.Model = val
		case k == "effort" && !advise:
			tool.Effort = val
		case advise && (k == "args" || k == "model" || k == "effort"):
			probs = append(probs, fmt.Sprintf("%s: unknown key %q — an entry is %s (an advise entry starts no session)", at, k, entryShape(slot)))
		default:
			probs = append(probs, fmt.Sprintf("%s: unknown key %q — an entry is %s", at, k, entryShape(slot)))
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
	if NeverChildSkill(tool.Skill) {
		probs = append(probs, fmt.Sprintf("%s: skill %q is never started by the loop — a release is prepared only on a person's instruction (D524), so it cannot be a tool", at, tool.Skill))
	}
	if err := ValidateArgsTemplate(tool.Args); err != nil {
		probs = append(probs, fmt.Sprintf("%s: %v", at, err))
	}
	if tool.Effort != "" && !slices.Contains(effort.Levels, tool.Effort) {
		probs = append(probs, fmt.Sprintf("%s: effort %q is not one of %s", at, tool.Effort, strings.Join(effort.Levels, ", ")))
	}
	for _, g := range tool.Paths {
		if _, err := globRegexp(g); err != nil || strings.HasPrefix(g, "/") {
			probs = append(probs, fmt.Sprintf("%s: paths entry %q is not a glob relative to the project root", at, g))
		}
	}
	for _, s := range tool.Touches {
		if !slugRe.MatchString(s) {
			probs = append(probs, fmt.Sprintf("%s: touches entry %q is not a kebab-case capability slug", at, s))
		}
	}
	if advise {
		probs = append(probs, adviseProblems(tool, at)...)
	}
	return tool, probs
}

// adviseProblems checks an advise entry's load: and for:.
func adviseProblems(tool Tool, at string) []string {
	var probs []string
	if len(tool.Load) == 0 {
		probs = append(probs, at+": missing load (the files, relative to the skill's folder, that the sessions read)")
	}
	for _, f := range tool.Load {
		if !loadPathOK(f) {
			probs = append(probs, fmt.Sprintf("%s: load entry %q is not a file path inside the skill's folder (relative, no ..)", at, f))
		}
	}
	seen := map[string]bool{}
	for _, s := range tool.For {
		switch {
		case !slices.Contains(AdviseForValues, s):
			probs = append(probs, fmt.Sprintf("%s: for %q is not one of plan, change, apply, review (no delegate reads advise)", at, s))
		case seen[s]:
			probs = append(probs, fmt.Sprintf("%s: for lists %q twice", at, s))
		}
		seen[s] = true
	}
	return probs
}

// loadPathOK: a load: entry is a clean relative slash path under the
// skill's folder.
func loadPathOK(f string) bool {
	if f == "" || strings.HasPrefix(f, "/") || strings.Contains(f, `\`) || filepath.IsAbs(f) {
		return false
	}
	c := path.Clean(f)
	return c != "." && c != ".." && !strings.HasPrefix(c, "../")
}

// ToolStatus is one tools: entry as `ape plugins status` reports it.
type ToolStatus struct {
	Slot   string `json:"slot"`
	Name   string `json:"name"`
	Skill  string `json:"skill"`
	Args   string `json:"args,omitempty"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Source is where the skill was found: plugin, project or user.
	Source string `json:"source,omitempty"`
	Path   string `json:"path,omitempty"`
	// State: ok, not-installed, not-invocable, or unchecked (a plugin skill
	// while the installs cannot be read: claude is not on PATH).
	State string `json:"state"`
	// For and Files are an advise entry's (W6): the sessions that read it,
	// and its load: files as resolved — Files only when the skill was found.
	For   []string     `json:"for,omitempty"`
	Files []AdviseFile `json:"files,omitempty"`
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

// localSkill finds a user or project skill (or command) by name: under
// the user's .claude, then the project's, skills/<name>/SKILL.md, then
// commands/<name>.md. A user skill or command wins over a project one of
// the same name, as in Claude Code. A command's folder (filepath.Dir of
// its Path, which an advise entry's load: resolves under) is the whole
// commands/ folder.
func (p *Project) localSkill(name string) (SkillInfo, string, bool) {
	bases := []struct{ source, dir string }{{"user", claudeConfigDir()}, {"project", filepath.Join(p.liveRoot(), ".claude")}}
	for _, b := range bases {
		if b.dir == "" {
			continue
		}
		if file := filepath.Join(b.dir, "skills", name, "SKILL.md"); fileExists(file) {
			return readSkill(file, name), b.source, true
		}
		if file := filepath.Join(b.dir, "commands", name+".md"); fileExists(file) {
			return readSkill(file, name), b.source, true
		}
	}
	return SkillInfo{}, "", false
}

// toolStatuses resolves every tools: entry and returns the tools.*
// findings: tools.invalid for the block's problems, tools.not-installed
// and tools.not-invocable per entry, and for an advise entry (W6)
// tools.not-installed for a load: file its skill's folder lacks and
// tools.too-big for files over caps.advise. rows are the plugins'
// statuses (pluginStatuses); checked says whether they were read.
func (p *Project) toolStatuses(cfg Config, rows []PluginStatus, checked bool) ([]ToolStatus, []Finding) {
	out := []ToolStatus{}
	var fs []Finding
	for _, prob := range cfg.Tools.problems {
		fs = append(fs, Finding{File: configYAML, Message: "tools config: " + prob + " — fix the tools: block (an entry is {name, skill, when, args?, model?, effort?, paths?, touches?}; an advise entry {name, skill, when, load, for?, paths?, touches?})"})
	}
	adviseCap := cfg.Caps.Limits().Advise
	for _, slot := range AllToolSlots {
		tools := cfg.Tools.Slot(slot)
		for i := range tools {
			tool := &tools[i]
			st := p.resolveTool(cfg, rows, checked, slot, *tool)
			label := "tool " + slot + "/" + tool.Name + ": skill " + tool.Skill
			switch st.State {
			case ToolNotInstalled:
				fs = append(fs, Finding{File: configYAML, Message: label + " is not installed (" + st.Path + ") — " + installHint(tool.Skill)})
			case ToolNotInvocable:
				fs = append(fs, Finding{File: configYAML, Message: label + " is not slash-invocable (user-invocable: false in " + st.Path +
					") — ape task types it as a slash command: drop user-invocable: false, or name another skill"})
			case ToolOK:
				if slot == "advise" {
					fs = append(fs, p.adviseEntryStatus(&st, *tool, adviseCap)...)
				}
			}
			out = append(out, st)
		}
	}
	return out, append(fs, planAdviseCap(cfg, out)...)
}

// planAdviseCap is the plan session's caps.advise_session finding (D611):
// what the entries whose for: names plan load together. A change's
// sessions are capped by change check, against the entries it names; the
// plan session reads every plan entry, so it is capped here, on the config.
func planAdviseCap(cfg Config, sts []ToolStatus) []Finding {
	total := 0
	var names []string
	for i := range sts {
		st := &sts[i]
		if st.Slot != "advise" || st.State != ToolOK || !slices.Contains(st.For, AdvisePlan) {
			continue
		}
		for j := range st.Files {
			total += st.Files[j].Bytes
		}
		names = append(names, st.Name)
	}
	limit := cfg.Caps.Limits().AdviseSession
	if total <= limit {
		return nil
	}
	return []Finding{{File: configYAML, Message: "advise: the plan session would read " + sizeKB(total) + " (" + strings.Join(names, ", ") + "), over the " +
		capSize(limit) + " cap (caps.advise_session) — a person narrows an entry's for: or raises caps.advise_session in _apex/config.yaml"}}
}

// adviseEntryStatus resolves an advise entry's load: files into st and
// returns its findings: a file the skill's folder lacks
// (tools.not-installed), files over caps.advise together (tools.too-big).
func (p *Project) adviseEntryStatus(st *ToolStatus, tool Tool, adviseCap int) []Finding {
	st.For = tool.Sessions()
	st.Files = adviseFilesOf(tool, filepath.Dir(st.Path))
	label := "tool advise/" + tool.Name + ": skill " + tool.Skill
	var fs []Finding
	total := 0
	for i := range st.Files {
		f := &st.Files[i]
		total += f.Bytes
		if f.Error != "" {
			st.State = ToolNotInstalled
			fs = append(fs, Finding{File: configYAML, Message: label + " is not installed with its load file " + f.File + " (" + f.Error + ": " + f.Path +
				") — install a version of the skill that ships it, or fix load: in _apex/config.yaml"})
		}
	}
	if total > adviseCap {
		fs = append(fs, Finding{File: configYAML, Message: label + " loads " + sizeKB(total) + ", over the " + capSize(adviseCap) + " cap (caps.advise)" +
			" — the expert ships a shorter advise file (a rule that can be checked becomes a script), load: names less, or a person raises caps.advise (ceiling " + capSize(capCeiling(CapAdvise)) + ")"})
	}
	return fs
}

// installHint is the remediation for a tool skill that is not installed.
func installHint(skill string) string {
	if plugin, _, ok := strings.Cut(skill, ":"); ok {
		return "list the plugin " + plugin + " in plugins: and run `ape plugins sync`, or fix the skill name"
	}
	return "add .claude/skills/" + skill + "/SKILL.md or .claude/commands/" + skill + ".md (or a user skill or command), or fix the skill name"
}

// resolveTool finds one tool's skill. Path is where it was found or, when
// it was not, where it was looked for.
func (p *Project) resolveTool(cfg Config, rows []PluginStatus, checked bool, slot string, tool Tool) ToolStatus {
	st := ToolStatus{Slot: slot, Name: tool.Name, Skill: tool.Skill, Args: tool.Args, Model: tool.Model, Effort: tool.Effort, State: ToolNotInstalled}
	judge := func(s SkillInfo) ToolStatus {
		st.Path, st.State = s.Path, ToolOK
		// An advise skill starts no session: only its files are read.
		if !s.UserInvocable && slot != "advise" {
			st.State = ToolNotInvocable
		}
		return st
	}
	if !strings.Contains(tool.Skill, ":") {
		if s, source, ok := p.localSkill(tool.Skill); ok {
			st.Source = source
			return judge(s)
		}
		st.Path = "no skills/" + tool.Skill + "/SKILL.md or commands/" + tool.Skill + ".md in the project's or the user's .claude"
		return st
	}
	st.Source = "plugin"
	s, state, why := PluginSkill(cfg, rows, checked, tool.Skill)
	if state == ToolOK {
		return judge(s)
	}
	st.State, st.Path = state, why
	return st
}

// PluginSkill resolves a plugin skill, <plugin>:<skill>, as Claude Code
// would load it in an ape session: the plugin is listed in plugins:, it
// is installed (rows, pluginStatuses' — parallel to cfg.Plugins), and it
// carries a skill answering to <skill> (its name or an alias). state is
// ToolOK with the skill, else ToolNotInstalled, or ToolUnchecked when the
// installs were not read (checked false); why says what is missing.
//
// It is the one resolution: a tools: entry (resolveTool) and ape task's
// preflight (PluginSkillResolver) both use it, so `ape project check`
// cannot call a tool installed that `ape task` then refuses to start
// (F-E1-1).
func PluginSkill(cfg Config, rows []PluginStatus, checked bool, name string) (s SkillInfo, state, why string) {
	s, miss := pluginSkill(cfg, rows, checked, name)
	plugin, skill, _ := strings.Cut(name, ":")
	switch miss {
	case pluginSkillFound:
		return s, ToolOK, ""
	case pluginNotNamespaced:
		return s, ToolNotInstalled, name + " is not a <plugin>:<skill> name"
	case pluginUnlisted:
		return s, ToolNotInstalled, "no plugin " + plugin + " in plugins:"
	case pluginUnchecked:
		return s, ToolUnchecked, "claude is not on PATH: the installs were not read"
	case pluginNotInstalled:
		return s, ToolNotInstalled, "plugin " + plugin + " is not installed"
	case pluginLacksSkill: // the return below
	}
	return s, ToolNotInstalled, "plugin " + plugin + " has no skill " + skill
}

// pluginSkillMiss is why pluginSkill found no skill.
type pluginSkillMiss int

const (
	pluginSkillFound pluginSkillMiss = iota
	pluginNotNamespaced
	pluginUnlisted
	pluginUnchecked
	pluginNotInstalled
	pluginLacksSkill
)

func pluginSkill(cfg Config, rows []PluginStatus, checked bool, name string) (SkillInfo, pluginSkillMiss) {
	plugin, skill, namespaced := strings.Cut(name, ":")
	if !namespaced {
		return SkillInfo{}, pluginNotNamespaced
	}
	listed, installed := false, false
	for i, pl := range cfg.Plugins {
		if pl.Name != plugin || i >= len(rows) {
			continue
		}
		listed = true
		installed = installed || rows[i].Installed
		for _, sk := range rows[i].contents.Skills {
			if sk.answersTo(skill) {
				return sk, pluginSkillFound
			}
		}
	}
	switch {
	case !listed:
		return SkillInfo{}, pluginUnlisted
	case !checked:
		return SkillInfo{}, pluginUnchecked
	case !installed:
		return SkillInfo{}, pluginNotInstalled
	}
	return SkillInfo{}, pluginLacksSkill
}

// PluginSkillResolver is PluginSkill for `ape task` and `ape prompt
// --agent`: it reads the config and the listed plugins' installs once, on
// the first name it is asked, and returns nil when the skill resolves,
// else what is missing and the fix — installHint's for a plugin that is
// not listed or not installed, the skill name for a plugin that lacks it.
func (p *Project) PluginSkillResolver(ctx context.Context, cli PluginCLI) func(name string) error {
	var (
		once    sync.Once
		cfg     Config
		rows    []PluginStatus
		checked bool
		loadErr error
	)
	return func(name string) error {
		once.Do(func() {
			if cfg, loadErr = LoadConfig(p.Root); loadErr != nil || len(cfg.Plugins) == 0 {
				return // no plugin listed: every plugin skill is unlisted
			}
			rows, checked, loadErr = p.pluginStatuses(ctx, cli, cfg)
		})
		if loadErr != nil {
			return fmt.Errorf("cannot read the plugins: %w", loadErr)
		}
		_, state, why := PluginSkill(cfg, rows, checked, name)
		if state == ToolOK {
			return nil
		}
		plugin, _, _ := strings.Cut(name, ":")
		if _, miss := pluginSkill(cfg, rows, checked, name); miss == pluginLacksSkill {
			return errors.New(why + " — fix the skill name, or install a version of " + plugin + " that ships it")
		}
		return errors.New(why + " — " + installHint(name))
	}
}
