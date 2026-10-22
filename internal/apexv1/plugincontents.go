package apexv1

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/frontmatter"
	"gopkg.in/yaml.v3"
)

// What a listed plugin brings into a session (D59 Q4–Q6), read from the
// copy Claude Code loads it from (the installPath `claude plugin list
// --json` reports):
//
//   - skills: skills/<name>/SKILL.md and commands/**/*.md (Claude Code lists
//     a command as a skill), plus the extra paths plugin.json's skills and
//     commands name. A skill without disable-model-invocation: true is
//     model-invocable, and its description is listed in every session: that
//     count is the plugin's per-session cost;
//   - hooks: hooks/hooks.json, or hooks in .claude-plugin/plugin.json;
//   - MCP servers: .mcp.json, or mcpServers in .claude-plugin/plugin.json.
//
// A tool is a skill only (D59 Q4), so hooks or MCP servers are a finding
// (plugin.hooks-or-mcp) unless the plugins: entry allows them (allow:).

// SkillInfo is one skill (or command) a plugin, the project or the user
// provides.
type SkillInfo struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// ModelInvocable: no disable-model-invocation: true.
	ModelInvocable bool `json:"model_invocable"`
	// UserInvocable: no user-invocable: false — a slash command a person,
	// and ape task, can type.
	UserInvocable bool `json:"user_invocable"`
	// aliases are the other names the skill answers to: its directory or
	// file name when frontmatter name: differs.
	aliases []string
}

// answersTo reports whether name names the skill.
func (s SkillInfo) answersTo(name string) bool {
	return s.Name == name || slices.Contains(s.aliases, name)
}

// PluginContents is what one installed plugin carries.
type PluginContents struct {
	Skills []SkillInfo
	// Hooks and MCP name where each was found (empty: none).
	Hooks string
	MCP   string
}

// readSkill reads one SKILL.md or command file. name is the directory
// (skill) or file (command) name; frontmatter name: wins when present.
func readSkill(path, name string) SkillInfo {
	s := SkillInfo{Name: name, Path: path, ModelInvocable: true, UserInvocable: true}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	fm, _, err := frontmatter.Split(data)
	if err != nil || len(fm) == 0 {
		return s
	}
	var meta map[string]any
	if yaml.Unmarshal(fm, &meta) != nil {
		return s
	}
	if n, ok := meta["name"].(string); ok && strings.TrimSpace(n) != "" && strings.TrimSpace(n) != name {
		s.aliases = append(s.aliases, name)
		s.Name = strings.TrimSpace(n)
	}
	s.ModelInvocable = !yamlBool(meta["disable-model-invocation"], false)
	s.UserInvocable = yamlBool(meta["user-invocable"], true)
	return s
}

// yamlBool reads a frontmatter flag: a YAML bool, or the strings
// "true"/"false"; anything else is def.
func yamlBool(v any, def bool) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		switch strings.ToLower(strings.TrimSpace(b)) {
		case "true":
			return true
		case "false":
			return false
		}
	}
	return def
}

// skillsIn lists the skills under dir: dir itself when it holds a
// SKILL.md, else each subdirectory that does.
func skillsIn(dir string) []SkillInfo {
	if fileExists(filepath.Join(dir, "SKILL.md")) {
		return []SkillInfo{readSkill(filepath.Join(dir, "SKILL.md"), filepath.Base(dir))}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []SkillInfo
	for _, e := range entries {
		path := filepath.Join(dir, e.Name(), "SKILL.md")
		if e.IsDir() && fileExists(path) {
			out = append(out, readSkill(path, e.Name()))
		}
	}
	return out
}

// commandsIn lists the commands under path: the file itself when it is a
// .md file, else every .md file below it, named by its base name.
func commandsIn(path string) []SkillInfo {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if !fi.IsDir() {
		if strings.HasSuffix(path, ".md") {
			return []SkillInfo{readSkill(path, strings.TrimSuffix(filepath.Base(path), ".md"))}
		}
		return nil
	}
	var out []SkillInfo
	_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
			out = append(out, readSkill(p, strings.TrimSuffix(d.Name(), ".md")))
		}
		return nil
	})
	return out
}

// manifestPaths reads plugin.json's skills/commands value: a path or a
// list of paths, relative to the plugin root.
func manifestPaths(root string, v any) []string {
	var raw []any
	switch x := v.(type) {
	case string:
		raw = []any{x}
	case []any:
		raw = x
	}
	var out []string
	for _, r := range raw {
		if s, ok := r.(string); ok && s != "" {
			out = append(out, filepath.Join(root, filepath.FromSlash(s)))
		}
	}
	return out
}

// nonEmpty reports whether a manifest value declares anything.
func nonEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// declaresIn reports whether a hooks.json or .mcp.json declares anything
// under key (or, for a file with no such key, at its top level). A file
// that does not parse counts: Claude Code may read what this cannot.
func declaresIn(path, key string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var doc map[string]any
	if json.Unmarshal(data, &doc) != nil {
		return true
	}
	if v, ok := doc[key]; ok {
		return nonEmpty(v)
	}
	return len(doc) > 0
}

// InspectPlugin reads what an installed plugin carries.
func InspectPlugin(root string) PluginContents {
	var pc PluginContents
	var manifest map[string]any
	if data, err := os.ReadFile(filepath.Join(root, ".claude-plugin", "plugin.json")); err == nil {
		_ = json.Unmarshal(data, &manifest)
	}
	seen := map[string]bool{}
	add := func(ss []SkillInfo) {
		for _, s := range ss {
			if !seen[s.Path] {
				seen[s.Path] = true
				pc.Skills = append(pc.Skills, s)
			}
		}
	}
	add(skillsIn(filepath.Join(root, "skills")))
	for _, p := range manifestPaths(root, manifest["skills"]) {
		add(skillsIn(p))
	}
	add(commandsIn(filepath.Join(root, "commands")))
	for _, p := range manifestPaths(root, manifest["commands"]) {
		add(commandsIn(p))
	}
	sort.Slice(pc.Skills, func(i, j int) bool { return pc.Skills[i].Name < pc.Skills[j].Name })
	switch {
	case declaresIn(filepath.Join(root, "hooks", "hooks.json"), "hooks"):
		pc.Hooks = "hooks/hooks.json"
	case nonEmpty(manifest["hooks"]):
		pc.Hooks = "plugin.json hooks"
	}
	switch {
	case declaresIn(filepath.Join(root, ".mcp.json"), "mcpServers"):
		pc.MCP = ".mcp.json"
	case nonEmpty(manifest["mcpServers"]):
		pc.MCP = "plugin.json mcpServers"
	}
	return pc
}

// ModelInvocable counts the skills listed in every session.
func (pc PluginContents) ModelInvocable() int {
	n := 0
	for _, s := range pc.Skills {
		if s.ModelInvocable {
			n++
		}
	}
	return n
}

// PluginStatus is one listed plugin as `ape plugins status` reports it.
// The counts and flags are read from the install: zero when it is not
// installed, or when the installs could not be read (InstallsChecked).
type PluginStatus struct {
	ID          string `json:"id"`
	Ref         string `json:"ref,omitempty"`
	Installed   bool   `json:"installed"`
	Version     string `json:"version,omitempty"`
	AtRef       bool   `json:"at_ref"`
	Enabled     bool   `json:"enabled"`
	InstallPath string `json:"install_path,omitempty"`
	Skills      int    `json:"skills"`
	// ModelInvocable is the per-session listing cost: skills without
	// disable-model-invocation: true.
	ModelInvocable int      `json:"model_invocable"`
	Hooks          bool     `json:"hooks"`
	MCP            bool     `json:"mcp"`
	Allow          []string `json:"allow,omitempty"`
	// Allowed: every hooks/MCP the plugin carries is allowed by its entry.
	Allowed bool `json:"allowed"`

	contents PluginContents
}

// PluginsReport is `ape plugins status`.
type PluginsReport struct {
	// InstallsChecked is false when claude is not on PATH: then only the
	// config, settings.json and project/user skills are judged.
	InstallsChecked bool           `json:"installs_checked"`
	Plugins         []PluginStatus `json:"plugins"`
	Tools           []ToolStatus   `json:"tools"`
	Findings        []Finding      `json:"findings"`
	// Outdated is each listed marketplace's newer tags, with --outdated
	// only (W6, Q-E7).
	Outdated []MarketplaceTags `json:"outdated,omitempty"`
}

// listOnce memoizes Installed for one command, so a command that reads the
// installs twice (the drift check, then the contents) asks the CLI once.
// Install and Update forget it.
type listOnce struct {
	PluginCLI
	done bool
	list []InstalledPlugin
	err  error
}

// ListOnce wraps cli so Installed runs once until the next install or
// update. nil stays nil.
func ListOnce(cli PluginCLI) PluginCLI {
	if cli == nil {
		return nil
	}
	if _, ok := cli.(*listOnce); ok {
		return cli
	}
	return &listOnce{PluginCLI: cli}
}

func (l *listOnce) Installed(ctx context.Context, root string) ([]InstalledPlugin, error) {
	if !l.done {
		l.list, l.err = l.PluginCLI.Installed(ctx, root)
		l.done = true
	}
	return l.list, l.err
}

func (l *listOnce) Install(ctx context.Context, root, id string) error {
	l.done = false
	return l.PluginCLI.Install(ctx, root, id)
}

func (l *listOnce) Update(ctx context.Context, root, id string) error {
	l.done = false
	return l.PluginCLI.Update(ctx, root, id)
}

// pluginStatuses reads every listed plugin's install and contents. A nil
// cli, or one that cannot list (claude absent), yields the config rows
// with nothing installed and checked false.
func (p *Project) pluginStatuses(ctx context.Context, cli PluginCLI, cfg Config) (rows []PluginStatus, checked bool, err error) {
	var states []PluginState
	if cli != nil {
		states, err = p.pluginStates(ctx, cli, cfg)
		if err != nil && !errors.Is(err, ErrNoClaude) {
			return nil, false, err
		}
		checked = err == nil
	}
	enabled := map[string]any{}
	if s, err := readSettings(p.SettingsJSON()); err == nil {
		enabled, _ = s["enabledPlugins"].(map[string]any)
	}
	for i, pl := range cfg.Plugins {
		st := PluginStatus{ID: pl.ID(), Ref: pl.Ref, Allow: pl.Allow, Allowed: true}
		st.Enabled, _ = enabled[pl.ID()].(bool)
		if checked && states[i].Installed {
			st.Installed, st.Version, st.InstallPath = true, states[i].Version, states[i].InstallPath
			st.AtRef = states[i].OK()
			if st.InstallPath != "" {
				st.contents = InspectPlugin(st.InstallPath)
				st.Skills, st.ModelInvocable = len(st.contents.Skills), st.contents.ModelInvocable()
				st.Hooks, st.MCP = st.contents.Hooks != "", st.contents.MCP != ""
				st.Allowed = (!st.Hooks || pl.Allows(AllowHooks)) && (!st.MCP || pl.Allows(AllowMCP))
			}
		}
		rows = append(rows, st)
	}
	return rows, checked, nil
}

// hooksOrMCPFinding is the plugin.hooks-or-mcp finding for a row whose
// hooks or MCP servers its entry does not allow, else nil.
func hooksOrMCPFinding(st PluginStatus, ref PluginRef) *Finding {
	var carries, allow []string
	if st.Hooks && !ref.Allows(AllowHooks) {
		carries, allow = append(carries, "hooks ("+st.contents.Hooks+")"), append(allow, AllowHooks)
	}
	if st.MCP && !ref.Allows(AllowMCP) {
		carries, allow = append(carries, "MCP servers ("+st.contents.MCP+")"), append(allow, AllowMCP)
	}
	if len(carries) == 0 {
		return nil
	}
	allow = append(append([]string{}, ref.Allow...), allow...)
	return &Finding{File: configYAML, Message: "plugin " + st.ID + " carries " + strings.Join(carries, " and ") +
		", which its plugins: entry does not allow (a tool is a skill only) — a person reviews them and adds allow: [" +
		strings.Join(allow, ", ") + "] to the entry, or drops the plugin"}
}

// CheckPluginContents is `ape project check`'s plugin.hooks-or-mcp and
// tools.* findings (the drift and config findings are CheckPlugins'). A
// nil cli judges what needs no install: the tools schema and the project
// and user skills.
func (p *Project) CheckPluginContents(ctx context.Context, cli PluginCLI) []Finding {
	rep, err := p.pluginsReport(ctx, cli)
	if err != nil {
		return nil
	}
	return rep.Findings
}

// PluginsStatus is `ape plugins status`: every listed plugin (installed,
// version, enabled, its skills and what else it carries), every tools:
// entry (installed, slash-invocable), and the findings — CheckPlugins'
// and CheckPluginContents' together.
func (p *Project) PluginsStatus(ctx context.Context, cli PluginCLI) (*PluginsReport, error) {
	cli = ListOnce(cli)
	rep, err := p.pluginsReport(ctx, cli)
	if err != nil {
		return nil, err
	}
	rep.Findings = append(p.CheckPlugins(ctx, cli), rep.Findings...)
	return rep, nil
}

// pluginsReport builds the report with the contents and tools findings.
func (p *Project) pluginsReport(ctx context.Context, cli PluginCLI) (*PluginsReport, error) {
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return nil, err
	}
	rep := &PluginsReport{InstallsChecked: cli != nil, Plugins: []PluginStatus{}, Tools: []ToolStatus{}, Findings: []Finding{}}
	var rows []PluginStatus
	if len(pluginConfigErrors(cfg)) == 0 && len(cfg.Plugins) > 0 {
		var listErr error
		rows, rep.InstallsChecked, listErr = p.pluginStatuses(ctx, cli, cfg)
		if listErr != nil {
			// CheckPlugins reports the CLI's failure (plugins.drift).
			rows, _, _ = p.pluginStatuses(ctx, nil, cfg)
		}
		rep.Plugins = rows
		for i := range rows {
			if f := hooksOrMCPFinding(rows[i], cfg.Plugins[i]); f != nil {
				rep.Findings = append(rep.Findings, *f)
			}
		}
	}
	tools, fs := p.toolStatuses(cfg, rows, rep.InstallsChecked)
	rep.Tools = tools
	rep.Findings = append(rep.Findings, fs...)
	// v1.1 W6 (Q-E8): a user-scope copy stands in for the project's.
	rep.Findings = append(rep.Findings, p.userScopeFindings(ctx, cli, cfg, rep.InstallsChecked)...)
	return rep, nil
}
