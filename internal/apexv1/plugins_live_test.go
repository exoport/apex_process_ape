package apexv1

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The one test against the real `claude plugin` CLI: a local directory
// marketplace, an isolated CLAUDE_CONFIG_DIR (and HOME), a temp project.
// Skipped where claude is not installed (CI) and under -short.
func TestSyncPlugins_RealClaudeCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude is not on PATH")
	}
	base := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, "config"))
	t.Setenv("HOME", filepath.Join(base, "home"))
	for _, k := range []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ID"} {
		t.Setenv(k, "")
	}
	mkt := filepath.Join(base, "mkt")
	plugin := filepath.Join(mkt, "plugins", "nats-expert")
	writeAll := func(files map[string]string) {
		for path, body := range files {
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
		}
	}
	setVersion := func(v string) {
		writeAll(map[string]string{filepath.Join(plugin, ".claude-plugin", "plugin.json"): `{"name":"nats-expert","version":"` + v + `","description":"x"}`})
	}
	writeAll(map[string]string{
		filepath.Join(mkt, ".claude-plugin", "marketplace.json"): `{"name":"team-tools","owner":{"name":"t"},` +
			`"plugins":[{"name":"nats-expert","source":"./plugins/nats-expert","description":"x"}]}`,
		filepath.Join(plugin, "skills", "hello", "SKILL.md"): "---\nname: hello\ndescription: say hi\n---\nSay hi.\n",
	})
	setVersion("1.4.0")

	project := filepath.Join(base, "project")
	require.NoError(t, os.MkdirAll(project, 0o755))
	p, err := Open(project)
	require.NoError(t, err)
	config := func(ref string) {
		writeConfigForTest(t, project, "marketplaces:\n  team-tools: {source: directory, path: "+mkt+"}\n"+
			"plugins:\n  - {name: nats-expert, marketplace: team-tools, ref: "+ref+"}\n")
	}
	cli := ClaudePluginCLI{}

	config("v1.4.0")
	require.NotEmpty(t, p.CheckPlugins(ctx(), cli), "nothing installed yet")
	res, err := p.SyncPlugins(ctx(), cli, SyncOptions{WriteSettings: true})
	require.NoError(t, err)
	require.Equal(t, []string{"installed nats-expert@team-tools"}, res.Actions)
	require.Empty(t, p.CheckPlugins(ctx(), cli), findingMessages(p.CheckPlugins(ctx(), cli)))
	settings, err := os.ReadFile(p.SettingsJSON())
	require.NoError(t, err)
	require.Contains(t, string(settings), `"nats-expert@team-tools": true`)

	// The marketplace moves on and the config pins the new version:
	// preflight mode updates it and leaves settings.json as it was.
	setVersion("1.5.0")
	config("v1.5.0")
	res, err = p.SyncPlugins(ctx(), cli, SyncOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"updated nats-expert@team-tools"}, res.Actions)
	after, err := os.ReadFile(p.SettingsJSON())
	require.NoError(t, err)
	require.Equal(t, string(settings), string(after))
	require.Empty(t, p.CheckPlugins(ctx(), cli))

	// A ref the marketplace does not serve: refused, with the command.
	config("v2.0.0")
	_, err = p.SyncPlugins(ctx(), cli, SyncOptions{})
	var fe *FailError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, "plugins.missing", fe.Code)
	require.Contains(t, fe.Msg, "is at version 1.5.0, the config pins ref v2.0.0")
	require.Contains(t, fe.Msg, "claude plugin update nats-expert@team-tools --scope project")
}
