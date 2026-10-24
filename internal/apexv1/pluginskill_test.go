package apexv1

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// F-E1-1: ape task resolves a plugin skill the way a tools: entry is
// resolved — its plugin listed in plugins:, installed, carrying the skill
// (by name or alias) — and says what is missing otherwise.
func TestPluginSkillResolver(t *testing.T) {
	p, cli := statusProject(t, pluginConfig)
	// A skill whose frontmatter name differs from its directory answers to both.
	writeTree(t, cli.paths["nats-expert@team-tools"], map[string]string{"skills/old-dir/SKILL.md": skillMD("bus", "")})
	delete(cli.installed, "linter@gh")
	resolve := p.PluginSkillResolver(ctx(), cli)

	require.NoError(t, resolve("nats-expert:nats"))
	require.NoError(t, resolve("nats-expert:bus"), "frontmatter name")
	require.NoError(t, resolve("nats-expert:old-dir"), "directory name, an alias")

	err := resolve("apish:nats-expert")
	require.EqualError(t, err, "no plugin apish in plugins: — list the plugin apish in plugins: and run `ape plugins sync`, or fix the skill name")

	err = resolve("linter:lint")
	require.EqualError(t, err, "plugin linter is not installed — list the plugin linter in plugins: and run `ape plugins sync`, or fix the skill name")

	err = resolve("nats-expert:nope")
	require.EqualError(t, err, "plugin nats-expert has no skill nope — fix the skill name, or install a version of nats-expert that ships it")

	require.Equal(t, []string{"list"}, cli.calls, "the installs are read once, on the first name asked")

	// Without claude the installs cannot be read: refused, saying so.
	err = p.PluginSkillResolver(ctx(), nil)("nats-expert:nats")
	require.ErrorContains(t, err, "claude is not on PATH: the installs were not read")
}

// No plugin listed: every plugin skill is unlisted, and the CLI is not asked.
func TestPluginSkillResolver_NoPluginsListed(t *testing.T) {
	p, cli := statusProject(t, "project_name: x\n")
	err := p.PluginSkillResolver(ctx(), cli)("apish:nats-expert")
	require.ErrorContains(t, err, "no plugin apish in plugins:")
	require.Empty(t, cli.calls)
}

// resolveTool's plugin branch is PluginSkill: for every case `ape project
// check` and `ape task` reach the same verdict, in the same words.
func TestPluginSkill_ResolveToolAgrees(t *testing.T) {
	p, cli := statusProject(t, pluginConfig)
	delete(cli.installed, "linter@gh")
	cfg, err := LoadConfig(p.Root)
	require.NoError(t, err)
	for _, checked := range []bool{true, false} {
		var c PluginCLI = cli
		if !checked {
			c = nil
		}
		rows, ok, err := p.pluginStatuses(ctx(), c, cfg)
		require.NoError(t, err)
		require.Equal(t, checked, ok)
		resolve := p.PluginSkillResolver(ctx(), c)
		for _, name := range []string{"nats-expert:nats", "nats-expert:setup", "nats-expert:nope", "linter:lint", "apish:nats-expert"} {
			st := p.resolveTool(cfg, rows, ok, "review", Tool{Name: "t", Skill: name})
			s, state, why := PluginSkill(cfg, rows, ok, name)
			require.Equal(t, state, st.State, name)
			rerr := resolve(name)
			if state == ToolOK {
				require.Equal(t, s.Path, st.Path, name)
				require.NoError(t, rerr, name)
				continue
			}
			require.Equal(t, why, st.Path, name)
			require.ErrorContains(t, rerr, why, name)
		}
	}
	// The found skill is the plugin's file.
	rows, ok, err := p.pluginStatuses(ctx(), cli, cfg)
	require.NoError(t, err)
	s, state, _ := PluginSkill(cfg, rows, ok, "nats-expert:nats")
	require.Equal(t, ToolOK, state)
	require.Equal(t, filepath.Join(cli.paths["nats-expert@team-tools"], "skills", "nats", "SKILL.md"), s.Path)
}
