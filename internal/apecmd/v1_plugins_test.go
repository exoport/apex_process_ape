package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
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
	// marketplaces, plugins and tools are guarded: the person accepts them.
	_, _, err = v1Exec(t, newProjectCmd, "accept-config", "--by", "ann", "--cwd", root)
	require.NoError(t, err)

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
	codes := make([]string, 0, len(res.Findings))
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
		InstallsChecked bool `json:"installs_checked"` //nolint:tagliatelle // the v1 --json payloads are snake_case by contract
		Plugins         []struct {
			ID             string `json:"id"`
			Installed      bool   `json:"installed"`
			Enabled        bool   `json:"enabled"`
			Skills         int    `json:"skills"`
			ModelInvocable int    `json:"model_invocable"` //nolint:tagliatelle // the v1 --json payloads are snake_case by contract
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
	v1Git(t, root, "commit", "-qam", "allow mcp")
	_, _, err = v1Exec(t, newProjectCmd, "accept-config", "--by", "ann", "--cwd", root)
	require.NoError(t, err)
	stdout, stderr, err = v1Exec(t, newProjectCmd, "check", "--cwd", root)
	require.NoError(t, err, "stdout: %s\nstderr: %s", stdout, stderr)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

// W6: plugins status --outdated reads a marketplace's tags (a local bare
// repository, no network); an advise entry's files are listed; change
// check carries tools.unmatched under "notices", failing or not; ape
// status --json lists a change's advise files.
func TestPluginsCmd_AdviseOutdatedAndNotices(t *testing.T) {
	root := seedV1Fixture(t)
	fakeClaudePlugins(t, "[]")
	work, bare := t.TempDir(), filepath.Join(t.TempDir(), "mkt.git")
	v1Git(t, work, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("m\n"), 0o644))
	v1Git(t, work, "add", "-A")
	v1Git(t, work, "commit", "-qm", "catalog")
	for _, tag := range []string{"v0.1.0", "v0.2.0", "v0.3.0-rc.1"} {
		v1Git(t, work, "tag", tag)
	}
	v1Git(t, t.TempDir(), "clone", "-q", "--bare", work, bare)

	skill := filepath.Join(root, ".claude", "skills", "nats")
	require.NoError(t, os.MkdirAll(skill, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: nats\ndescription: n\ndisable-model-invocation: true\n---\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skill, "advise.md"), []byte("N1 NEVER.\n"), 0o644))
	cfg := filepath.Join(root, "_apex", "config.yaml")
	base, err := os.ReadFile(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg, append(base, []byte(`marketplaces:
  experts: {source: git, url: `+bare+`, ref: v0.1.0}
tools:
  advise:
    - {name: nats, skill: nats, when: "NATS", paths: ["src/bus/**"], load: [advise.md], for: [apply]}
`)...), 0o644))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-qm", "config")
	_, _, err = v1Exec(t, newProjectCmd, "accept-config", "--by", "ann", "--cwd", root)
	require.NoError(t, err)

	stdout, stderr, err := v1Exec(t, newPluginsCmd, "status", "--outdated", "--cwd", root, "--json")
	require.NoError(t, err, stderr)
	var rep struct {
		Tools []struct {
			Slot  string `json:"slot"`
			Files []struct {
				File   string `json:"file"`
				Bytes  int    `json:"bytes"`
				SHA256 string `json:"sha256"`
			} `json:"files"`
		} `json:"tools"`
		Outdated []struct {
			Marketplace string   `json:"marketplace"`
			Newer       []string `json:"newer"`
			Latest      string   `json:"latest"`
		} `json:"outdated"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &rep), stdout)
	require.Len(t, rep.Outdated, 1)
	require.Equal(t, "experts", rep.Outdated[0].Marketplace)
	require.Equal(t, []string{"v0.2.0"}, rep.Outdated[0].Newer)
	require.Equal(t, "v0.2.0", rep.Outdated[0].Latest)
	require.Len(t, rep.Tools, 1)
	require.Equal(t, "advise", rep.Tools[0].Slot)
	require.Equal(t, 10, rep.Tools[0].Files[0].Bytes)
	require.Len(t, rep.Tools[0].Files[0].SHA256, 64)
	stdout, _, err = v1Exec(t, newPluginsCmd, "status", "--outdated", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "market  experts  ref v0.1.0  newer: v0.2.0\n")
	require.Contains(t, stdout, "        load advise.md  10 bytes  sha256 ")

	_, _, err = v1Exec(t, newChangeCmd, "new", "add-bus", "--milestone", "M1", "--touches", "greeting", "--paths", "src/bus/*.go", "--cwd", root)
	require.NoError(t, err)
	stdout, stderr, err = v1Exec(t, newChangeCmd, "check", "add-bus", "--cwd", root, "--json")
	require.Error(t, err, "a skeleton has findings")
	var chk struct {
		Notices []struct {
			Severity string `json:"severity"`
			Code     string `json:"code"`
			Message  string `json:"message"`
			Fix      string `json:"fix"`
		} `json:"notices"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &chk), stdout)
	require.Len(t, chk.Notices, 1, stdout)
	require.Equal(t, "notice", chk.Notices[0].Severity)
	require.Equal(t, "tools.unmatched", chk.Notices[0].Code)
	require.Equal(t, "tools.advise entry nats matches this change (paths: src/bus/*.go ~ src/bus/**) and the change does not list it", chk.Notices[0].Message)
	require.Empty(t, stderr)
	_, stderr, _ = v1Exec(t, newChangeCmd, "check", "add-bus", "--cwd", root)
	require.Contains(t, stderr, "notice: development/changes/add-bus/change.md:1: tools.advise entry nats matches this change")

	md := filepath.Join(root, "development", "changes", "add-bus", "change.md")
	doc, err := os.ReadFile(md)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(md, []byte(strings.Replace(string(doc), "touches: [greeting]", "touches: [greeting]\nadvise: [nats]", 1)), 0o644))
	stdout, _, err = v1Exec(t, newStatusCmd, "--cwd", root, "--json")
	require.NoError(t, err)
	var st struct {
		Changes []struct {
			ID     string `json:"id"`
			Advise []struct {
				Name   string   `json:"name"`
				For    []string `json:"for"`
				Path   string   `json:"path"`
				Bytes  int      `json:"bytes"`
				SHA256 string   `json:"sha256"`
			} `json:"advise"`
		} `json:"changes"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &st), stdout)
	require.Len(t, st.Changes, 1)
	require.Len(t, st.Changes[0].Advise, 1)
	require.Equal(t, filepath.Join(skill, "advise.md"), st.Changes[0].Advise[0].Path)
	require.Equal(t, []string{"apply"}, st.Changes[0].Advise[0].For)
	require.Equal(t, 10, st.Changes[0].Advise[0].Bytes)
}

// W6-F1 (D392): a marketplace on a branch shows the note, which names the
// latest tag, on its human line.
func TestPrintOutdated_BranchRefNamesTheLatestTag(t *testing.T) {
	var b strings.Builder
	printOutdated(&b, []apexv1.MarketplaceTags{{
		Marketplace: "experts", Source: "github", Ref: "main", Latest: "v0.2.0", Newer: []string{},
		Note: "ref main is not a version tag; pin a tag to compare (latest v0.2.0)",
	}})
	require.Equal(t, "market  experts  ref main  ref main is not a version tag; pin a tag to compare (latest v0.2.0)\n", b.String())
}

// F-E1-1: `ape prompt --agent <plugin>:<skill>` resolves a plugin skill as
// ape task does — listed, installed, carrying it — and refuses the rest
// with what is missing; a plain agent still needs a SKILL.md.
func TestPromptAgent_PluginSkill(t *testing.T) {
	root := seedV1Fixture(t)
	plugin := filepath.Join(t.TempDir(), "kit")
	require.NoError(t, os.MkdirAll(filepath.Join(plugin, "skills", "security"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(plugin, "skills", "security", "SKILL.md"), []byte("---\nname: security\ndescription: s\n---\n"), 0o644))
	list, err := json.Marshal([]map[string]string{{"id": "kit@team", "version": "1.0.0", "scope": "project", "projectPath": root, "installPath": plugin}})
	require.NoError(t, err)
	fakeClaudePlugins(t, string(list))
	cfg := filepath.Join(root, "_apex", "config.yaml")
	base, err := os.ReadFile(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg, append(base, []byte("marketplaces:\n  team: {source: github, repo: org/kit}\nplugins:\n  - {name: kit, marketplace: team, ref: v1.0.0}\n")...), 0o644))

	require.NoError(t, resolvePromptAgent("kit:security", root))
	require.EqualError(t, resolvePromptAgent("kit:nope", root),
		`--agent "kit:nope" did not resolve: plugin kit has no skill nope — fix the skill name, or install a version of kit that ships it`)
	require.EqualError(t, resolvePromptAgent("zed:x", root),
		`--agent "zed:x" did not resolve: no plugin zed in plugins: — list the plugin zed in plugins: and run `+"`ape plugins sync`"+`, or fix the skill name`)
	require.EqualError(t, resolvePromptAgent("apex-nope", root), `--agent "apex-nope" did not resolve under .claude/skills (project or user)`)
}
