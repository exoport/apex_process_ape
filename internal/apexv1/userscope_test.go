package apexv1

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// userScopeCLI is a fakePluginCLI that also lists user-scope installs.
type userScopeCLI struct {
	*fakePluginCLI
	user []string
}

func (u userScopeCLI) Installed(c context.Context, root string) ([]InstalledPlugin, error) {
	list, err := u.fakePluginCLI.Installed(c, root)
	for _, id := range u.user {
		list = append(list, InstalledPlugin{ID: id, Version: "0.0.1", Scope: "user"})
	}
	return list, err
}

// W6 (Q-E8): a listed plugin also installed or enabled at user scope, and
// a tools: skill that is a user skill, are plugins.user-scope findings —
// the user copy wins over the project's on this machine. Every read is of
// the temp CLAUDE_CONFIG_DIR, never the real ~/.claude.
func TestPluginsStatus_UserScope(t *testing.T) {
	p, cli := statusProject(t, pluginConfig+`tools:
  review:
    - {name: both, skill: dup-lens, when: a}
    - {name: mine, skill: my-lens, when: a}
    - {name: proj, skill: proj-lens, when: a}
  advise:
    - {name: also, skill: dup-lens, when: a, load: [SKILL.md]}
`)
	cfgDir := isolatedClaudeDir(t)
	writeTree(t, cfgDir, map[string]string{
		"skills/dup-lens/SKILL.md": skillMD("dup-lens", ""),
		"commands/my-lens.md":      skillMD("my-lens", ""),
		"settings.json":            `{"enabledPlugins": {"nats-expert@someone-else": true, "linter@gh": false, "unrelated@x": true}}`,
	})
	writeTree(t, p.Root, map[string]string{
		".claude/skills/dup-lens/SKILL.md":  skillMD("dup-lens", ""),
		".claude/skills/proj-lens/SKILL.md": skillMD("proj-lens", ""),
	})
	rep, err := p.PluginsStatus(ctx(), userScopeCLI{cli, []string{"linter@gh", "nats-expert@someone-else"}})
	require.NoError(t, err)
	var got []Finding
	for _, f := range ClassifyFindings(rep.Findings) {
		if f.Code == CodePluginsUserScope {
			f.Message, _ = splitFix(f.Message)
			got = append(got, f)
		}
	}
	settings := filepath.Join(cfgDir, "settings.json")
	require.Len(t, got, 4, findingMessages(rep.Findings))
	require.Equal(t, "plugin nats-expert@team-tools is also installed at user scope: nats-expert@someone-else installed at user scope (claude plugin list); nats-expert@someone-else enabled in "+settings, got[0].Message)
	require.Equal(t, "a user copy is not pinned by the project's ref and its skills stand in for the project's: a person removes it (claude plugin uninstall nats-expert@someone-else --scope user, or drops it from enabledPlugins in "+settings+")", got[0].Fix)
	require.Equal(t, "plugin linter@gh is also installed at user scope: linter@gh installed at user scope (claude plugin list)", got[1].Message, "enabledPlugins false is not enabled")
	require.Equal(t, "skill dup-lens of tool review/both is also installed at user scope ("+filepath.Join(cfgDir, "skills", "dup-lens", "SKILL.md")+"), which wins over the project's copy", got[2].Message)
	require.Equal(t, "skill my-lens of tool review/mine is installed at user scope only ("+filepath.Join(cfgDir, "commands", "my-lens.md")+"), so it is this machine's, unpinned", got[3].Message)
	require.Equal(t, ActionPerson, ActionFor(CodePluginsUserScope))

	// Without the CLI the user settings are still read.
	rep, err = p.PluginsStatus(ctx(), nil)
	require.NoError(t, err)
	require.Contains(t, findingMessages(rep.Findings), "plugin nats-expert@team-tools is also installed at user scope: nats-expert@someone-else enabled in "+settings)
	require.Contains(t, codesOf(p.CheckPluginContents(ctx(), nil)), CodePluginsUserScope, "project check reports it too")
}
