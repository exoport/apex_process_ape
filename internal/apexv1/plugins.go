package apexv1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Plugins handled by ape (backlog 36).
//
// _apex/config.yaml names the Claude Code plugins a project uses:
//
//	marketplaces:
//	  team-tools: {source: github, repo: org/claude-plugins}   # or {source: git, url: ...}
//	                                                         # or {source: directory, path: ...}
//	plugins:
//	  - {name: nats-expert, marketplace: team-tools, ref: v1.4.0}
//	  - {name: kb, marketplace: team-tools, allow: [mcp]}   # D59 Q5
//
// ape writes them into .claude/settings.json (extraKnownMarketplaces,
// enabledPlugins) and installs each at PROJECT scope through the `claude
// plugin` CLI — on `ape project init`, `ape framework update` and `ape
// doctor --fix` — and `ape task` installs or updates any that is missing
// or off its ref before it spawns.
//
// What "ref" means. A consumer cannot pin a plugin version in Claude Code:
// a marketplace serves one version of each plugin, and an install records
// that version in the CLI's installed_plugins.json — plugin.json's
// `version`, else the marketplace entry's, else the source commit (read
// here from `claude plugin list --json`). ref is the version the project
// expects: ape compares it with the installed version (a leading "v" on
// either side ignored; for a commit, either is a prefix of the other), and
// when they differ refreshes the marketplace and updates the plugin. A
// marketplace that does not serve ref leaves the plugin off it, and that is
// reported with the command to run — ape cannot install a version the
// marketplace does not have. To pin, point the marketplace at a ref: a
// github or git marketplace takes `ref:` (written as the source's ref, and
// as <repo>#<ref> to the CLI). An empty ref accepts any version.
//
// What "allow" means (D59 Q5). A tool is a skill only (D59 Q4): a listed
// plugin that carries hooks or MCP servers is a finding
// (plugin.hooks-or-mcp, action person) in `ape project check` and `ape
// plugins status` until a person allows that plugin explicitly, per kind:
// allow: [hooks], [mcp] or [hooks, mcp]. plugins: is under the
// config.changed guard, so adding an allowance is itself a person's act.

// Marketplace is one marketplaces.<name> entry.
type Marketplace struct {
	Source string `json:"source"         yaml:"source"` // github | git | directory
	Repo   string `json:"repo,omitempty" yaml:"repo"`   // github: owner/repo
	URL    string `json:"url,omitempty"  yaml:"url"`    // git
	Path   string `json:"path,omitempty" yaml:"path"`   // directory; relative to the project root
	Ref    string `json:"ref,omitempty"  yaml:"ref"`    // github, git: branch or tag of the catalog
}

// PluginRef is one plugins entry.
type PluginRef struct {
	Name        string `json:"name"          yaml:"name"`
	Marketplace string `json:"marketplace"   yaml:"marketplace"`
	Ref         string `json:"ref,omitempty" yaml:"ref"`
	// Allow lists what the plugin may carry besides skills: hooks, mcp.
	Allow []string `json:"allow,omitempty" yaml:"allow"`
}

// The allow: values (D59 Q5).
const (
	AllowHooks = "hooks"
	AllowMCP   = "mcp"
)

// Allows reports whether the entry allows kind (AllowHooks, AllowMCP).
func (r PluginRef) Allows(kind string) bool { return slices.Contains(r.Allow, kind) }

// ID is the plugin's Claude Code id, <name>@<marketplace>.
func (r PluginRef) ID() string { return r.Name + "@" + r.Marketplace }

var (
	pluginNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	commitRe     = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	shellSafeRe  = regexp.MustCompile(`^[A-Za-z0-9_./:@#+=-]+$`)
)

// pluginConfigErrors lists what is wrong with the plugin config.
func pluginConfigErrors(cfg Config) []string {
	var out []string
	names := make([]string, 0, len(cfg.Marketplaces))
	for n := range cfg.Marketplaces {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		m := cfg.Marketplaces[n]
		if !pluginNameRe.MatchString(n) {
			out = append(out, fmt.Sprintf("marketplace name %q is not a plain name", n))
		}
		switch m.Source {
		case "github":
			if !strings.Contains(m.Repo, "/") {
				out = append(out, "marketplaces."+n+": source github needs repo: <owner>/<repo>")
			}
		case "git":
			if m.URL == "" {
				out = append(out, "marketplaces."+n+": source git needs url:")
			}
		case "directory":
			if m.Path == "" {
				out = append(out, "marketplaces."+n+": source directory needs path:")
			}
			if m.Ref != "" {
				out = append(out, "marketplaces."+n+": a directory marketplace has no ref:")
			}
		default:
			out = append(out, fmt.Sprintf("marketplaces.%s: source %q is not github, git or directory", n, m.Source))
		}
	}
	seen := map[string]bool{}
	for i, pl := range cfg.Plugins {
		switch {
		case !pluginNameRe.MatchString(pl.Name):
			out = append(out, fmt.Sprintf("plugins[%d]: name %q is not a plugin name", i, pl.Name))
		case !pluginNameRe.MatchString(pl.Marketplace):
			out = append(out, fmt.Sprintf("plugins[%d] (%s): marketplace %q is not a marketplace name", i, pl.Name, pl.Marketplace))
		case seen[pl.ID()]:
			out = append(out, "plugins: "+pl.ID()+" is listed twice")
		}
		seen[pl.ID()] = true
		for _, a := range pl.Allow {
			if a != AllowHooks && a != AllowMCP {
				out = append(out, fmt.Sprintf("plugins[%d] (%s): allow %q is not hooks or mcp", i, pl.Name, a))
			}
		}
	}
	return out
}

// marketplacePath resolves a directory marketplace's path.
func marketplacePath(root, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(root, path)
}

// settingsSource is the marketplace as extraKnownMarketplaces.<name>.source.
// A directory path is written absolute, as Claude Code's own `marketplace
// add` writes it: a directory marketplace is machine-local.
func (m Marketplace) settingsSource(root string) map[string]any {
	src := map[string]any{"source": m.Source}
	switch m.Source {
	case "github":
		src["repo"] = m.Repo
	case "git":
		src["url"] = m.URL
	case "directory":
		src["path"] = marketplacePath(root, m.Path)
	}
	if m.Ref != "" {
		src["ref"] = m.Ref
	}
	return src
}

// cliSource is the marketplace as `claude plugin marketplace add` takes it.
func (m Marketplace) cliSource(root string) string {
	ref := ""
	if m.Ref != "" {
		ref = "#" + m.Ref
	}
	switch m.Source {
	case "github":
		return m.Repo + ref
	case "git":
		return m.URL + ref
	}
	return marketplacePath(root, m.Path)
}

// desiredPluginSettings are the settings.json entries the config implies.
func desiredPluginSettings(cfg Config, root string) (markets, enabled map[string]any) {
	markets, enabled = map[string]any{}, map[string]any{}
	for n, m := range cfg.Marketplaces {
		markets[n] = map[string]any{"source": m.settingsSource(root)}
	}
	for _, pl := range cfg.Plugins {
		enabled[pl.ID()] = true
	}
	return markets, enabled
}

// PluginSettings is the --settings fragment that declares and enables the
// config's plugins, for `ape task` to pass inline: nil when there are none.
func PluginSettings(cfg Config, root string) map[string]any {
	if len(cfg.Marketplaces) == 0 && len(cfg.Plugins) == 0 {
		return nil
	}
	markets, enabled := desiredPluginSettings(cfg, root)
	out := map[string]any{}
	if len(markets) > 0 {
		out["extraKnownMarketplaces"] = markets
	}
	if len(enabled) > 0 {
		out["enabledPlugins"] = enabled
	}
	return out
}

// jsonEqual compares a decoded JSON value with a desired one.
func jsonEqual(have, want any) bool {
	a, errA := json.Marshal(have)
	b, errB := json.Marshal(want)
	if errA != nil || errB != nil {
		return reflect.DeepEqual(have, want)
	}
	var x, y any
	_ = json.Unmarshal(a, &x)
	_ = json.Unmarshal(b, &y)
	return reflect.DeepEqual(x, y)
}

// MergePluginSettings writes the config's marketplaces and plugins into a
// settings.json: extraKnownMarketplaces.<name> replaced whole when it
// differs, enabledPlugins.<id> set true. Every other key and entry is kept
// (a plugin dropped from the config stays as it was). Returns what changed.
func MergePluginSettings(path string, cfg Config, root string) ([]string, error) {
	if len(cfg.Marketplaces) == 0 && len(cfg.Plugins) == 0 {
		return nil, nil
	}
	settings, err := readSettings(path)
	if err != nil {
		return nil, err
	}
	markets, enabled := desiredPluginSettings(cfg, root)
	var changed []string
	merge := func(key string, want map[string]any) {
		have, _ := settings[key].(map[string]any)
		if have == nil {
			have = map[string]any{}
		}
		names := make([]string, 0, len(want))
		for n := range want {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if cur, ok := have[n]; !ok || !jsonEqual(cur, want[n]) {
				have[n] = want[n]
				changed = append(changed, key+"."+n)
			}
		}
		if len(have) > 0 {
			settings[key] = have
		}
	}
	merge("extraKnownMarketplaces", markets)
	merge("enabledPlugins", enabled)
	if len(changed) == 0 {
		return nil, nil
	}
	return changed, writeSettings(path, settings)
}

// InstalledPlugin is one row of `claude plugin list --json`.
type InstalledPlugin struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	Scope       string `json:"scope"`
	ProjectPath string `json:"projectPath"`
	// InstallPath is the versioned copy Claude Code loads the plugin from
	// (under <config dir>/plugins/cache/): what plugins status inspects.
	InstallPath string `json:"installPath"`
}

// PluginCLI is the `claude plugin` CLI, at project scope, run in the
// project root. ClaudePluginCLI is the real one; tests use a fake.
type PluginCLI interface {
	Installed(ctx context.Context, root string) ([]InstalledPlugin, error)
	AddMarketplace(ctx context.Context, root, source string) error
	UpdateMarketplace(ctx context.Context, root, name string) error
	Install(ctx context.Context, root, id string) error
	Update(ctx context.Context, root, id string) error
}

// ErrNoClaude: the claude binary is not on PATH.
var ErrNoClaude = errors.New("claude is not on PATH")

// ClaudePluginCLI runs `<Bin> plugin ...` ("claude" when Bin is empty)
// with the caller's environment (CLAUDE_CONFIG_DIR included).
type ClaudePluginCLI struct{ Bin string }

func (c ClaudePluginCLI) run(ctx context.Context, root string, args ...string) ([]byte, error) {
	bin := c.Bin
	if bin == "" {
		bin = "claude"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, ErrNoClaude
	}
	cmd := exec.CommandContext(ctx, path, append([]string{"plugin"}, args...)...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("claude plugin %s: %s", strings.Join(args, " "), cliMessage(stdout.Bytes(), stderr.Bytes(), err))
	}
	return stdout.Bytes(), nil
}

// cliMessage is the CLI's own account of a failure: the "message" of its
// --json result line, else its stderr, else the exit.
func cliMessage(stdout, stderr []byte, err error) string {
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	for _, line := range slices.Backward(lines) {
		var res struct {
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(line), &res) == nil && res.Message != "" {
			return res.Message
		}
	}
	if s := strings.TrimSpace(string(stderr)); s != "" {
		return s
	}
	return err.Error()
}

// Installed lists the installed plugins.
func (c ClaudePluginCLI) Installed(ctx context.Context, root string) ([]InstalledPlugin, error) {
	out, err := c.run(ctx, root, "list", "--json")
	if err != nil {
		return nil, err
	}
	var list []InstalledPlugin
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("claude plugin list --json: %w", err)
	}
	return list, nil
}

// AddMarketplace registers a marketplace (a no-op when it is).
func (c ClaudePluginCLI) AddMarketplace(ctx context.Context, root, source string) error {
	_, err := c.run(ctx, root, "marketplace", "add", source, "--scope", "project", "--json")
	return err
}

// UpdateMarketplace refreshes a marketplace's catalog.
func (c ClaudePluginCLI) UpdateMarketplace(ctx context.Context, root, name string) error {
	_, err := c.run(ctx, root, "marketplace", "update", name, "--json")
	return err
}

// Install installs a plugin at project scope.
func (c ClaudePluginCLI) Install(ctx context.Context, root, id string) error {
	_, err := c.run(ctx, root, "install", id, "--scope", "project", "--json")
	return err
}

// Update updates a plugin at project scope.
func (c ClaudePluginCLI) Update(ctx context.Context, root, id string) error {
	_, err := c.run(ctx, root, "update", id, "--scope", "project", "--json")
	return err
}

// RefMatches reports whether an installed version is the ref a project
// pins: equal, with a leading "v" ignored on either side; for a commit
// (7+ hex digits), either a prefix of the other. An empty ref matches any.
func RefMatches(version, ref string) bool {
	if ref == "" {
		return true
	}
	v, r := strings.TrimPrefix(version, "v"), strings.TrimPrefix(ref, "v")
	if v == r {
		return true
	}
	return commitRe.MatchString(v) && commitRe.MatchString(r) && (strings.HasPrefix(v, r) || strings.HasPrefix(r, v))
}

// PluginState is one configured plugin as installed for this project.
type PluginState struct {
	Plugin      PluginRef `json:"plugin"`
	Installed   bool      `json:"installed"`
	Version     string    `json:"version,omitempty"`
	InstallPath string    `json:"install_path,omitempty"`
}

// OK reports whether the plugin is installed at its ref.
func (s PluginState) OK() bool { return s.Installed && RefMatches(s.Version, s.Plugin.Ref) }

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// pluginStates reads each configured plugin's project-scope install.
func (p *Project) pluginStates(ctx context.Context, cli PluginCLI, cfg Config) ([]PluginState, error) {
	list, err := cli.Installed(ctx, p.Root)
	if err != nil {
		return nil, err
	}
	out := make([]PluginState, 0, len(cfg.Plugins))
	for _, pl := range cfg.Plugins {
		st := PluginState{Plugin: pl}
		for _, ip := range list {
			if ip.ID == pl.ID() && ip.Scope == "project" && samePath(ip.ProjectPath, p.Root) {
				st.Installed, st.Version, st.InstallPath = true, ip.Version, ip.InstallPath
				break
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// fixCommand is the command a person runs to put one plugin right.
func (p *Project) fixCommand(cfg Config, st PluginState) string {
	var steps []string
	if m, ok := cfg.Marketplaces[st.Plugin.Marketplace]; ok {
		steps = append(steps, "claude plugin marketplace add "+shellQuote(m.cliSource(p.Root))+" --scope project")
	}
	if st.Installed {
		steps = append(steps, "claude plugin marketplace update "+st.Plugin.Marketplace,
			"claude plugin update "+st.Plugin.ID()+" --scope project")
	} else {
		steps = append(steps, "claude plugin install "+st.Plugin.ID()+" --scope project")
	}
	return "cd " + shellQuote(p.Root) + " && " + strings.Join(steps, " && ")
}

func shellQuote(s string) string {
	if shellSafeRe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// problem describes a plugin that is not OK.
func (s PluginState) problem() string {
	if !s.Installed {
		return s.Plugin.ID() + " is not installed at project scope"
	}
	return fmt.Sprintf("%s is at version %s, the config pins ref %s", s.Plugin.ID(), s.Version, s.Plugin.Ref)
}

// PluginSync is what SyncPlugins did.
type PluginSync struct {
	Actions  []string `json:"actions,omitempty"`
	Settings []string `json:"settings,omitempty"`
	Commit   string   `json:"commit,omitempty"`
}

// SyncOptions shape SyncPlugins.
type SyncOptions struct {
	// WriteSettings merges the config's entries into settings.json (init,
	// framework update, doctor --fix). Without it — `ape task`'s preflight
	// — settings.json is left byte-for-byte as it was, whatever the CLI
	// wrote: a dispatch must not start on a tree ape just dirtied. The
	// session gets the entries inline instead (PluginSettings).
	WriteSettings bool
	// Commit commits a settings.json the merge changed, as "apex: plugins".
	Commit bool
}

// SyncPlugins makes the config's plugins installed at their refs: a
// missing one is installed, one off its ref is updated after its
// marketplace is refreshed. A plugin that is still not right afterwards
// is a FailError (plugins.missing) carrying the command to run. A project
// with no plugin config is a no-op.
func (p *Project) SyncPlugins(ctx context.Context, cli PluginCLI, opt SyncOptions) (*PluginSync, error) {
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return nil, err
	}
	res := &PluginSync{}
	if len(cfg.Marketplaces) == 0 && len(cfg.Plugins) == 0 {
		return res, nil
	}
	if errs := pluginConfigErrors(cfg); len(errs) > 0 {
		return nil, &FailError{Code: "plugins.config", Msg: "_apex/config.yaml: " + strings.Join(errs, "; ") + " — fix the plugins/marketplaces config"}
	}
	before, readErr := os.ReadFile(p.SettingsJSON())
	states, err := p.pluginStates(ctx, cli, cfg)
	if err != nil {
		return nil, &FailError{Code: "plugins.missing", Msg: "cannot read the installed plugins (" + err.Error() + ") — install Claude Code, or run `claude plugin list` to see why"}
	}
	var failures []string
	res.Actions, failures = p.repairPlugins(ctx, cli, cfg, states)
	if len(res.Actions) > 0 {
		states, err = p.pluginStates(ctx, cli, cfg)
	}
	if !opt.WriteSettings {
		p.restoreSettings(before, readErr)
	}
	if err != nil {
		return nil, &FailError{Code: "plugins.missing", Msg: "cannot read the installed plugins (" + err.Error() + ") — run `claude plugin list` to see why"}
	}
	if opt.WriteSettings {
		if err := p.writePluginSettings(ctx, cfg, before, opt.Commit, res); err != nil {
			return res, err
		}
	}
	var problems, fixes []string
	for _, st := range states {
		if !st.OK() {
			problems = append(problems, st.problem())
			fixes = append(fixes, p.fixCommand(cfg, st))
		}
	}
	if len(problems) > 0 {
		msg := strings.Join(problems, "; ")
		if len(failures) > 0 {
			msg += " (" + strings.Join(failures, "; ") + ")"
		}
		return res, &FailError{Code: "plugins.missing", Msg: msg + " — run: " + strings.Join(fixes, " ; ")}
	}
	return res, nil
}

// repairPlugins installs each plugin that is missing and updates each one
// off its ref (after adding, then refreshing, its marketplace). It returns
// what it did and the CLI's failures.
func (p *Project) repairPlugins(ctx context.Context, cli PluginCLI, cfg Config, states []PluginState) (actions, failures []string) {
	added := map[string]bool{}
	for _, st := range states {
		if st.OK() {
			continue
		}
		mkt := st.Plugin.Marketplace
		if m, ok := cfg.Marketplaces[mkt]; ok && !added[mkt] {
			added[mkt] = true
			if err := cli.AddMarketplace(ctx, p.Root, m.cliSource(p.Root)); err != nil {
				failures = append(failures, err.Error())
				continue
			}
		}
		var err error
		if st.Installed {
			if err = cli.UpdateMarketplace(ctx, p.Root, mkt); err == nil {
				err = cli.Update(ctx, p.Root, st.Plugin.ID())
			}
			actions = append(actions, "updated "+st.Plugin.ID())
		} else {
			err = cli.Install(ctx, p.Root, st.Plugin.ID())
			actions = append(actions, "installed "+st.Plugin.ID())
		}
		if err != nil {
			failures = append(failures, err.Error())
		}
	}
	return actions, failures
}

// restoreSettings puts settings.json back as it was before the CLI ran
// (absent when it was absent).
func (p *Project) restoreSettings(before []byte, readErr error) {
	if readErr != nil {
		_ = os.Remove(p.SettingsJSON())
		return
	}
	if now, err := os.ReadFile(p.SettingsJSON()); err != nil || !bytes.Equal(now, before) {
		_ = writeFileAtomic(p.SettingsJSON(), string(before))
	}
}

// writePluginSettings merges the config's entries into settings.json and,
// with commit, commits the file when it differs from before — the CLI may
// have written the same entries itself (marketplace add, install), so the
// file decides, not the merge.
func (p *Project) writePluginSettings(ctx context.Context, cfg Config, before []byte, commit bool, res *PluginSync) error {
	var err error
	if res.Settings, err = MergePluginSettings(p.SettingsJSON(), cfg, p.Root); err != nil {
		return err
	}
	after, _ := os.ReadFile(p.SettingsJSON())
	changed := !bytes.Equal(before, after)
	if changed && len(res.Settings) == 0 {
		res.Settings = []string{"written by the claude plugin CLI"}
	}
	if !commit || !changed {
		return nil
	}
	sha, err := p.commitRecord(ctx, "apex: plugins", "", p.Rel(p.SettingsJSON()))
	if err != nil {
		return commitFailed(err, "plugin settings written but their commit failed: "+err.Error())
	}
	res.Commit = sha
	return nil
}

// CheckPlugins reports drift between the plugin config and the project:
// settings.json entries missing or different (always), and, with a cli,
// plugins not installed at project scope or off their ref. A nil cli
// (claude not on PATH) checks the settings only.
func (p *Project) CheckPlugins(ctx context.Context, cli PluginCLI) []Finding {
	const file = configYAML
	cfg, err := LoadConfig(p.Root)
	if err != nil || (len(cfg.Marketplaces) == 0 && len(cfg.Plugins) == 0) {
		return nil
	}
	if errs := pluginConfigErrors(cfg); len(errs) > 0 {
		out := make([]Finding, 0, len(errs))
		for _, e := range errs {
			out = append(out, Finding{File: file, Message: "plugins config: " + e + " — fix the plugins/marketplaces config"})
		}
		return out
	}
	var out []Finding
	settings, err := readSettings(p.SettingsJSON())
	if err == nil {
		markets, enabled := desiredPluginSettings(cfg, p.Root)
		for _, sec := range []struct {
			key  string
			want map[string]any
		}{{"extraKnownMarketplaces", markets}, {"enabledPlugins", enabled}} {
			have, _ := settings[sec.key].(map[string]any)
			names := make([]string, 0, len(sec.want))
			for n := range sec.want {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				cur, ok := have[n]
				switch {
				case !ok:
					out = append(out, Finding{File: settingsJSONRel, Message: "plugins: " + sec.key + "." + n + " is missing — run `ape project init` (or `ape doctor --fix`) to write it"})
				case !jsonEqual(cur, sec.want[n]):
					out = append(out, Finding{File: settingsJSONRel, Message: "plugins: " + sec.key + "." + n + " differs from _apex/config.yaml — run `ape project init` (or `ape doctor --fix`) to rewrite it"})
				}
			}
		}
	}
	if cli == nil {
		return out
	}
	states, err := p.pluginStates(ctx, cli, cfg)
	if err != nil {
		if errors.Is(err, ErrNoClaude) {
			return out
		}
		return append(out, Finding{File: file, Message: "plugins: cannot read the installed plugins (" + err.Error() + ") — run `claude plugin list` to see why"})
	}
	for _, st := range states {
		if !st.OK() {
			out = append(out, Finding{File: file, Message: "plugins: " + st.problem() + " — run `ape doctor --fix`, or: " + p.fixCommand(cfg, st)})
		}
	}
	return out
}
