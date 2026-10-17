package apecmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
)

// Plugins handled by ape (backlog 36): see internal/apexv1/plugins.go.

// pluginCLIOrNil is the `claude plugin` CLI, nil when claude is not on
// PATH (project check then judges settings.json only).
func pluginCLIOrNil() apexv1.PluginCLI {
	const bin = "claude"
	if _, err := exec.LookPath(bin); err != nil {
		return nil
	}
	return apexv1.ClaudePluginCLI{Bin: bin}
}

// v1TaskPlugins is `ape task`'s plugin preflight in a v1 project: every
// plugin the config lists installed at its ref (settings.json untouched),
// and the --settings fragment that enables them for the session. A plugin
// still missing afterwards refuses the dispatch with the command to run.
func v1TaskPlugins(ctx context.Context, p *apexv1.Project, claudeBin string) (map[string]any, error) {
	cfg, err := apexv1.LoadConfig(p.Root)
	if err != nil {
		return nil, err
	}
	settings := apexv1.PluginSettings(cfg, p.Root)
	if settings == nil {
		return nil, nil //nolint:nilnil // no plugins configured: nothing to pass, and nothing wrong
	}
	if _, err := p.SyncPlugins(ctx, apexv1.ClaudePluginCLI{Bin: claudeBin}, apexv1.SyncOptions{}); err != nil {
		if fe, ok := errors.AsType[*apexv1.FailError](err); ok {
			return nil, fmt.Errorf("%s: %s", fe.Code, fe.Msg)
		}
		return nil, err
	}
	return settings, nil
}

// syncPluginsReport runs the full plugin sync — settings.json written,
// plugins installed, the settings change committed when commit is set —
// and prints what it did. A project with no plugin config prints nothing.
func syncPluginsReport(ctx context.Context, w io.Writer, p *apexv1.Project, commit bool) (*apexv1.PluginSync, error) {
	res, err := p.SyncPlugins(ctx, apexv1.ClaudePluginCLI{}, apexv1.SyncOptions{WriteSettings: true, Commit: commit})
	if res != nil {
		for _, a := range res.Actions {
			fmt.Fprintln(w, "plugin   "+a)
		}
		if len(res.Settings) > 0 {
			fmt.Fprintln(w, "plugin   settings.json: "+strings.Join(res.Settings, ", "))
		}
	}
	return res, err
}

// checkPluginsProject is doctor's plugins.project row.
func checkPluginsProject(ctx context.Context, env doctorEnv) CheckResult {
	res := CheckResult{Name: "plugins.project"}
	p, ok := v1ProjectAt(env.ProjectRoot)
	if !ok {
		res.Status, res.Message = StatusSkip, msgNotV1Project
		return res
	}
	cfg, err := apexv1.LoadConfig(p.Root)
	if err != nil || apexv1.PluginSettings(cfg, p.Root) == nil {
		res.Status, res.Message = StatusSkip, "_apex/config.yaml lists no plugins"
		return res
	}
	cli := pluginCLIOrNil()
	fs := p.CheckPlugins(ctx, cli)
	if len(fs) == 0 {
		res.Status, res.Message = StatusOK, fmt.Sprintf("%d plugin(s) installed at their refs, settings.json in step", len(cfg.Plugins))
		if cli == nil {
			res.Status, res.Message = StatusWarn, "settings.json in step; claude is not on PATH, so installs were not checked"
		}
		return res
	}
	msgs := make([]string, 0, len(fs))
	for _, f := range fs {
		msgs = append(msgs, f.Message)
	}
	res.Status, res.Message = StatusFail, strings.Join(msgs, "; ")
	res.Remediation = "ape doctor --fix installs and updates the plugins _apex/config.yaml lists and writes their settings"
	res.FixCommand = "ape doctor --fix --cwd " + p.Root
	return res
}

func newPluginsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugins",
		Short: "Install the plugins _apex/config.yaml lists, and report what they and the tools slot bring",
		Long: `The Claude Code plugins an APEX v1 project uses, and the tools slot (D59 Q4–Q6).

_apex/config.yaml (marketplaces, plugins and tools are under the config.changed
guard: a commit that changes them and is not ape's is a person's to accept):

  marketplaces:
    team-tools: {source: github, repo: org/claude-plugins, ref: v2}   # or {source: git, url: …},
                                                                      # {source: directory, path: …}
  plugins:
    - {name: nats-expert, marketplace: team-tools, ref: v1.4.0}
    - {name: kb, marketplace: team-tools, allow: [mcp]}
  tools:
    design:
      - {name: ui-mockup, skill: frontend-design:mockup, when: "the change adds or changes a screen",
         args: "{change} --out {assets}", model: sonnet, effort: high}
    review:
      - {name: security, skill: security-lens, when: "the change touches auth, secrets or input parsing"}

allow: lists what a plugin may carry besides skills — hooks, mcp. A listed plugin
that carries hooks (hooks/hooks.json, or hooks in .claude-plugin/plugin.json) or MCP
servers (.mcp.json, or mcpServers in plugin.json) its entry does not allow is the
finding plugin.hooks-or-mcp (action person): a tool is a skill only. Another allow:
value is a config error (plugins.config).

tools: has two slots, design and review; each entry is {name, skill, when}, names
unique, plus optional args: (the template ape fills into ape task <skill> --args;
placeholders {id}, {task}, {assets}, {change}, no other), model: and effort: (low,
medium, high, xhigh, max; unset: the D65 defaults). A skill is <plugin>:<skill> (the plugin listed in plugins:), or a project
skill (.claude/skills/<skill>/SKILL.md) or user skill ($CLAUDE_CONFIG_DIR, else
~/.claude, /skills/<skill>/SKILL.md; a user skill wins over a project one). Each must
be installed (tools.not-installed) and slash-invocable — not user-invocable: false
(tools.not-invocable) — because ape task types it as a slash command; a malformed
block is tools.invalid. The orchestrator may start these skills as children, beside
apex-plan, apex-change, apex-apply, apex-review, apex-release and apex-accept (D62,
D76); apex-handoff and apex-orchestrator never.

ape project check reports the same findings (exit 1).`,
	}
	cmd.AddCommand(newPluginsSyncCmd(), newPluginsStatusCmd())
	return cmd
}

func newPluginsSyncCmd() *cobra.Command {
	var f v1Flags
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Install or update every plugin _apex/config.yaml lists and write their settings",
		Long: `Make the project's plugins what _apex/config.yaml says: each marketplace added, each
plugin installed at project scope (or, off its ref, updated after its marketplace is
refreshed) through the claude plugin CLI, and extraKnownMarketplaces and enabledPlugins
written into .claude/settings.json (other keys kept). A settings.json that changed is
committed as "apex: plugins" (that file only). A second run installs, writes and
commits nothing.

ape project init, ape framework update and ape doctor --fix do the same; ape task
installs a missing plugin before it spawns. Afterwards the findings ape plugins status
reports (plugin.hooks-or-mcp, tools.*) are listed; they do not fail the sync.

Exit codes: 0 synced · 1 a plugin is still missing or off its ref (plugins.missing,
with the command to run), the config is malformed (plugins.config), or the commit
failed · 2 usage.`,
		Example: "  ape plugins sync\n  ape plugins sync --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			var out strings.Builder
			res, err := syncPluginsReport(cmd.Context(), &out, p, true)
			if err != nil {
				return v1Fail(cmd, &f, err, res)
			}
			rep, err := p.PluginsStatus(cmd.Context(), pluginCLIOrNil())
			if err != nil {
				return v1Fail(cmd, &f, err, res)
			}
			findings := apexv1.ClassifyFindings(rep.Findings)
			return v1Emit(cmd.OutOrStdout(), &f, struct {
				*apexv1.PluginSync
				Findings []apexv1.Finding `json:"findings"`
			}{res, findings}, func(w io.Writer) {
				fmt.Fprint(w, out.String())
				if res.Commit != "" {
					fmt.Fprintln(w, "commit   "+res.Commit+" apex: plugins")
				}
				if out.Len() == 0 {
					fmt.Fprintln(w, "plugins: nothing to do")
				}
				printPluginFindings(w, findings)
			})
		},
	}
	addV1Flags(cmd, &f)
	return cmd
}

func newPluginsStatusCmd() *cobra.Command {
	var f v1Flags
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report each listed plugin and tools: skill, and their findings",
		Long: `Report, for each plugin _apex/config.yaml lists: whether it is installed at project
scope, its version against ref, whether settings.json enables it, how many skills it
adds and how many of them are model-invocable (no disable-model-invocation: true —
each one's description is listed in every session, the plugin's per-session cost),
whether it carries hooks or MCP servers, and whether its allow: covers them. And for
each tools: entry: where its skill was found (plugin, project, user) and its state —
ok, not-installed, not-invocable, or unchecked (a plugin skill while claude is not on
PATH, so the installs cannot be read).

Findings: plugins.config, plugins.drift (ape plugins sync puts it right),
plugin.hooks-or-mcp, tools.invalid, tools.not-installed, tools.not-invocable — see
ape plugins --help. They are reported, not failed on: ape project check is the gate.

JSON: {"installs_checked", "plugins": [{"id", "ref", "installed", "version", "at_ref",
"enabled", "install_path", "skills", "model_invocable", "hooks", "mcp", "allow",
"allowed"}], "tools": [{"slot", "name", "skill", "source", "path", "state"}],
"findings": [{"file", "message", "code", "fix"}]}.

Exit codes: 0 reported (findings or not) · 1 the config cannot be read · 2 usage.`,
		Example: "  ape plugins status\n  ape plugins status --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			rep, err := p.PluginsStatus(cmd.Context(), pluginCLIOrNil())
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			rep.Findings = apexv1.ClassifyFindings(rep.Findings)
			return v1Emit(cmd.OutOrStdout(), &f, rep, func(w io.Writer) { printPluginsReport(w, rep) })
		},
	}
	addV1Flags(cmd, &f)
	return cmd
}

// printPluginsReport is plugins status's human output.
func printPluginsReport(w io.Writer, rep *apexv1.PluginsReport) {
	if len(rep.Plugins) == 0 {
		fmt.Fprintln(w, "plugins: none listed")
	}
	for i := range rep.Plugins {
		pl := &rep.Plugins[i]
		state := "not installed"
		switch {
		case !rep.InstallsChecked:
			state = "installs not checked (claude is not on PATH)"
		case pl.Installed && pl.AtRef:
			state = "installed " + pl.Version
		case pl.Installed:
			state = "installed " + pl.Version + ", off ref " + pl.Ref
		}
		enabled := "enabled"
		if !pl.Enabled {
			enabled = "not enabled in settings.json"
		}
		fmt.Fprintf(w, "plugin  %s  %s, %s\n", pl.ID, state, enabled)
		if pl.Installed {
			fmt.Fprintf(w, "        %d skill(s), %d model-invocable%s\n", pl.Skills, pl.ModelInvocable, pluginCarries(pl))
		}
	}
	for i := range rep.Tools {
		t := &rep.Tools[i]
		where := t.Source
		if t.Path != "" {
			where += " " + t.Path
		}
		fmt.Fprintf(w, "tool    %s/%s  %s  %s  %s\n", t.Slot, t.Name, t.Skill, t.State, strings.TrimSpace(where))
	}
	printPluginFindings(w, rep.Findings)
}

// pluginCarries is an installed plugin's "; carries hooks and MCP servers
// (allowed)" note, "" when it carries neither.
func pluginCarries(pl *apexv1.PluginStatus) string {
	var extra []string
	if pl.Hooks {
		extra = append(extra, "hooks")
	}
	if pl.MCP {
		extra = append(extra, "MCP servers")
	}
	if len(extra) == 0 {
		return ""
	}
	verdict := "allowed"
	if !pl.Allowed {
		verdict = "NOT allowed"
	}
	return "; carries " + strings.Join(extra, " and ") + " (" + verdict + ")"
}

// printPluginFindings lists findings with their codes.
func printPluginFindings(w io.Writer, fs []apexv1.Finding) {
	for _, fd := range fs {
		fmt.Fprintf(w, "finding %s: %s\n", fd.Code, fd.Message)
	}
}
