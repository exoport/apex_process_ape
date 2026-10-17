package apexv1

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakePluginCLI is the `claude plugin` CLI as a map: serves is what each
// marketplace hands out (id → version), installed what this project has.
type fakePluginCLI struct {
	serves    map[string]string
	installed map[string]string
	calls     []string
	failAdd   bool
	// paths is each installed plugin's installPath.
	paths map[string]string
	// settingsWrite mimics the CLI rewriting settings.json.
	settingsWrite bool
}

func (f *fakePluginCLI) Installed(_ context.Context, root string) ([]InstalledPlugin, error) {
	f.calls = append(f.calls, "list")
	out := make([]InstalledPlugin, 0, len(f.installed)+1)
	for id, v := range f.installed {
		out = append(out, InstalledPlugin{ID: id, Version: v, Scope: "project", ProjectPath: root, InstallPath: f.paths[id]})
	}
	out = append(out, InstalledPlugin{ID: "other@x", Version: "1", Scope: "project", ProjectPath: "/elsewhere"})
	return out, nil
}

func (f *fakePluginCLI) AddMarketplace(_ context.Context, root, source string) error {
	f.calls = append(f.calls, "add "+source)
	if f.failAdd {
		return errors.New("claude plugin marketplace add: unreachable")
	}
	if f.settingsWrite {
		_ = os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
		_ = os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(`{"written":"by the cli"}`), 0o644)
	}
	return nil
}

func (f *fakePluginCLI) UpdateMarketplace(_ context.Context, _, name string) error {
	f.calls = append(f.calls, "refresh "+name)
	return nil
}

func (f *fakePluginCLI) Install(_ context.Context, _, id string) error {
	f.calls = append(f.calls, "install "+id)
	v, ok := f.serves[id]
	if !ok {
		return errors.New("claude plugin install " + id + `: Plugin not found in marketplace`)
	}
	f.installed[id] = v
	return nil
}

func (f *fakePluginCLI) Update(_ context.Context, _, id string) error {
	f.calls = append(f.calls, "update "+id)
	f.installed[id] = f.serves[id]
	return nil
}

const pluginConfig = `project_name: x
marketplaces:
  team-tools: {source: directory, path: ../mkt}
  gh: {source: github, repo: org/claude-plugins, ref: stable}
plugins:
  - {name: nats-expert, marketplace: team-tools, ref: v1.4.0}
  - {name: linter, marketplace: gh}
`

func pluginProject(t *testing.T) *Project {
	t.Helper()
	p := settingsProject(t, `{"model": "opus", "enabledPlugins": {"mine@personal": true}}`)
	writeConfigForTest(t, p.Root, pluginConfig)
	return p
}

func TestSyncPlugins_InstallsUpdatesAndWritesSettings(t *testing.T) {
	p := pluginProject(t)
	cli := &fakePluginCLI{
		serves:    map[string]string{"nats-expert@team-tools": "1.4.0", "linter@gh": "0a1b2c3d4e"},
		installed: map[string]string{"nats-expert@team-tools": "1.3.0"},
	}
	res, err := p.SyncPlugins(ctx(), cli, SyncOptions{WriteSettings: true})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"updated nats-expert@team-tools", "installed linter@gh"}, res.Actions)
	require.Contains(t, cli.calls, "add "+filepath.Join(filepath.Dir(p.Root), "mkt"))
	require.Contains(t, cli.calls, "add org/claude-plugins#stable")
	require.Contains(t, cli.calls, "refresh team-tools")
	require.Equal(t, "1.4.0", cli.installed["nats-expert@team-tools"])

	var s map[string]any
	data, err := os.ReadFile(p.SettingsJSON())
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &s))
	require.Equal(t, "opus", s["model"], "other keys kept")
	require.Equal(t, map[string]any{"mine@personal": true, "nats-expert@team-tools": true, "linter@gh": true}, s["enabledPlugins"])
	markets, ok := s["extraKnownMarketplaces"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, map[string]any{"source": map[string]any{"source": "github", "repo": "org/claude-plugins", "ref": "stable"}}, markets["gh"])
	require.Empty(t, p.CheckPlugins(ctx(), cli), "in step")

	// Idempotent: nothing to install, nothing to write.
	cli.calls = nil
	res, err = p.SyncPlugins(ctx(), cli, SyncOptions{WriteSettings: true})
	require.NoError(t, err)
	require.Empty(t, res.Actions)
	require.Empty(t, res.Settings)
	require.Equal(t, []string{"list"}, cli.calls, "one list call when all is well")
}

// ape task's preflight installs but leaves settings.json exactly as it was,
// whatever the CLI wrote.
func TestSyncPlugins_PreflightLeavesSettingsAlone(t *testing.T) {
	p := pluginProject(t)
	before, err := os.ReadFile(p.SettingsJSON())
	require.NoError(t, err)
	cli := &fakePluginCLI{
		serves:    map[string]string{"nats-expert@team-tools": "1.4.0", "linter@gh": "2"},
		installed: map[string]string{}, settingsWrite: true,
	}
	_, err = p.SyncPlugins(ctx(), cli, SyncOptions{})
	require.NoError(t, err)
	after, err := os.ReadFile(p.SettingsJSON())
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
}

// A marketplace that does not serve the pinned ref leaves the plugin off
// it: plugins.missing, with the exact command to run.
func TestSyncPlugins_RefusesWithTheCommand(t *testing.T) {
	p := pluginProject(t)
	cli := &fakePluginCLI{
		serves:    map[string]string{"nats-expert@team-tools": "1.3.9", "linter@gh": "2"},
		installed: map[string]string{"nats-expert@team-tools": "1.3.0", "linter@gh": "2"},
	}
	_, err := p.SyncPlugins(ctx(), cli, SyncOptions{})
	var fe *FailError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, "plugins.missing", fe.Code)
	require.Contains(t, fe.Msg, "nats-expert@team-tools is at version 1.3.9, the config pins ref v1.4.0")
	require.Contains(t, fe.Msg, "claude plugin update nats-expert@team-tools --scope project")
	d := Diagnostics(err)
	require.Equal(t, "plugins.missing", d[0].Code)
	require.Contains(t, d[0].Fix, "cd ")

	cli.failAdd = true
	delete(cli.installed, "linter@gh")
	_, err = p.SyncPlugins(ctx(), cli, SyncOptions{})
	require.ErrorAs(t, err, &fe)
	require.Contains(t, fe.Msg, "linter@gh is not installed at project scope")
	require.Contains(t, fe.Msg, "unreachable")
	require.Contains(t, fe.Msg, "claude plugin marketplace add org/claude-plugins#stable --scope project && claude plugin install linter@gh --scope project")
}

func TestCheckPlugins_Drift(t *testing.T) {
	p := pluginProject(t)
	cli := &fakePluginCLI{installed: map[string]string{"nats-expert@team-tools": "1.3.0"}}
	fs := p.CheckPlugins(ctx(), cli)
	findingMessages(fs)
	for _, c := range codesOf(fs) {
		require.Equal(t, "plugins.drift", c)
	}
	msgs := findingMessages(fs)
	require.Contains(t, msgs, "extraKnownMarketplaces.gh is missing")
	require.Contains(t, msgs, "enabledPlugins.linter@gh is missing")
	require.Contains(t, msgs, "linter@gh is not installed at project scope")
	require.Contains(t, msgs, "nats-expert@team-tools is at version 1.3.0, the config pins ref v1.4.0")

	// Settings only, when claude is not there to ask.
	require.Len(t, p.CheckPlugins(ctx(), nil), 4)

	writeConfigForTest(t, p.Root, "marketplaces:\n  m: {source: ftp}\nplugins:\n  - {name: a, marketplace: m}\n  - {name: a, marketplace: m}\n")
	fs = p.CheckPlugins(ctx(), cli)
	findingMessages(fs)
	require.Equal(t, []string{"plugins.config", "plugins.config"}, codesOf(fs))
}

func TestRefMatches(t *testing.T) {
	for _, tc := range []struct {
		version, ref string
		ok           bool
	}{
		{"1.4.0", "v1.4.0", true},
		{"v1.4.0", "1.4.0", true},
		{"1.4.0", "1.4.0", true},
		{"1.4.1", "v1.4.0", false},
		{"0a1b2c3d4e5f", "0a1b2c3", true},
		{"0a1b2c3", "0a1b2c3d4e5f", true},
		{"0a1b2c3d", "0a1b2c4", false},
		{"anything", "", true},
		{"", "v1", false},
	} {
		require.Equal(t, tc.ok, RefMatches(tc.version, tc.ref), "%q vs %q", tc.version, tc.ref)
	}
}

func TestPluginSettings_Inline(t *testing.T) {
	require.Nil(t, PluginSettings(Config{}, "/p"))
	cfg := Config{Marketplaces: map[string]Marketplace{"m": {Source: "directory", Path: "mkt"}}, Plugins: []PluginRef{{Name: "a", Marketplace: "m"}}}
	root := t.TempDir()
	got := PluginSettings(cfg, root)
	require.Equal(t, map[string]any{"a@m": true}, got["enabledPlugins"])
	markets, ok := got["extraKnownMarketplaces"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, map[string]any{"source": map[string]any{"source": "directory", "path": filepath.Join(root, "mkt")}}, markets["m"])
}
