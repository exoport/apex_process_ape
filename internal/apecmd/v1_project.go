package apecmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
)

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Create and check an APEX v1 project layout",
	}
	cmd.AddCommand(newProjectInitCmd(), newProjectCheckCmd(), newProjectCodesCmd())
	return cmd
}

func newProjectInitCmd() *cobra.Command {
	var f v1Flags
	var brownfield bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create the development/ skeleton and merge ape's hooks (never overwrites)",
		Long: `Create the APEX v1 skeleton where it is absent. Nothing that exists is overwritten
(one exception: the @AGENTS.md line appended to a CLAUDE.md that lacks it).

  _apex/config.yaml                  project_name, development_folder
  AGENTS.md                          the project map (skeleton)
  CLAUDE.md                          "@AGENTS.md"; an existing CLAUDE.md without that line
                                     gets it appended, and nothing else
  development/project.md             vision, users, scope, out of scope, constraints (skeleton)
  development/roadmap.md             "# Roadmap"
  development/specs/                 empty: no backfill — a spec is born from its first change
  development/changes/archive/
  development/decisions/
  development/.apex/                 ape's own records (never edited by a model)

Hooks merged into .claude/settings.json (existing keys and hooks are kept):
  SessionStart          ape hook session-start
  Stop, SubagentStop    ape hook stop          (timeout 1800 s: it runs the change's checks)
  PreToolUse            ape hook pre-tool      (matcher Edit|Write|MultiEdit|NotebookEdit|Bash:
                                                the edit guard, and the git tag guard)
A hook that was altered (another matcher, async, a shorter stop timeout) is replaced
by the one above; running init again changes nothing. ape task passes the same
hooks inline as well, so its sessions have them even when settings.json does not.

Everything init wrote (and settings.json when it merged hooks) is committed as
"apex: project init"; files that existed before are not touched and not committed.

Plugins: when _apex/config.yaml lists marketplaces/plugins, init installs each
plugin at project scope through the claude plugin CLI (marketplace add, install,
or marketplace update + update when it is off its ref), writes
extraKnownMarketplaces and enabledPlugins into .claude/settings.json (other keys
kept) and commits that as "apex: plugins". ape framework update and ape doctor
--fix do the same; ape task installs a missing one before it spawns.

  marketplaces:
    team-tools: {source: github, repo: org/claude-plugins}   # or {source: git, url: …},
                                                             # {source: directory, path: …}
  plugins:
    - {name: nats-expert, marketplace: team-tools, ref: v1.4.0}

ref is the version the install must report (claude plugin list; a leading v is
ignored; a commit matches by prefix). Claude Code cannot pin a plugin version
from the consumer side: a marketplace serves one version of each plugin, so pin
by pointing the marketplace at a ref (github/git: ref: <branch|tag>). A plugin
the marketplace does not serve at ref fails with the command to run.

--brownfield adds nothing; it records in development/.apex/project.json that the
project existed before APEX.

Exit codes: 0 ok · 1 failed (e.g. settings.json is not valid JSON) · 2 usage.`,
		Example: "  ape project init\n  ape project init --brownfield --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := f.cwd
			if dir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				dir = wd
			}
			res, err := apexv1.Init(cmd.Context(), dir, brownfield)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			// Backlog 36: the plugins _apex/config.yaml lists, installed
			// and written into settings.json (committed as "apex: plugins").
			p, err := apexv1.Open(res.Root)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			var pluginOut strings.Builder
			plugins, err := syncPluginsReport(cmd.Context(), &pluginOut, p, true)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			out := struct {
				*apexv1.InitResult
				Plugins *apexv1.PluginSync `json:"plugins,omitempty"`
			}{res, plugins}
			return v1Emit(cmd.OutOrStdout(), &f, out, func(w io.Writer) {
				for _, c := range res.Created {
					fmt.Fprintln(w, "created  "+c)
				}
				for _, k := range res.Kept {
					fmt.Fprintln(w, "kept     "+k)
				}
				for _, a := range res.Amended {
					fmt.Fprintln(w, "amended  "+a+" (appended @AGENTS.md)")
				}
				for _, h := range res.HooksAdded {
					fmt.Fprintln(w, "hook     "+h)
				}
				fmt.Fprint(w, pluginOut.String())
				fmt.Fprintln(w, "next: write development/project.md, AGENTS.md and the roadmap (apex-plan), then `ape project check`")
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().BoolVar(&brownfield, "brownfield", false, "record that the project existed before APEX")
	return cmd
}

func newProjectCheckCmd() *cobra.Command {
	var f v1Flags
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate every v1 shape and size cap; list all failures",
		Long: `Validate the whole project and list every failure, not just the first.

  development/project.md   # <name>, ## Vision, ## Users, ## Scope, ## Out of scope,
                           ## Constraints (each non-empty); ≤ 150 lines and ≤ 8 KB
  AGENTS.md                present, ≤ 120 lines; CLAUDE.md (if any) has the line @AGENTS.md
  development/roadmap.md   # Roadmap, ≥ 1 "## M<n> — <title>" with Status (ape-written),
                           Gate, Goal; Budget a number when present
  specs/<cap>/spec.md      # title, Purpose:, "### Requirement: <name>" each with ≥ 1
                           "#### Scenario:" and exactly one "- check: <cmd> | human-review"
                           per scenario; ≤ 20 KB ("split into two capabilities")
  decisions/NNNN-<slug>.md # ADR-NNNN: title, Status, Date, Context/Decision/Consequences,
                           Enforced by
  changes/<id>/            every non-archived change, as ` + "`ape change check`" + `
  .claude/settings.json    ape's hooks registered as ` + "`ape project init`" + ` writes them:
                           hooks.missing / hooks.altered (matcher, type, async, a
                           stop timeout below 1800 s); ` + "`ape project init`" + ` restores them
  plugins                  _apex/config.yaml marketplaces/plugins: settings.json's
                           extraKnownMarketplaces/enabledPlugins match them, and (when
                           claude is on PATH) each plugin is installed at project scope
                           at its ref (plugins.drift; plugins.config for a bad config);
                           ` + "`ape doctor --fix`" + ` puts them right

Exit codes: 0 green · 1 findings (listed on stderr, or under "findings" with --json) · 2 usage.`,
		Example: "  ape project check\n  ape project check --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			fs := append(p.Check(), p.CheckHooks()...)
			fs = append(fs, p.CheckPlugins(cmd.Context(), pluginCLIOrNil(""))...)
			if len(fs) > 0 {
				return v1Fail(cmd, &f, &apexv1.FailError{
					Code:     "project.check-failed",
					Msg:      fmt.Sprintf("%d finding(s) — fix them and run `ape project check` again", len(fs)),
					Findings: fs,
				}, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, map[string]any{"ok": true, "findings": []apexv1.Finding{}}, func(w io.Writer) {
				fmt.Fprintln(w, "project check: green")
			})
		},
	}
	addV1Flags(cmd, &f)
	return cmd
}

func newProjectCodesCmd() *cobra.Command {
	var f v1Flags
	cmd := &cobra.Command{
		Use:   "codes",
		Short: "List the diagnostic codes v1 commands report in --json failures",
		Long: `List every diagnostic code an APEX v1 command (project, status, milestone, change,
decision) can report, with the exit status it comes with and what it means.

Every --json failure is {"ok": false, "exit": N, "error": "<one line>",
"diagnostics": [{"severity", "code", "message", "fix", "file"?, "line"?}], ...}: the
first diagnostic is the failure itself, the rest its findings or conflicts. Branch on
"code"; "message" and "fix" are for people. Codes are stable: a new rule gets a new
code, and a code's meaning never changes. severity is always "error" (no warning tier).
Exit: 1 check failed · 2 usage · 3 conflict (nothing written).`,
		Example: "  ape project codes\n  ape project codes --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return v1Emit(cmd.OutOrStdout(), &f, apexv1.DiagnosticCodes, func(w io.Writer) {
				for _, c := range apexv1.DiagnosticCodes {
					fmt.Fprintf(w, "%-30s %d  %s\n", c.Code, c.Exit, c.Meaning)
				}
			})
		},
	}
	cmd.Flags().BoolVar(&f.json, "json", false, "machine output (JSON on stdout)")
	return cmd
}

func newStatusCmd() *cobra.Command {
	var f v1Flags
	var brief bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the current milestone, active changes, blocks, pending human review and spend",
		Long: `Show where the project stands, from the records ape keeps:

  - the current milestone (the first not done): status, gate and its last recorded run,
    budget, and the spend of runs started since it was approved
  - every change not archived, with its status, blocked reason, red checks from its
    last verify, and human-review tasks not yet accepted
  - spend so far: the sum of finished ape task/pipeline run manifests
  - next: what the way of working says comes next, derived from the state alone

--brief renders at most 25 lines (what the SessionStart hook injects).

Exit codes: 0 ok · 2 usage.`,
		Example: "  ape status\n  ape status --brief\n  ape status --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			r := p.Status()
			return v1Emit(cmd.OutOrStdout(), &f, r, func(w io.Writer) {
				if brief {
					fmt.Fprint(w, r.Brief())
					return
				}
				fmt.Fprint(w, statusLong(r))
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().BoolVar(&brief, "brief", false, "at most 25 lines")
	return cmd
}

func statusLong(r *apexv1.StatusReport) string {
	var b strings.Builder
	b.WriteString(r.Brief())
	if len(r.Milestones) > 0 {
		b.WriteString("\nRoadmap:\n")
		for _, m := range r.Milestones {
			fmt.Fprintf(&b, "  %-4s %-9s %s\n", m.ID, m.Status, m.Title)
		}
	}
	if r.Milestone != nil && r.Milestone.GateLast != nil && !r.Milestone.GateLast.Green() {
		fmt.Fprintf(&b, "\nLast gate output (%s):\n", r.Milestone.GateLast.Log)
		for _, l := range r.Milestone.GateLast.Tail {
			b.WriteString("  | " + l + "\n")
		}
	}
	fmt.Fprintf(&b, "\nSpend note: %s\n", r.Spend.Note)
	return b.String()
}
