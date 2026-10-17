package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeClaudePlugins puts a `claude` shim first on PATH that answers
// `claude plugin list --json` from list.json and succeeds at every other
// plugin verb (logging it), and isolates CLAUDE_CONFIG_DIR and HOME. It
// runs nothing else — no git — so it cannot touch any repository.
func fakeClaudePlugins(t *testing.T, list string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "list.json"), []byte(list), 0o644))
	shim := "#!/bin/sh\n" +
		"echo \"$*\" >> '" + logPath + "'\n" +
		"if [ \"$1 $2\" = 'plugin list' ]; then cat '" + filepath.Join(dir, "list.json") + "'; exit 0; fi\n" +
		"echo '{}'\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "claude"), []byte(shim), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	return logPath
}

func TestPluginsCmd_SyncAndStatus(t *testing.T) {
	root := seedV1Fixture(t)
	plugin := filepath.Join(t.TempDir(), "kit")
	for rel, body := range map[string]string{
		"skills/security/SKILL.md": "---\nname: security\ndescription: s\n---\n",
		"skills/setup/SKILL.md":    "---\nname: setup\ndescription: s\ndisable-model-invocation: true\n---\n",
		".mcp.json":                `{"mcpServers":{"kb":{"command":"kb"}}}`,
	} {
		path := filepath.Join(plugin, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	list, err := json.Marshal([]map[string]string{{"id": "kit@team", "version": "1.0.0", "scope": "project", "projectPath": root, "installPath": plugin}})
	require.NoError(t, err)
	calls := fakeClaudePlugins(t, string(list))

	cfg := filepath.Join(root, "_apex", "config.yaml")
	base, err := os.ReadFile(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg, append(base, []byte(`marketplaces:
  team: {source: github, repo: org/kit}
plugins:
  - {name: kit, marketplace: team, ref: v1.0.0}
tools:
  review:
    - {name: security, skill: "kit:security", when: "auth changes"}
    - {name: perf, skill: perf-lens, when: "hot paths"}
`)...), 0o644))
	v1Git(t, root, "commit", "-qam", "config")

	stdout, stderr, err := v1Exec(t, newPluginsCmd, "sync", "--cwd", root, "--json")
	require.NoError(t, err, stderr)
	var res struct {
		Settings []string `json:"settings"`
		Commit   string   `json:"commit"`
		Findings []struct {
			Code string `json:"code"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	require.NotEmpty(t, res.Settings)
	require.NotEmpty(t, res.Commit)
	require.Equal(t, "apex: plugins", v1Git(t, root, "log", "-1", "--format=%s"))
	codes := []string{}
	for _, f := range res.Findings {
		codes = append(codes, f.Code)
	}
	require.ElementsMatch(t, []string{"plugin.hooks-or-mcp", "tools.not-installed"}, codes)

	// Idempotent: nothing installed, written or committed.
	head := v1Git(t, root, "rev-parse", "HEAD")
	stdout, stderr, err = v1Exec(t, newPluginsCmd, "sync", "--cwd", root)
	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "plugins: nothing to do")
	require.Equal(t, head, v1Git(t, root, "rev-parse", "HEAD"))
	require.Empty(t, v1Git(t, root, "status", "--porcelain"))
	log, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.NotContains(t, string(log), "install", "kit was installed at its ref already")

	stdout, stderr, err = v1Exec(t, newPluginsCmd, "status", "--cwd", root, "--json")
	require.NoError(t, err, stderr)
	var rep struct {
		InstallsChecked bool `json:"installs_checked"`
		Plugins         []struct {
			ID             string `json:"id"`
			Installed      bool   `json:"installed"`
			Enabled        bool   `json:"enabled"`
			Skills         int    `json:"skills"`
			ModelInvocable int    `json:"model_invocable"`
			MCP            bool   `json:"mcp"`
			Allowed        bool   `json:"allowed"`
		} `json:"plugins"`
		Tools []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &rep), stdout)
	require.True(t, rep.InstallsChecked)
	require.Len(t, rep.Plugins, 1)
	require.Equal(t, "kit@team", rep.Plugins[0].ID)
	require.True(t, rep.Plugins[0].Installed)
	require.True(t, rep.Plugins[0].Enabled)
	require.Equal(t, 2, rep.Plugins[0].Skills)
	require.Equal(t, 1, rep.Plugins[0].ModelInvocable)
	require.True(t, rep.Plugins[0].MCP)
	require.False(t, rep.Plugins[0].Allowed)
	require.Len(t, rep.Tools, 2)
	require.Equal(t, "ok", rep.Tools[0].State)
	require.Equal(t, "not-installed", rep.Tools[1].State)

	stdout, _, err = v1Exec(t, newPluginsCmd, "status", "--cwd", root)
	require.NoError(t, err, "status reports; it does not fail on findings")
	require.Contains(t, stdout, "2 skill(s), 1 model-invocable; carries MCP servers (NOT allowed)")
	require.Contains(t, stdout, "finding plugin.hooks-or-mcp:")

	// project check is the gate.
	_, stderr, err = v1Exec(t, newProjectCmd, "check", "--cwd", root)
	require.Error(t, err)
	require.Contains(t, stderr, "plugin kit@team carries MCP servers")
	require.Contains(t, stderr, "tool review/perf: skill perf-lens is not installed")

	// Allowed, and the project skill present: green.
	require.NoError(t, os.WriteFile(cfg, []byte(strings.Replace(string(mustRead(t, cfg)), "ref: v1.0.0}", "ref: v1.0.0, allow: [mcp]}", 1)), 0o644))
	skill := filepath.Join(root, ".claude", "skills", "perf-lens", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(skill), 0o755))
	require.NoError(t, os.WriteFile(skill, []byte("---\nname: perf-lens\ndescription: p\n---\n"), 0o644))
	stdout, stderr, err = v1Exec(t, newProjectCmd, "check", "--cwd", root)
	require.NoError(t, err, "stdout: %s\nstderr: %s", stdout, stderr)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
