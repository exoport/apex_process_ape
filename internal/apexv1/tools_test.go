package apexv1

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToolsConfig_Schema(t *testing.T) {
	isolatedClaudeDir(t)
	p := settingsProject(t, "")
	writeConfigForTest(t, p.Root, `tools:
  design:
    - {name: mock, skill: mockups, when: "a screen changes"}
    - {name: mock, skill: other, when: x}
    - {name: bad, skill: /slash, when: x}
    - {skill: s, when: x}
    - {name: extra, skill: s, when: x, mcp: srv}
    - just-a-string
    - {name: tmpl, skill: s, when: x, args: "{change} {nope}"}
    - {name: eff, skill: s, when: x, effort: huge}
    - {name: full, skill: s, when: x, args: "{id} --task {task} --out {assets} {change}", model: sonnet, effort: high}
  review:
    - {name: sec, skill: "kit:security", when: "auth"}
    - {name: perf, skill: perf}
  deploy:
    - {name: d, skill: d, when: d}
`)
	cfg, err := LoadConfig(p.Root)
	require.NoError(t, err, "a bad tools: block never makes the config unparseable")
	require.Equal(t, []Tool{
		{Name: "mock", Skill: "mockups", When: "a screen changes"},
		{Name: "full", Skill: "s", When: "x", Args: "{id} --task {task} --out {assets} {change}", Model: "sonnet", Effort: "high"},
	}, cfg.Tools.Design)
	require.Equal(t, []Tool{{Name: "sec", Skill: "kit:security", When: "auth"}}, cfg.Tools.Review)

	fs := p.CheckPluginContents(ctx(), nil)
	msgs := findingMessages(fs)
	for _, want := range []string{
		`tools.design[1]: name "mock" is already used by tools.design[0]`,
		`tools.design[2]: skill "/slash" is not a skill name`,
		`tools.design[3]: missing name`,
		`tools.design[4]: unknown key "mcp"`,
		`tools.design[5]: is not a mapping`,
		`tools.design[6]: args "{change} {nope}": unknown placeholder {nope}`,
		`tools.design[7]: effort "huge" is not one of low, medium, high, xhigh, max`,
		`tools.review[1]: missing when`,
		`tools.deploy: unknown slot`,
	} {
		require.Contains(t, msgs, want)
	}
	codes := codesOf(fs)
	require.Equal(t, 9, strings.Count(strings.Join(codes, " "), "tools.invalid"))
	require.Equal(t, ActionPerson, ActionFor("tools.invalid"))

	for _, bad := range []string{"tools: [a, b]\n", "tools: {design: x}\n"} {
		writeConfigForTest(t, p.Root, bad)
		_, err := LoadConfig(p.Root)
		require.NoError(t, err)
		require.Equal(t, []string{"tools.invalid"}, codesOf(p.CheckPluginContents(ctx(), nil)), bad)
	}
	writeConfigForTest(t, p.Root, "tools:\n  review:\n")
	require.Empty(t, p.CheckPluginContents(ctx(), nil))
}

// Each named skill must be installed and slash-invocable: a plugin skill
// of a listed, installed plugin; a project skill; a user skill (which
// wins over a project skill of the same name).
func TestTools_InstalledAndInvocable(t *testing.T) {
	config := pluginConfig + `tools:
  design:
    - {name: n, skill: "nats-expert:nats", when: a}
    - {name: setup, skill: "nats-expert:setup", when: a}
    - {name: hidden, skill: "linter:hidden", when: a}
    - {name: gone, skill: "nats-expert:gone", when: a}
    - {name: unlisted, skill: "other:x", when: a}
  review:
    - {name: proj, skill: proj-lens, when: a}
    - {name: user, skill: user-lens, when: a}
    - {name: cmd, skill: legacy, when: a}
    - {name: both, skill: shared, when: a}
    - {name: missing, skill: nowhere, when: a}
`
	p, cli := statusProject(t, config)
	cfgDir := isolatedClaudeDir(t)
	writeTree(t, p.Root, map[string]string{
		".claude/skills/proj-lens/SKILL.md": skillMD("proj-lens", ""),
		".claude/commands/legacy.md":        "Run the legacy review.\n",
		".claude/skills/shared/SKILL.md":    skillMD("shared", ""),
	})
	writeTree(t, cfgDir, map[string]string{
		"skills/user-lens/SKILL.md": skillMD("user-lens", "disable-model-invocation: true\n"),
		"skills/shared/SKILL.md":    skillMD("shared", "user-invocable: false\n"),
	})
	rep, err := p.PluginsStatus(ctx(), cli)
	require.NoError(t, err)
	state := map[string]ToolStatus{}
	for _, ts := range rep.Tools {
		state[ts.Name] = ts
	}
	require.Equal(t, ToolOK, state["n"].State)
	require.Equal(t, "plugin", state["n"].Source)
	require.Equal(t, ToolOK, state["setup"].State, "disable-model-invocation does not stop a slash command")
	require.Equal(t, ToolNotInvocable, state["hidden"].State)
	require.Equal(t, ToolNotInstalled, state["gone"].State)
	require.Equal(t, ToolNotInstalled, state["unlisted"].State)
	require.Equal(t, ToolOK, state["proj"].State)
	require.Equal(t, "project", state["proj"].Source)
	require.Equal(t, ToolOK, state["user"].State)
	require.Equal(t, "user", state["user"].Source)
	require.Equal(t, ToolOK, state["cmd"].State)
	require.Equal(t, ToolNotInvocable, state["both"].State, "the user skill wins")
	require.Equal(t, "user", state["both"].Source)
	require.Equal(t, ToolNotInstalled, state["missing"].State)

	fs := p.CheckPluginContents(ctx(), cli)
	msgs := findingMessages(fs)
	require.Contains(t, msgs, "tool design/hidden: skill linter:hidden is not slash-invocable (user-invocable: false in ")
	require.Contains(t, msgs, "tool design/unlisted: skill other:x is not installed (no plugin other in plugins:) — list the plugin other in plugins: and run `ape plugins sync`")
	require.Contains(t, msgs, "tool review/missing: skill nowhere is not installed")
	codes := strings.Join(codesOf(fs), " ")
	require.Equal(t, 3, strings.Count(codes, "tools.not-installed"), codes) // gone, unlisted, missing
	require.Equal(t, 2, strings.Count(codes, "tools.not-invocable"), codes)

	// Without claude, a plugin skill is unchecked, never a finding.
	rep, err = p.PluginsStatus(ctx(), nil)
	require.NoError(t, err)
	for _, ts := range rep.Tools {
		if ts.Name == "n" || ts.Name == "hidden" {
			require.Equal(t, ToolUnchecked, ts.State, ts.Name)
		}
	}
}

func TestChildSkillAllowList(t *testing.T) {
	p := settingsProject(t, "")
	require.Equal(t, []string{"apex-plan", "apex-change", "apex-apply", "apex-review", "apex-release", "apex-accept"}, p.ChildSkillAllowList(Config{}))
	writeConfigForTest(t, p.Root, `tools:
  review:
    - {name: sec, skill: "kit:security", when: a}
    - {name: again, skill: apex-review, when: a}
  design:
    - {name: mock, skill: mockups, when: a}
    - {name: mock2, skill: mockups, when: b}
    - {name: broken, skill: "/x", when: a}
`)
	cfg, err := LoadConfig(p.Root)
	require.NoError(t, err)
	got := p.ChildSkillAllowList(cfg)
	require.Equal(t, []string{"apex-plan", "apex-change", "apex-apply", "apex-review", "apex-release", "apex-accept", "mockups", "kit:security"}, got)
	require.NotContains(t, got, "apex-handoff")
	require.NotContains(t, got, "apex-orchestrator")
}

// F7: a tools: skill may be a local Claude Code command,
// commands/<name>.md, at project or user scope. ape looks under the
// user's .claude, then the project's, and in each for the skill before the
// command; the first found wins. An advise entry on a command resolves
// load: against the whole commands/ folder. A skill found nowhere names
// both places.
func TestTools_LocalCommands(t *testing.T) {
	p, cli := statusProject(t, pluginConfig+`tools:
  review:
    - {name: projcmd, skill: mylint, when: a}
    - {name: usercmd, skill: myfmt, when: a}
    - {name: mixed, skill: dual, when: a}
    - {name: missing, skill: nowhere, when: a}
  advise:
    - {name: rules, skill: mylint, when: a, load: [advise.md]}
`)
	cfgDir := isolatedClaudeDir(t)
	writeTree(t, p.Root, map[string]string{
		".claude/commands/mylint.md":   "Lint the change.\n",
		".claude/commands/advise.md":   "Rules.\n",
		".claude/skills/dual/SKILL.md": skillMD("dual", ""),
	})
	writeTree(t, cfgDir, map[string]string{
		"commands/myfmt.md": "Format the change.\n",
		"commands/dual.md":  "The user's dual.\n",
	})
	rep, err := p.PluginsStatus(ctx(), cli)
	require.NoError(t, err)
	state := map[string]ToolStatus{}
	for _, ts := range rep.Tools {
		state[ts.Name] = ts
	}
	projCmds := filepath.Join(p.liveRoot(), ".claude", "commands")

	require.Equal(t, ToolOK, state["projcmd"].State)
	require.Equal(t, "project", state["projcmd"].Source)
	require.Equal(t, filepath.Join(projCmds, "mylint.md"), state["projcmd"].Path)

	require.Equal(t, ToolOK, state["usercmd"].State)
	require.Equal(t, "user", state["usercmd"].Source)
	require.Equal(t, filepath.Join(cfgDir, "commands", "myfmt.md"), state["usercmd"].Path)

	require.Equal(t, "user", state["mixed"].Source, "a user command wins over a project skill of the same name")
	require.Equal(t, filepath.Join(cfgDir, "commands", "dual.md"), state["mixed"].Path)

	require.Equal(t, ToolNotInstalled, state["missing"].State)
	require.Equal(t, "no skills/nowhere/SKILL.md or commands/nowhere.md in the project's or the user's .claude", state["missing"].Path)

	rules := state["rules"]
	require.Equal(t, ToolOK, rules.State)
	require.Len(t, rules.Files, 1)
	require.Equal(t, filepath.Join(projCmds, "advise.md"), rules.Files[0].Path, "load: resolves against the whole commands/ folder")
	require.Empty(t, rules.Files[0].Error)

	msgs := findingMessages(p.CheckPluginContents(ctx(), cli))
	require.Contains(t, msgs, "tool review/missing: skill nowhere is not installed (no skills/nowhere/SKILL.md or commands/nowhere.md in the project's or the user's .claude)"+
		" — add .claude/skills/nowhere/SKILL.md or .claude/commands/nowhere.md (or a user skill or command), or fix the skill name")
	require.NotContains(t, msgs, "tool review/projcmd")
	require.NotContains(t, msgs, "tool advise/rules")
	require.Contains(t, msgs, "skill myfmt of tool review/usercmd is installed at user scope only ("+filepath.Join(cfgDir, "commands", "myfmt.md")+
		"), so it is this machine's, unpinned — a person moves it into the project's .claude/commands/myfmt.md (committed) and removes the user copy")
}
