package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeTree writes files (relative path → body) under root.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
}

func skillMD(name, extra string) string {
	return "---\nname: " + name + "\ndescription: " + name + "\n" + extra + "---\nBody.\n"
}

// isolatedClaudeDir points CLAUDE_CONFIG_DIR and HOME at temp dirs, so no
// test reads the real ~/.claude, and returns the config dir.
func isolatedClaudeDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, "config"))
	t.Setenv("HOME", filepath.Join(base, "home"))
	return filepath.Join(base, "config")
}

func TestInspectPlugin_SkillsHooksMCP(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"skills/a/SKILL.md":           skillMD("a", ""),
		"skills/b/SKILL.md":           skillMD("b", "disable-model-invocation: true\n"),
		"skills/c/SKILL.md":           skillMD("c", "user-invocable: false\n"),
		"skills/not-a-skill/notes.md": "x",
		"commands/deploy.md":          "---\ndescription: deploy\ndisable-model-invocation: \"true\"\n---\n",
		"commands/sub/lint.md":        "Lint it.\n",
		"extra/x/SKILL.md":            skillMD("renamed", ""),
		".claude-plugin/plugin.json":  `{"name":"p","skills":"./extra","mcpServers":{"db":{"command":"db"}}}`,
		"hooks/hooks.json":            `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"x"}]}]}}`,
	})
	pc := InspectPlugin(root)
	names := make([]string, 0, len(pc.Skills))
	for _, s := range pc.Skills {
		names = append(names, s.Name)
	}
	require.Equal(t, []string{"a", "b", "c", "deploy", "lint", "renamed"}, names)
	require.Equal(t, 4, pc.ModelInvocable(), "a, c, lint, renamed: b and deploy opt out")
	require.Equal(t, "hooks/hooks.json", pc.Hooks)
	require.Equal(t, "plugin.json mcpServers", pc.MCP)
	for _, s := range pc.Skills {
		require.Equal(t, s.Name != "c", s.UserInvocable, s.Name)
		if s.Name == "renamed" {
			require.True(t, s.answersTo("x"), "the directory name still names it")
		}
	}

	// An empty hooks.json is no hooks; .mcp.json counts; plugin.json hooks counts.
	bare := t.TempDir()
	writeTree(t, bare, map[string]string{
		"hooks/hooks.json": `{"hooks":{}}`,
		".mcp.json":        `{"mcpServers":{"kb":{"url":"http://x"}}}`,
	})
	pc = InspectPlugin(bare)
	require.Empty(t, pc.Hooks)
	require.Equal(t, ".mcp.json", pc.MCP)
	require.Empty(t, pc.Skills)
	writeTree(t, bare, map[string]string{".claude-plugin/plugin.json": `{"hooks":"./more-hooks.json"}`, ".mcp.json": `{}`})
	pc = InspectPlugin(bare)
	require.Equal(t, "plugin.json hooks", pc.Hooks)
	require.Empty(t, pc.MCP)
}

// statusProject is a plugin project whose two plugins are installed in
// temp trees: nats-expert carries a hook, linter an MCP server.
func statusProject(t *testing.T, config string) (*Project, *fakePluginCLI) {
	t.Helper()
	isolatedClaudeDir(t)
	p := settingsProject(t, `{"enabledPlugins": {"nats-expert@team-tools": true}}`)
	writeConfigForTest(t, p.Root, config)
	cache := t.TempDir()
	nats, linter := filepath.Join(cache, "nats"), filepath.Join(cache, "linter")
	writeTree(t, nats, map[string]string{
		"skills/nats/SKILL.md":  skillMD("nats", ""),
		"skills/setup/SKILL.md": skillMD("setup", "disable-model-invocation: true\n"),
		"hooks/hooks.json":      `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}`,
	})
	writeTree(t, linter, map[string]string{
		"skills/lint/SKILL.md":   skillMD("lint", ""),
		"skills/hidden/SKILL.md": skillMD("hidden", "user-invocable: false\n"),
		".mcp.json":              `{"lintd":{"command":"lintd"}}`,
	})
	cli := &fakePluginCLI{
		installed: map[string]string{"nats-expert@team-tools": "1.4.0", "linter@gh": "2"},
		paths:     map[string]string{"nats-expert@team-tools": nats, "linter@gh": linter},
	}
	return p, cli
}

func TestPluginsStatus_CountsAndHooksOrMCP(t *testing.T) {
	p, cli := statusProject(t, pluginConfig)
	rep, err := p.PluginsStatus(ctx(), cli)
	require.NoError(t, err)
	require.True(t, rep.InstallsChecked)
	require.Len(t, rep.Plugins, 2)
	nats, linter := rep.Plugins[0], rep.Plugins[1]
	require.Equal(t, "nats-expert@team-tools", nats.ID)
	require.True(t, nats.Installed)
	require.True(t, nats.AtRef)
	require.True(t, nats.Enabled)
	require.Equal(t, 2, nats.Skills)
	require.Equal(t, 1, nats.ModelInvocable)
	require.True(t, nats.Hooks)
	require.False(t, nats.MCP)
	require.False(t, nats.Allowed)
	require.False(t, linter.Enabled, "settings.json does not enable it")
	require.Equal(t, 2, linter.ModelInvocable, "user-invocable: false is still model-invocable")
	require.True(t, linter.MCP)

	msgs := findingMessages(rep.Findings)
	require.Contains(t, msgs, "plugin nats-expert@team-tools carries hooks (hooks/hooks.json), which its plugins: entry does not allow")
	require.Contains(t, msgs, "adds allow: [hooks] to the entry")
	require.Contains(t, msgs, "plugin linter@gh carries MCP servers (.mcp.json)")
	codes := codesOf(rep.Findings)
	require.Contains(t, codes, "plugin.hooks-or-mcp")
	require.Contains(t, codes, "plugins.drift", "the drift check runs too: settings.json lacks the marketplaces")
	require.Equal(t, ActionPerson, ActionFor("plugin.hooks-or-mcp"))

	// project check's half: the contents findings only, from one list call.
	cli.calls = nil
	once := ListOnce(cli)
	_ = p.CheckPlugins(ctx(), once)
	fs := p.CheckPluginContents(ctx(), once)
	require.Equal(t, []string{"plugin.hooks-or-mcp", "plugin.hooks-or-mcp"}, codesOf(fs))
	require.Equal(t, []string{"list"}, cli.calls, "ListOnce: one claude plugin list per command")

	// Allowed per kind: the findings go, the flags stay.
	allowed := strings.Replace(pluginConfig, "ref: v1.4.0}", "ref: v1.4.0, allow: [hooks]}", 1)
	allowed = strings.Replace(allowed, "marketplace: gh}", "marketplace: gh, allow: [mcp, hooks]}", 1)
	writeConfigForTest(t, p.Root, allowed)
	rep, err = p.PluginsStatus(ctx(), cli)
	require.NoError(t, err)
	require.NotContains(t, codesOf(rep.Findings), "plugin.hooks-or-mcp")
	require.True(t, rep.Plugins[0].Hooks)
	require.True(t, rep.Plugins[0].Allowed)

	// Allowing the wrong kind keeps the finding.
	writeConfigForTest(t, p.Root, strings.Replace(pluginConfig, "ref: v1.4.0}", "ref: v1.4.0, allow: [mcp]}", 1))
	fs = p.CheckPluginContents(ctx(), cli)
	require.Contains(t, findingMessages(fs), "plugin nats-expert@team-tools carries hooks")
	require.Contains(t, findingMessages(fs), "adds allow: [mcp, hooks]")

	// Without claude: nothing installed is judged.
	rep, err = p.PluginsStatus(ctx(), nil)
	require.NoError(t, err)
	require.False(t, rep.InstallsChecked)
	require.False(t, rep.Plugins[0].Installed)
	require.NotContains(t, codesOf(rep.Findings), "plugin.hooks-or-mcp")
}

// An allow: value other than hooks or mcp is a config error.
func TestPluginConfig_UnknownAllow(t *testing.T) {
	p, cli := statusProject(t, strings.Replace(pluginConfig, "marketplace: gh}", "marketplace: gh, allow: [hooks, lsp]}", 1))
	fs := p.CheckPlugins(ctx(), cli)
	require.Equal(t, []string{"plugins.config"}, codesOf(fs))
	require.Contains(t, findingMessages(fs), `plugins[1] (linter): allow "lsp" is not hooks or mcp`)
	_, err := p.SyncPlugins(ctx(), cli, SyncOptions{})
	var fe *FailError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, "plugins.config", fe.Code)
}

// ape plugins sync: install, write settings, commit "apex: plugins"; a
// second run changes and commits nothing.
func TestSyncPlugins_CommitIdempotent(t *testing.T) {
	p := fixture(t, map[string]string{
		"_apex/config.yaml":     pluginConfig,
		".claude/settings.json": `{"model": "opus"}` + "\n",
	})
	cli := &fakePluginCLI{
		serves:    map[string]string{"nats-expert@team-tools": "1.4.0", "linter@gh": "2"},
		installed: map[string]string{},
	}
	res, err := p.SyncPlugins(ctx(), cli, SyncOptions{WriteSettings: true, Commit: true})
	require.NoError(t, err)
	require.Len(t, res.Actions, 2)
	require.NotEmpty(t, res.Commit)
	require.Equal(t, "apex: plugins", run(t, p.Root, "log", "-1", "--format=%s"))
	require.Equal(t, ".claude/settings.json", run(t, p.Root, "show", "--name-only", "--format=", "HEAD"))

	head := run(t, p.Root, "rev-parse", "HEAD")
	res, err = p.SyncPlugins(ctx(), cli, SyncOptions{WriteSettings: true, Commit: true})
	require.NoError(t, err)
	require.Empty(t, res.Actions)
	require.Empty(t, res.Settings)
	require.Empty(t, res.Commit)
	require.Equal(t, head, run(t, p.Root, "rev-parse", "HEAD"), "a second sync commits nothing")
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
}
