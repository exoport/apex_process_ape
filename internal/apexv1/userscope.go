package apexv1

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// plugins.user-scope (v1.1 W6, Q-E8). ape installs every listed plugin at
// project scope, pinned by the project's ref, and a tools: skill resolves
// a user skill before a project one, as Claude Code does. So a copy at
// user scope — a plugin of the same name the person installed or enabled
// for themselves, or a user skill a tools: entry names — silently stands
// in for the project's on this machine, at whatever version it has. Each
// is a finding (action person) of `ape plugins status` and `ape project
// check`; ape never removes it.
//
// User scope is what Claude Code reads for the person: `claude plugin
// list --json` rows of scope "user" (when the installs were read), the
// enabledPlugins of <config dir>/settings.json, and <config
// dir>/skills/<name>/SKILL.md or commands/<name>.md ($CLAUDE_CONFIG_DIR,
// else ~/.claude).

// pluginName is a plugin id's name, the part before @.
func pluginName(id string) string {
	name, _, _ := strings.Cut(id, "@")
	return name
}

// userScopeFindings are the plugins.user-scope findings. cli (nil: none)
// lists the installs when checked; the user settings and skills are read
// whether or not it can.
func (p *Project) userScopeFindings(ctx context.Context, cli PluginCLI, cfg Config, checked bool) []Finding {
	dir := claudeConfigDir()
	if dir == "" {
		return nil
	}
	var out []Finding
	if len(cfg.Plugins) > 0 && len(pluginConfigErrors(cfg)) == 0 {
		out = append(out, p.userScopePlugins(ctx, cli, cfg, checked, dir)...)
	}
	seen := map[string]bool{}
	for _, slot := range AllToolSlots {
		tools := cfg.Tools.Slot(slot)
		for i := range tools {
			t := &tools[i]
			if strings.Contains(t.Skill, ":") || seen[t.Skill] {
				continue
			}
			seen[t.Skill] = true
			user := userSkillPath(dir, t.Skill)
			if user == "" {
				continue
			}
			label := "skill " + t.Skill + " of tool " + slot + "/" + t.Name
			if userSkillPath(filepath.Join(p.liveRoot(), ".claude"), t.Skill) != "" {
				out = append(out, Finding{File: configYAML, Message: label + " is also installed at user scope (" + user +
					"), which wins over the project's copy — a person removes the user copy, or renames one of them"})
				continue
			}
			into := ".claude/skills/" + t.Skill + "/"
			if user == filepath.Join(dir, "commands", t.Skill+".md") {
				into = ".claude/commands/" + t.Skill + ".md"
			}
			out = append(out, Finding{File: configYAML, Message: label + " is installed at user scope only (" + user +
				"), so it is this machine's, unpinned — a person moves it into the project's " + into + " (committed) and removes the user copy"})
		}
	}
	return out
}

// userScopePlugins is the finding for each listed plugin with a user-scope
// copy: a user install of the same name, or one enabled in the user's
// settings.json.
func (p *Project) userScopePlugins(ctx context.Context, cli PluginCLI, cfg Config, checked bool, dir string) []Finding {
	var installs []InstalledPlugin
	if cli != nil && checked {
		if list, err := cli.Installed(ctx, p.liveRoot()); err == nil {
			for _, ip := range list {
				if ip.Scope == "user" {
					installs = append(installs, ip)
				}
			}
		}
	}
	settingsPath := filepath.Join(dir, "settings.json")
	var enabled []string
	if s, err := readSettings(settingsPath); err == nil {
		m, _ := s["enabledPlugins"].(map[string]any)
		for id, v := range m {
			if on, _ := v.(bool); on {
				enabled = append(enabled, id)
			}
		}
		sort.Strings(enabled)
	}
	var out []Finding
	for _, pl := range cfg.Plugins {
		var where, ids []string
		for _, ip := range installs {
			if pluginName(ip.ID) == pl.Name {
				where = append(where, ip.ID+" installed at user scope (claude plugin list)")
				ids = append(ids, ip.ID)
			}
		}
		for _, id := range enabled {
			if pluginName(id) == pl.Name {
				where = append(where, id+" enabled in "+settingsPath)
				if !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
			}
		}
		if len(where) == 0 {
			continue
		}
		fixes := make([]string, 0, len(ids))
		for _, id := range ids {
			fixes = append(fixes, "claude plugin uninstall "+id+" --scope user")
		}
		out = append(out, Finding{File: configYAML, Message: "plugin " + pl.ID() + " is also installed at user scope: " + strings.Join(where, "; ") +
			" — a user copy is not pinned by the project's ref and its skills stand in for the project's: a person removes it (" +
			strings.Join(fixes, " ; ") + ", or drops it from enabledPlugins in " + settingsPath + ")"})
	}
	return out
}

// userSkillPath is the skill (skills/<name>/SKILL.md) or command
// (commands/<name>.md) name under a .claude-shaped dir, "" when none.
func userSkillPath(dir, name string) string {
	for _, path := range []string{filepath.Join(dir, "skills", name, "SKILL.md"), filepath.Join(dir, "commands", name+".md")} {
		if fileExists(path) {
			return path
		}
	}
	return ""
}
