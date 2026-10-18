package apecmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/statusboard"
	"github.com/spf13/cobra"
)

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Create and check an APEX v1 project layout",
	}
	cmd.AddCommand(newProjectInitCmd(), newProjectCheckCmd(), newProjectCodesCmd(), newProjectLiftCmd(), newProjectAcceptConfigCmd())
	return cmd
}

func newProjectInitCmd() *cobra.Command {
	var f v1Flags
	var brownfield bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create the development/ skeleton and merge ape's hooks (never overwrites)",
		Long: `Create the APEX v1 skeleton where it is absent. Nothing that exists is overwritten
(the exceptions: the @AGENTS.md line appended to a CLAUDE.md that lacks it, the
project-context line added to an AGENTS.md that lacks it, and the models: defaults
appended to an _apex/config.yaml that has no models: block).

  _apex/config.yaml                  project_name, development_folder, and the models:
                                     defaults (D65, D93) — apex-apply, apex-orchestrator
                                     and apex-release {model: sonnet, effort: high};
                                     apex-review, apex-plan, apex-change and apex-accept
                                     {model: opus, effort: medium}. An existing config
                                     with no top-level models: gets that block appended,
                                     its own text kept byte for byte (restored, with a
                                     warning, if the result would not parse); the
                                     amendment is committed with "apex: project init"
                                     only when the config was tracked and clean before,
                                     else left for you to commit. An existing models: is
                                     left alone: init warns (stderr; JSON "warnings") for
                                     each of those skills it has no entry for, which then
                                     runs on claude's default model (also read, never
                                     written: regression, effort, caps (see ape project
                                     check --help), timeouts, and limits —
                                     budget: enforce|off (default off; see ape task --help),
                                     attempts: N red verifies per start (default 5; see
                                     ape change verify --help); commit: {subject,
                                     hooks} for the archive's squash, and evidence:
                                     archive|off (default off) — see ape change archive
                                     --help; notify: <command>, run on a limit stop or a
                                     blocked change — see ape task --help); see "Check
                                     settings" below
  AGENTS.md                          the project map (skeleton). Right under its title:
                                     Project context: read @development/project.md before
                                     planning or changing anything.
                                     (the configured development folder; plain text, so
                                     Claude Code imports project.md into every session).
                                     An existing AGENTS.md whose lines carry no
                                     @development/project.md outside code gets that line
                                     after its first # heading (or at the top), and
                                     nothing else — re-run init on a project made before
                                     rc.4 to add it
  CLAUDE.md                          "@AGENTS.md"; an existing CLAUDE.md without that line
                                     gets it appended, and nothing else
  development/project.md             vision, users, scope, out of scope, constraints (skeleton);
                                     optional: stack or tools (either name, D118), notes
  development/roadmap.md             "# Roadmap"
  development/specs/                 empty: no backfill — a spec is born from its first change
  development/changes/               the changes in flight
  development/archive/               archived and abandoned changes
  development/decisions/
  development/briefs/                each plan's input, dated and verbatim (YYYY-MM-DD-<slug>.md,
                                     written by apex-plan; never edited — see ape project check)
  _apex/state/                       ape's own records (never edited by a model; never touched
                                     by ape framework update): milestones.json, decisions.json
                                     tracked; logs/, runs/, hooks/ ignored. Its .gitignore is a
                                     whitelist (*, !.gitignore, !milestones.json, !decisions.json)

Hooks merged into .claude/settings.json (existing keys and hooks are kept):
  SessionStart          ape hook session-start
  Stop, SubagentStop    ape hook stop          (timeout 1800 s: it runs the change's checks)
  PreToolUse            ape hook pre-tool      (matcher Edit|Write|MultiEdit|NotebookEdit|Bash:
                                                the edit guard, and the Bash guards:
                                                git tag writes, git push, pkill/killall,
                                                rebase/merge on a change branch)
A hook that was altered (another matcher, async, a shorter stop timeout) is replaced
by the one above; running init again changes nothing. ape task passes the same
hooks inline as well, so its sessions have them even when settings.json does not.

Everything init wrote (and settings.json when it merged hooks) is committed as
"apex: project init", with the files it amended (CLAUDE.md, AGENTS.md, and the config
when it was tracked and clean); other files that existed before are not touched and
not committed. JSON: "amended" lists the files init changed, "amended_uncommitted"
those it left out of the commit.

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
the marketplace does not serve at ref fails with the command to run. An entry's
allow: [hooks, mcp] lets the plugin carry hooks or MCP servers; ape plugins sync
re-syncs at any time and ape plugins status reports them, and the tools: slot
(see ape plugins --help).

Check settings in _apex/config.yaml (read by start, verify, the gate and the hooks; a
value ape cannot use, or a key it does not know inside these blocks, makes the config
unparseable and every command refuses it):

  regression: make test              the project's own check command, at start/verify/gate, failing
                                     only when newly red; or in tiers:
  regression: {change: make test, milestone: make test-all}
                                     change: at start and verify (baseline-aware);
                                     milestone (else change): at the milestone gate
  timeouts: {check: 20m, regression: {idle: 10m, max: 1h}, gate: {idle: 3m}}
                                     per tier, a Go duration (its max) or {idle, max}. A
                                     command is killed (exit 124, reason idle|ceiling, its
                                     last output lines kept) when it makes no progress —
                                     no new output bytes and no CPU time used by its
                                     process group — for idle, or when it reaches max.
                                     Defaults: check idle 15m / max 2h, regression 30m /
                                     2h, gate 30m / 4h. Any positive duration (no floor);
                                     a scalar's idle is the default clamped to it; idle
                                     above max, or max above ape's hard ceiling (check 4h,
                                     regression 8h, gate 12h), is a config error. A
                                     command's --timeout flag is every tier's max (capped
                                     at the ceiling). A timed-out command is not retried
  retries: 1                         a red check runs again up to n times (0–5, default
                                     0); a pass on a retry is green and recorded flaky:
                                     true (JSON results[].flaky, tries; listed by ape
                                     status). Vacuous and unrun checks are not retried
  checks: {vacuous_output: ["no tests to run", …]}
                                     a check that exits 0 but whose output says it ran
                                     nothing is red, reason "vacuous" (exit -2). Default
                                     list: "no tests to run" (go test), "running 0 tests"
                                     (cargo), "Ran 0 tests" (unittest < 3.12), "0 passing"
                                     (mocha), "collected 0 items" (pytest with exit 5
                                     masked); [] turns it off. A phrase counts as a whole
                                     word, and not when a line without one starts like a
                                     line with one (go test ./... -run X that ran X in one
                                     package: "ok pkgA 0.4s" beside "ok pkgB … [no tests
                                     to run]" is not vacuous)
  requires: {RADIX_DSN: env, docker: cmd}
                                     a check that mentions $RADIX_DSN / ${RADIX_DSN (a
                                     comment counts: "go test ./store/... # needs
                                     $RADIX_DSN") while it is unset or empty, or runs
                                     docker as a command while it is not on PATH, is not
                                     run: reason "unrun" (exit -3), red, never green
  docs: {changelog: CHANGELOG.md, user_docs: docs/}
                                     both optional, project-relative: the changelog and
                                     user docs a user-visible change updates (apex-change
                                     adds the task, apex-review checks it); ape project
                                     check reports a missing path (docs.missing); ape
                                     status --json carries the block as "docs"

Scope (D45): v1.0: one operator per checkout, on a local trunk; PR mode comes later. Init
says so (JSON "scope"); change start and archive refuse while the base branch is behind its
upstream (base.behind-upstream).

--brownfield adds nothing; it records in _apex/state/project.json (ignored) that the
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
			// A v0 project is lifted, not initialised beside its v0 tree.
			if apexv1.IsV0Project(apexv1.FindRoot(dir)) {
				return v1Fail(cmd, &f, apexv1.V0ProjectError(), nil)
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
				// Scope is what v1.0 supports (D45).
				Scope string `json:"scope"`
			}{res, plugins, apexv1.Scope}
			return v1Emit(cmd.OutOrStdout(), &f, out, func(w io.Writer) {
				for _, c := range res.Created {
					fmt.Fprintln(w, "created  "+c)
				}
				for _, k := range res.Kept {
					fmt.Fprintln(w, "kept     "+k)
				}
				uncommitted := map[string]bool{}
				for _, u := range res.AmendedUncommitted {
					uncommitted[u] = true
				}
				for _, a := range res.Amended {
					note := initAmendNote(a)
					if uncommitted[a] {
						note += "; not committed: it was untracked or had uncommitted changes — commit it yourself"
					}
					fmt.Fprintln(w, "amended  "+a+" ("+note+")")
				}
				for _, h := range res.HooksAdded {
					fmt.Fprintln(w, "hook     "+h)
				}
				fmt.Fprint(w, pluginOut.String())
				for _, warn := range res.Warnings {
					fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+warn)
				}
				fmt.Fprintln(w, "scope: "+apexv1.Scope)
				fmt.Fprintln(w, "next: write development/project.md, AGENTS.md and the roadmap (apex-plan), then `ape project check`")
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().BoolVar(&brownfield, "brownfield", false, "record that the project existed before APEX")
	return cmd
}

// initAmendNote says what init changed in an existing file.
func initAmendNote(rel string) string {
	switch rel {
	case "CLAUDE.md":
		return "appended @AGENTS.md"
	case "AGENTS.md":
		return "added the project-context line"
	case "_apex/config.yaml":
		return "appended the models: defaults"
	}
	return "amended"
}

func newProjectCheckCmd() *cobra.Command {
	var f v1Flags
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate every v1 shape and size cap; list all failures",
		Long: `Validate the whole project and list every failure, not just the first.

  development/project.md   # <name>, ## Vision, ## Users, ## Scope, ## Out of scope,
                           ## Constraints (each non-empty); ≤ caps.project_md (16 KB;
                           no line cap)
  development/architecture.md
                           optional; when present ≤ caps.architecture_md (16 KB;
                           architecture.too-big: move detail to specs/, decisions/ or
                           docs/explanation/)
  AGENTS.md                present, ≤ caps.agents_md (16 KB; its lines are not measured);
                           a line outside code carries @development/project.md (the
                           configured folder; the reference counts, not the wording —
                           agents.no-project-context: ` + "`ape project init`" + ` adds the line);
                           CLAUDE.md (if any) has the line @AGENTS.md
  development/roadmap.md   # Roadmap, ≥ 1 "## M<n> — <title>" with Status (ape-written),
                           Gate, Goal; Budget a number when present; a "## Next"
                           section holds findings (` + "`ape next add`" + `), not a milestone. Its Delivery: line
                           links the current brief, e.g.
                           ` + "`Delivery: Phase 1 — \"…\" ([brief](briefs/2026-10-16-<slug>.md))`" + `;
                           a Markdown link or a bare briefs/….md path, relative to
                           roadmap.md (or development/briefs/… from the project root),
                           must resolve (roadmap.delivery-link); briefs with no linking
                           Delivery: line are roadmap.delivery-unlinked. An approved
                           milestone has ape's approval record (milestone.status-
                           unrecorded; a done one, ape's close) and its gate as approved
                           (milestone.gate-changed): a person approves it again
  development/briefs/      YYYY-MM-DD-<slug>.md with a real date (brief.bad-name);
                           committed with the plan (brief.uncommitted); never changed
                           from the text of the commit that added it, in the working
                           tree or a later commit (brief.edited: restore it, write a
                           new dated brief)
  specs/<cap>/spec.md      # title, Purpose: (the line and those after it up to a blank
                           line, heading or fenced code), "### Requirement: <name>" each
                           with ≥ 1 "#### Scenario:" and exactly one "- check: <cmd> |
                           human-review" per scenario; ≤ caps.spec (24 KB; "split into two
                           capabilities")
  decisions/NNNN-<slug>.md # ADR-NNNN: title, Status, Date, Context/Decision/Consequences,
                           Enforced by; an accepted one recorded in decisions.json
                           (decision.accepted-unrecorded: ` + "`ape decision accept`" + ` records it)
  changes/<id>/            every non-archived change, as ` + "`ape change check`" + `
  layout                   no development/changes/archive/ (an older ape's archive: move
                           its folders to development/archive/), no development/.apex/
                           (an older ape's state: move milestones.json and decisions.json
                           to _apex/state/, delete it; ` + "`ape project init`" + ` then adds
                           _apex/state/.gitignore)
  lift                     development/lift-disposition.md in a project whose v1 epoch is a
                           lift commit ("apex: lift …"): the disposition list awaits review
                           (lift.disposition-pending) until the person deletes the file;
                           its full lists, _apex/state/lift-disposition.json, are ignored
                           state and never asked for
  config                   since the v1 epoch, a commit that changed a guarded block of
                           _apex/config.yaml — limits, checks, retries, requires, timeouts,
                           caps, regression, commit (types included), evidence,
                           marketplaces, plugins, tools, human_review, release (D120) —
                           and is not ape's (config.changed, D50) — a person reverts it or
                           accepts it: ` + "`ape project accept-config`" + `
  caps                     _apex/config.yaml caps: (D69) — sizes like 16KB, 1MiB or plain
                           bytes, KB = KiB = 1024 bytes; default / hard ceiling:
                             project_md   16 KB / 48 KB    agents_md    16 KB / 48 KB
                             change_md    16 KB / 48 KB    design_md    16 KB / 48 KB
                             spec         24 KB / 64 KB    (per capability)
                             evidence_log  1 MiB / 16 MiB  (per check, evidence: archive)
                             architecture_md 16 KB / 48 KB (development/architecture.md, optional)
                           A value above its ceiling is caps.over-ceiling (action person):
                           reported here, and change check, start and archive refuse
                           until a person fixes it (abandon measures nothing, D89)
                           change_md counts change.md without ## Notes (D89); the whole
                           change.md, Notes included, has the 48 KB ceiling whatever the
                           cap (change.over-ceiling, action apply)
  models                   each _apex/config.yaml models: key names a skill — one of the
                           defaults ` + "`ape project init`" + ` writes, a project or user skill
                           (.claude/skills/<key>/SKILL.md), or a skill tools: or
                           human_review names — else models.unknown-skill: the entry pins
                           nothing (models.effort is not a skill: a skill's effort goes
                           inside its entry, {model, effort}; the default for every
                           session is the top-level effort:); an entry is a model name or
                           {model, effort}, any other field is models.unknown-field (both
                           action person)
  docs                     each path _apex/config.yaml docs: names (changelog, user_docs)
                           exists (docs.missing: create it, or fix the path)
  .claude/settings.json    ape's hooks registered as ` + "`ape project init`" + ` writes them:
                           hooks.missing / hooks.altered (matcher, type, async, a
                           stop timeout below 1800 s); ` + "`ape project init`" + ` restores them;
                           and shared through git: settings.ignored when git ignores it
                           (and does not track it), since its hooks then reach no other
                           checkout — only ` + "`ape task`" + ` sessions get them inline
  provenance               since the v1 epoch (the newest "apex: project init" or "apex: lift"
                           commit; nothing before it is judged), one git log walk: a commit
                           touching development/specs/** must be an archive squash — a
                           "Change: <id>" trailer whose change is archived, whatever its
                           subject — else spec.edited; a change's state.json may change only
                           in an "apex: …" commit or an archive squash, else state.forged.
                           A git revert of such a commit clears both (both action person)
  plugins                  _apex/config.yaml marketplaces/plugins: settings.json's
                           extraKnownMarketplaces/enabledPlugins match them, and (when
                           claude is on PATH) each plugin is installed at project scope
                           at its ref (plugins.drift; plugins.config for a bad config);
                           ` + "`ape plugins sync`" + ` (or ` + "`ape doctor --fix`" + `) puts them right;
                           an installed one that carries hooks or MCP servers its entry
                           does not allow (allow: [hooks, mcp]) is plugin.hooks-or-mcp
  tools                    _apex/config.yaml tools: {design: [{name, skill, when, args?,
                           model?, effort?}], review: [...]}: well-formed — args:
                           placeholders only {id} {task} {assets} {change}, effort: a
                           level — (tools.invalid), each skill
                           installed — <plugin>:<skill> of a listed plugin, or a project
                           or user skill — (tools.not-installed) and not
                           user-invocable: false (tools.not-invocable); a plugin skill
                           is judged only when claude is on PATH. See ape plugins --help

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
			cli := apexv1.ListOnce(pluginCLIOrNil())
			fs = append(fs, p.CheckPlugins(cmd.Context(), cli)...)
			fs = append(fs, p.CheckPluginContents(cmd.Context(), cli)...)
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
"diagnostics": [{"severity", "code", "message", "fix", "action", "file"?, "line"?}], ...}:
the first diagnostic is the failure itself, the rest its findings or conflicts. Branch on
"action" (D47): retry (run the same command again), apply (apex-apply: the work, or a
re-start), review (review again), replan (apex-change re-plans), stop (the run stops), person
(a person must act: list it, go on with other work). A code's action is the one listed here;
where its meaning names a case that changes it (milestone.changes-in-flight), the diagnostic
carries the action of the case (D49). "code" says which rule; "message" and
"fix" are for people. Codes are stable: a new rule gets a new
code, and a code's meaning never changes. severity is always "error" (no warning tier).
Exit: 1 check failed · 2 usage · 3 conflict (nothing written).`,
		Example: "  ape project codes\n  ape project codes --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return v1Emit(cmd.OutOrStdout(), &f, apexv1.DiagnosticCodes, func(w io.Writer) {
				for _, c := range apexv1.DiagnosticCodes {
					fmt.Fprintf(w, "%-30s %d  %-7s %s\n", c.Code, c.Exit, c.Action, c.Meaning)
				}
			})
		},
	}
	cmd.Flags().BoolVar(&f.json, "json", false, "machine output (JSON on stdout)")
	return cmd
}

func newStatusCmd() *cobra.Command {
	var f v1Flags
	var brief, board bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the current milestone, active changes, blocks, pending human review and spend",
		Long: `Show where the project stands, from the records ape keeps:

  - the current milestone (the first not done): status, gate and its last recorded run,
    budget, and the spend of runs started since it was approved
  - the milestone's start (JSON milestone.base_sha, and base_sha on each milestones[] line
    once approved; D130): HEAD at its first approval — what a gate gets as
    APE_MILESTONE_BASE, for a gate run by hand or a range since the milestone began
  - the milestone's limit stops (JSON milestone.stops: reason, at, run, skill, spent_usd,
    budget_usd, session_usd): sessions ape task stopped at its enforced budget
    (limits.budget: enforce; JSON milestone.budget_enforced), whose measured cost the
    spend counts
  - the milestone's changes blocked in a row (JSON milestone.consecutive_blocks and
    consecutive_blocks_cap, limits.consecutive_blocks) and, when that cap is reached, the
    limit that stops work on it: milestone.diagnostics, in the shape of a --json failure's
    diagnostics — [{code: "milestone.blocks-exceeded", action: "stop", …}] (D49; [] when
    none), and next says so
  - a milestone a person closed with its gate red (` + "`ape milestone close --accept-red`" + `):
    JSON milestones[].accept_red {reason, by, at, gate}; the brief names the last one, and
    the roadmap lines say "accepted red by <who>: <reason>"
  - the checked-out branch (JSON branch; "(on <branch>)" in the first line)
  - fixes first (trial decision D40): a change with "milestone: none" is a fix (JSON
    changes[].lane "fix"; else "milestone"). Approved fixes (changes[].approved: {by, at,
    plan_digest}) are listed before milestone changes — started ones first, then by
    approval time — and JSON "fixes" holds the ids of those not blocked, in the order they
    run: the first runs ahead of milestone work, at the orchestrator's next change boundary
    (never in parallel), or alone with ` + "`ape task apex-orchestrator --args <id>`" + `, and next
    points at it. A fix awaiting approval is listed last, with the command a person runs
    (` + "`ape change approve <id> --by <who>`" + `). A fix's runs (` + "`ape task <skill> --args <fix-id>`" + `)
    count toward no milestone's spend
  - every change not archived, with its status, blocked reason, red checks from its
    last verify (JSON failing: "T1 (exit 1)", "T2 (vacuous: …)", "S (unrun: $X is unset
    …)" — a red verify of an in-progress change comes from this checkout's
    _apex/state/logs/<id>/last-verify.json), the checks that passed only on a retry
    (JSON flaky; "flaky: T2" in the brief; retries: in _apex/config.yaml), human-review tasks not yet accepted, and for a started change its red
    verifies since it started against limits.attempts (JSON attempts, attempts_cap;
    "(3/5 attempts)"; attempts_total, never reset) — a change the cap blocked shows
    "attempts exceeded" as its reason. The counts are this checkout's
    _apex/state/attempts.json (git-ignored). A started change is worked on its own
    branch, change/<id> (D38; JSON changes[].branch), and its status is read from
    there whatever branch is checked out — a change branch lists the changes it does
    not hold too
  - an answered change (H9): a person's answer to what blocked it (JSON changes[].answer:
    {by, at, text}, kept once it is started again); next has it re-planned and started
  - human review (H4, D47): every change lists awaiting_human (always present in JSON, []
    when none): its human-review tasks no person has accepted, once only the person is
    left — the change is reviewed, or archived. The orchestrator skips such a change. A
    reviewed change awaiting a person is not archived next — next names the accept a
    person runs. Archived changes
    with unaccepted human-review tasks (archived past them by a person) are listed after
    the rest, status "archived", under "Archived, human review not accepted"; they stay
    acceptable (` + "`ape change accept --help`" + `)
  - tools (D59 Q4, D74): every change lists tools, its design-tool tasks
    ("— tool: <name>"), as [{task, name, skill, done}] — done: the task green at the
    latest verify of the current plan, or a human-review one accepted — and
    review_tools, its review_tools: frontmatter, as [{name, skill}]; skill is what
    _apex/config.yaml tools: names ("" when it names none). Both always present ([])
  - acceptances (JSON changes[].accepted: [{task, by, at, evidence, run}]): by a person,
    or by "delegate:<skill>" with its run when _apex/config.yaml human_review delegates
    them (D75; "Accepted by a delegate" in the long output); and each human-review task's
    newest delegate verdict (JSON changes[].delegate: {"T2": {verdict, reason, run, at,
    voided}}): a fail or a voided run leaves the task awaiting a person, and the brief
    says why. With human_review set, next names the delegate for an awaiting task it has
    not judged (` + "`ape change accept <id> <T> --delegate --wait 9m`" + `) before a person
  - spend so far: the sum of finished ape task/pipeline run manifests
  - red at baseline, not any change's own (JSON "pre_existing_red": [{change, kind, ref,
    cmd, exit, sha}]): the regression command, and scenario checks of the spec a change
    leaves untouched, that were red when a change was baselined — for the planner to
    roadmap. A change's own task and delta checks red at baseline are normal check-first
    and never listed
  - the base branch (the checked-out one, or the base of the change whose branch is
    checked out) against its upstream (JSON "base_upstream": {branch, upstream, ahead,
    behind}; null without one), from the local remote-tracking ref — ape never fetches.
    v1.0 is one operator per checkout on a local trunk (D45): start and archive refuse
    while the base is behind (base.behind-upstream: pull)
  - work outside the change flow (JSON "outside_flow": [{sha, subject, files}], newest
    first; a count line in the human output): commits on that base branch since the last
    archive squash — else since the v1 epoch, the newest "apex: project init" or "apex:
    lift" commit — that touch paths outside development/ and _apex/, carry no "Change:"
    trailer and have no "apex:" subject. An apex-release commit ("Release:" trailer)
    that touches only the changelog (docs.changelog, else CHANGELOG.md) is not
    listed. Shown, never failed
  - an archive in flight or interrupted by a crash (JSON "archive_journal", with
    "archive_running" when its process is alive; on the first line): status only reports
    it — the next writing command rolls a crashed one back (archive.interrupted; see
    ` + "`ape change archive --help`" + `)
  - next: what the way of working says comes next, derived from the state alone
  - the roadmap's Next (JSON "roadmap_next": ["2026-10-16 <text> (from <id>)", …], []
    when none; a count line in the human output): the findings noticed out of scope that
    apex-plan triages, as the base branch's roadmap.md holds them (` + "`ape next add`" + ` writes
    there from any branch). Not the same field as "next", the step above
  - JSON "docs": {changelog, user_docs} from _apex/config.yaml docs: ({} when unset), for
    the skills that keep a changelog and user docs current
  - JSON "config": {commit_types, release_scheme, release_docs} — the project's commit
    types (commit.types, default feat, fix, perf, refactor, docs, test, build, ci, chore),
    release scheme (release.scheme: semver, the default, date or edition) and the docs a
    release brings up to date (release.docs, default README.md and docs/ where they exist;
    D129), defaults filled in, for apex-change and apex-release (D120)
  - the latest ape task run (JSON "latest_run": {skill, run_id, status, stopped,
    resets, resets_at, message, depth, parent_run}): how it ended, e.g. "stopped:
    usage-limit, resets 2026-10-16T23:00:00+02:00" when a claude.ai usage limit
    stopped it (see ape task --help, exit 14); a session an orchestrator started
    names that run ("started by <run>"; depth 2, parent_run)
  - JSON "sessions": the model sessions ape launched that run in this project now
    ([{run_id, kind, skill, depth, parent_run, pid, started_at}], [] when none): an
    apex-orchestrator run at depth 1 and its one child at depth 2 (D62)

--brief renders at most 25 lines (what the SessionStart hook injects).

The board (D60): in a project with a board (.aboard/, see ` + "`ape aboard --help`" + `), ape keeps
one tab, "APEX status" (key ape-status), in step with this report. It is refreshed best
effort after every v1 command that changes what status reports — project init, lift and
accept-config; milestone new, approve, gate and close; decision new and accept; next add;
every change verb but check and diff — and by --board, which refreshes it now and says
where it wrote (JSON "board": {action: posted|wrote|unchanged|no-board|skipped, where,
detail}). A running board is posted to; with none running, .aboard/aboard.json is
rewritten in place. ape never creates a board. The tab is not authoritative: it is a view
of this report, nothing reads it back, and where the two disagree the tab is wrong. A
refresh never changes a command's exit code or stdout (a problem is one line on stderr),
and one that would change nothing writes nothing. The tab is quiet (aboard v0.4.0, D126):
ape's refreshes raise no notification, unread dot or banner. Going quiet is announced once,
when ape creates the tab (or first refreshes one an older ape made). ape sets quiet only
that once: if a person turns it off from the tab's menu, it stays off, and ape's changed
views raise the dot again.

Exit codes: 0 ok · 2 usage. --board exits 0 whatever the board did.`,
		Example: "  ape status\n  ape status --brief\n  ape status --json\n  ape status --board",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RefuseV0(cmd, &f, p); err != nil {
				return err
			}
			r := p.Status()
			if board {
				o := refreshBoardNow(cmd.Context(), p, r, cmd.ErrOrStderr())
				return v1Emit(cmd.OutOrStdout(), &f, statusWithBoard{StatusReport: r, Board: &o}, func(w io.Writer) {
					if brief {
						fmt.Fprint(w, r.Brief())
					} else {
						fmt.Fprint(w, statusLong(r))
					}
					fmt.Fprintln(w, boardLine(p, o))
				})
			}
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
	cmd.Flags().BoolVar(&board, "board", false, "refresh the project's aboard status tab now and say where it wrote (JSON \"board\"); "+
		"the tab is a view, never a record; until ape pins the aboard release that honours its quiet flag, "+
		"a changed view posted to a running board raises the tab's unread dot")
	return cmd
}

// statusWithBoard is `ape status --board --json`: the report, plus what
// the board refresh did.
type statusWithBoard struct {
	*apexv1.StatusReport
	Board *statusboard.Outcome `json:"board"`
}

func statusLong(r *apexv1.StatusReport) string {
	var b strings.Builder
	b.WriteString(r.Brief())
	if len(r.Milestones) > 0 {
		b.WriteString("\nRoadmap:\n")
		for _, m := range r.Milestones {
			fmt.Fprintf(&b, "  %-4s %-9s %s", m.ID, m.Status, m.Title)
			if a := m.AcceptRed; a != nil {
				fmt.Fprintf(&b, " — accepted red by %s: %s", a.By, a.Reason)
			}
			b.WriteString("\n")
		}
	}
	var delegated []string
	for i := range r.Changes {
		c := &r.Changes[i]
		for _, a := range c.Accepted {
			if strings.HasPrefix(a.By, apexv1.DelegateByPrefix) {
				delegated = append(delegated, fmt.Sprintf("  %-28s %-4s by %s (run %s)", c.ID, a.Task, a.By, a.Run))
			}
		}
	}
	if len(delegated) > 0 {
		b.WriteString("\nAccepted by a delegate (D75):\n" + strings.Join(delegated, "\n") + "\n")
	}
	if r.Milestone != nil && r.Milestone.GateLast != nil && !r.Milestone.GateLast.Green() {
		fmt.Fprintf(&b, "\nLast gate output (%s):\n", r.Milestone.GateLast.Log)
		for _, l := range r.Milestone.GateLast.Tail {
			b.WriteString("  | " + l + "\n")
		}
	}
	if n := len(r.OutsideFlow); n > 0 {
		fmt.Fprintf(&b, "\nOutside the change flow: %d commit(s) on the base since the last archive touch code with no Change: trailer (newest %.7s %s; ape status --json: outside_flow)\n",
			n, r.OutsideFlow[0].SHA, r.OutsideFlow[0].Subject)
	}
	if n := len(r.RoadmapNext); n > 0 {
		fmt.Fprintf(&b, "\nNext (roadmap): %d finding(s) to triage at the next delivery (ape status --json: roadmap_next)\n", n)
	}
	if u := r.BaseUpstream; u != nil && u.Behind == 0 {
		// Behind is in the brief already.
		fmt.Fprintf(&b, "\nBase: %s\n", u.Describe())
	}
	if r.LatestRun != nil {
		fmt.Fprintf(&b, "\nLatest run: %s\n", r.LatestRun.Describe())
	}
	for i, s := range r.Sessions {
		if i == 0 {
			b.WriteString("\nRunning sessions:\n")
		}
		fmt.Fprintf(&b, "  %s %s %s, depth %d", s.Kind, s.RunID, s.Skill, s.Depth)
		if s.ParentRun != "" {
			fmt.Fprintf(&b, ", started by %s", s.ParentRun)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nSpend note: %s\n", r.Spend.Note)
	return b.String()
}
