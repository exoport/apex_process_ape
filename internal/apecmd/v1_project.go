package apecmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/statusboard"
	"github.com/spf13/cobra"
)

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Create and check an APEX v1 project layout",
	}
	cmd.AddCommand(newProjectInitCmd(), newProjectCheckCmd(), newProjectCodesCmd(), newProjectLiftCmd(), newProjectAcceptConfigCmd(),
		newProjectArchiveLayoutCmd(), newProjectRewriteLiveCmd(), newProjectLiftDispositionCmd())
	// rc.7 (rc7-rename).
	cmd.AddCommand(newProjectOperatorInterfaceCmd())
	// v1.1 W7.
	cmd.AddCommand(newProjectFenceCmd(), newProjectPackageCheckCmd())
	return cmd
}

// projectInitLongHelp is `ape project init --help`'s text.
const projectInitLongHelp = `Create the APEX v1 skeleton where it is absent. Nothing that exists is overwritten
(the exceptions: the @AGENTS.md line appended to a CLAUDE.md that lacks it, the
project-context line added to an AGENTS.md that lacks it, and the models: defaults
appended to an _apex/config.yaml that has no models: block).

A new project installs the framework's skills first, then runs init: ` + "`ape framework setup`" + `,
then ` + "`ape project init`" + ` (each names the other in its next:, D162). init's next: asks
for setup while no .claude/skills/apex-* exists; JSON "next" carries the same line.

  _apex/config.yaml                  project_name, development_folder, and the models:
                                     defaults (D65, D93) — apex-apply, apex-loop
                                     and apex-release {model: sonnet, effort: high};
                                     apex-review, apex-plan, apex-change and apex-accept
                                     {model: opus, effort: medium}. No row for the
                                     operator's apex-orchestrator: it runs in your own
                                     session, on the model you chose (D175, D176). A
                                     models.apex-orchestrator row (a project made before
                                     rc.7) is read as apex-loop's while there is no
                                     apex-loop row, with a warning, until ape project
                                     operator-interface moves it (see its --help). An existing config
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
                                     hooks} for the archive's squash (no commit: line
                                     is written: the subject defaults to
                                     "{type}: {title}", D193), and evidence:
                                     archive|off (default off) — see ape change archive
                                     --help; notify: <command>, run on every event ape
                                     logs in _apex/state/events.jsonl — see ape status
                                     --help); see "Check
                                     settings" below
  AGENTS.md                          the project map (skeleton). Right under its title:
                                     Project context: read @development/project.md before
                                     planning or changing anything.
                                     (the configured development folder; plain text, so
                                     Claude Code imports project.md into every session),
                                     then the line naming the operator's interface (A13):
                                     Talk to APEX: type ` + "`/apex-orchestrator`" + ` in Claude Code.
                                     (an existing AGENTS.md is not given it: ape project
                                     operator-interface adds it).
                                     An existing AGENTS.md whose lines carry no
                                     @development/project.md outside code gets that line
                                     after its first # heading (or at the top), and
                                     nothing else — re-run init on a project made before
                                     rc.4 to add it
  CLAUDE.md                          "@AGENTS.md"; an existing CLAUDE.md without that line
                                     gets it appended, and nothing else
  development/project.md             vision, users, scope, out of scope, constraints (skeleton);
                                     optional: stack or tools (either name, D118), notes
  development/roadmap.md             "# Roadmap", then the line pointing at Next:
                                     ` + "`Next — not milestones: [next.md](next.md)`" + ` (D217)
  development/next.md                Next (D217): "# Next" and a line saying these are
                                     findings to triage, not milestones — written only
                                     with a new roadmap; an existing roadmap keeps its
                                     Next until ape next move moves it
  development/specs/                 empty: no backfill — a spec is born from its first change
  development/changes/               the changes in flight
  development/archive/               archived and abandoned changes
  development/decisions/
  development/decisions/0001-record-architecture-decisions.md
                                     ADR-0001, APEX's standard ADR "Record architecture
                                     decisions" (D205), accepted: the installed payload's
                                     _apex/standards/adr-0001.md, else ape's own copy;
                                     recorded in decisions.json as accepted by "APEX
                                     standard (ape project init)" with standard:
                                     {framework_version, sha256} — the framework's own
                                     record, no person act. Kept when ADR-0001 is the
                                     standard; any other ADR-0001 is left as it is (JSON
                                     standard_adr.action differs; see ape decision
                                     standard)
  development/briefs/                each plan's input, dated and verbatim (YYYY-MM-DD-<slug>.md,
                                     or a YYYY-MM-DD-<slug>/ folder with brief.md; written by
                                     apex-plan; never edited — see ape project check)
  hot/.gitignore                     "*" when hot/ has no .gitignore (an existing one is kept):
                                     hot/ holds the project's secrets, which gates and checks
                                     read at run time ("$(cat hot/<name>)"), never as
                                     literals. The "*" ignores the .gitignore too, so hot/
                                     stays this checkout's and is not in the init commit
                                     (see ape project check: hot.not-ignored)
  _apex/state/                       ape's own records (never edited by a model; never touched
                                     by ape framework update): milestones.json, decisions.json
                                     tracked; logs/, runs/, hooks/ ignored. Its .gitignore is a
                                     whitelist (*, !.gitignore, !milestones.json, !decisions.json)

Hooks merged into .claude/settings.json (existing keys and hooks are kept):
  SessionStart          ape hook session-start
  Stop, SubagentStop    ape hook stop          (timeout 1800 s: it runs the change's checks)
  PreToolUse            ape hook pre-tool      (matcher Edit|Write|MultiEdit|NotebookEdit|Bash|Monitor:
                                                the edit guard, and the Bash guards, on a
                                                Bash or a Monitor command (Claude Code
                                                2.1.310+): git tag writes, git push, pkill/killall,
                                                rebase/merge/cherry-pick on a change
                                                branch; "onFailure": "block", so a
                                                guard that cannot run blocks the call)
  UserPromptSubmit      ape hook user-prompt   (records the person's own prompts, for
                                                person acts typed in Claude Code (E2))
A hook that was altered (another matcher, async, a shorter stop timeout, another
onFailure) is replaced by the one above; a pre-tool hook with no onFailure, as v1.0
wrote it, gains the key in place (project check: notice hooks.fail-open), and one with
the matcher before v1.3, without Monitor, gains Monitor in place (project check: notice
hooks.monitor-unguarded). Running init again changes nothing. ape task passes the same
hooks inline as well, except UserPromptSubmit, so its sessions have them even when
settings.json does not.

Git's pre-commit hook (Q89): in a git work tree, init installs <hooks dir>/pre-commit
(` + "`git rev-parse --git-path hooks`" + `: core.hooksPath when set, else .git/hooks) when no
pre-commit hook is there — a sh script marked "# ape: pre-commit secret scan" that runs
` + "`ape hook pre-commit`" + `, the commit secret scan, so a session's own git commit and a
person's are scanned as ape's commits are (see ape hook pre-commit --help). An existing
hook that is not ape's is kept, never edited: ape project check then lists the notice
hooks.pre-commit-absent with the line to add. The output's "git hook" line and JSON
"pre_commit" ({path, status: installed|present|kept, runs_ape}) say which happened. git
never versions .git/hooks, so a fresh clone has none: run init again, which installs it.

What .claude/ commits (D203): .claude/settings.json (ape's hooks) and .claude/skills/
(the framework's skills) are the team's and tracked; the rest of .claude/
(settings.local.json, your own agents) and CLAUDE.local.md are personal. init puts that
shape in the root .gitignore (created when absent):
  # APEX (ape): .claude/settings.json and .claude/skills/ are the team's; …
  .claude/*
  !.claude/settings.json
  !.claude/skills/
  CLAUDE.local.md
With no .claude rule the block is appended (only its missing lines when part of it is
there); an ignore of the whole folder (.claude, .claude/, /.claude, /.claude/, .claude/**,
/.claude/**) is rewritten in place to it, since git never reads a negation under an
ignored folder; your own .claude/<x> lines and every other line are kept as they are.
Idempotent. The output's ".gitignore:" line and JSON "gitignore" ({action:
added|rewritten|already, created, uncommitted, revealed}) say which; "revealed" lists
the files under .claude/ it un-ignored that init does not commit (a skill of your own:
commit it or ignore it by name). A settings.json it un-ignored is committed with the
hooks. An ignore elsewhere (a nested .gitignore, .git/info/exclude, core.excludesFile)
is not rewritten: ape project check reports it (settings.ignored).

Everything init wrote but hot/.gitignore and a hook under .git/ (and settings.json
when it merged hooks) is committed as "apex: project init", with the files it amended (CLAUDE.md, AGENTS.md, and the config
when it was tracked and clean) and the .gitignore when it was absent or tracked and
clean; other files that existed before are not touched and
not committed. JSON: "amended" lists the files init changed, "amended_uncommitted"
those it left out of the commit.

In a model session (APE_SESSION or CLAUDECODE set) on a project that is already v1,
init writes and repairs as above but commits nothing (Q156): an "apex: project init"
commit restarts the v1 epoch and would hide every finding before it, so only a
person's init makes one after the first. The files stay for the session's own commit
(apex-plan's plan: commit takes them); JSON "commit_withheld" lists them, with a
warning. A project's first init commits from a session as from a terminal.

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
                                     milestone (else change): at the milestone gate, its
                                     baseline taken by the milestone's first start
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
  docs: {changelog: CHANGELOG.md, user_docs: docs/, tested: [docs/examples/]}
                                     all optional, project-relative: the changelog and
                                     user docs a user-visible change updates (apex-change
                                     adds the task, apex-review checks it); ape project
                                     check reports a missing path (docs.missing); ape
                                     status --json carries the block as "docs". tested
                                     (a list, each path once) names the docs the project
                                     executes (doc-tests, tested examples): a change
                                     touching one is never docs-only, so ape change
                                     verify runs its regression command (see its help)

Scope (D45): v1.0: one operator per checkout, on a local trunk; PR mode comes later. Init
says so (JSON "scope"); change start and archive refuse while the base branch is behind its
upstream (base.behind-upstream).

A library (W7, D208): with a go.mod at the root and no main package (examples/, testdata/,
generators built only with //go:build ignore and the development folder's gate programs
do not count), and no library: key in _apex/config.yaml, init prints
  suggest: add "library: true" to _apex/config.yaml if this is a library others download — …
(JSON "suggest_library": {line, file, reason}) and never writes the key: a person decides.
With library: true already in the config and a go.mod at the root, init writes the
missing module fences (one go.mod in .claude/, _apex/ and the development folder, and a
root evidence/ when there is one; JSON "fences") into its commit — see ape project fence
--help.

--brownfield adds nothing; it records in _apex/state/project.json (ignored) that the
project existed before APEX.

Exit codes: 0 ok · 1 failed (e.g. settings.json is not valid JSON) · 2 usage.`

func newProjectInitCmd() *cobra.Command {
	var f v1Flags
	var brownfield bool
	cmd := &cobra.Command{
		Use:     "init",
		Short:   "Create the development/ skeleton and merge ape's hooks (never overwrites)",
		Long:    projectInitLongHelp,
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
			next := initNext(res.Root)
			out := struct {
				*apexv1.InitResult
				Plugins *apexv1.PluginSync `json:"plugins,omitempty"`
				// Scope is what v1.0 supports (D45).
				Scope string `json:"scope"`
				// Next is the step after init: the framework's skills first
				// when they are absent (D162).
				Next string `json:"next"`
			}{res, plugins, apexv1.Scope, next}
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
				printInitHooks(w, res)
				// W7: init never sets library: true; it suggests it.
				if sl := res.SuggestLibrary; sl != nil {
					fmt.Fprintln(w, "suggest: "+sl.Text())
				}
				fmt.Fprint(w, pluginOut.String())
				for _, warn := range res.Warnings {
					fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+warn)
				}
				fmt.Fprintln(w, "scope: "+apexv1.Scope)
				fmt.Fprintln(w, "next: "+next)
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().BoolVar(&brownfield, "brownfield", false, "record that the project existed before APEX")
	return cmd
}

// printInitHooks prints the hooks init merged, what it did to the root
// .gitignore (D203) and to git's pre-commit hook.
func printInitHooks(w io.Writer, res *apexv1.InitResult) {
	for _, h := range res.HooksAdded {
		fmt.Fprintln(w, "hook     "+h)
	}
	if res.StandardADR != nil {
		fmt.Fprintln(w, "ADR-0001 "+adrStandardLine(res.StandardADR))
	}
	for _, l := range apexv1.ClaudeIgnoreLines(res.GitIgnore) {
		fmt.Fprintln(w, l)
	}
	if pc := preCommitLine(res.PreCommit); pc != "" {
		fmt.Fprintln(w, pc)
	}
}

// preCommitLine says what init or the lift did with git's pre-commit hook
// (Q89); "" outside a git work tree.
func preCommitLine(h *apexv1.PreCommitHook) string {
	switch {
	case h == nil:
		return ""
	case h.Status == apexv1.PreCommitInstalled:
		return "git hook " + h.Path + " (installed: runs `" + apexv1.HookCmdPreCommit + "`, the secret scan)"
	case h.Status == apexv1.PreCommitPresent:
		return "git hook " + h.Path + " (ape's, already installed)"
	case h.RunsApe:
		return "git hook " + h.Path + " (kept existing: not ape's, and it runs `" + apexv1.HookCmdPreCommit + "`)"
	}
	return "git hook " + h.Path + " (kept existing: not ape's, so commits ape does not make are not scanned — add the line `" +
		apexv1.HookCmdPreCommit + " || exit 1` to it; ape project check lists hooks.pre-commit-absent)"
}

// initNext is init's next step: with no framework skills installed
// (.claude/skills/apex-*), `ape framework setup` first (D162: setup, then
// init; each names the other).
func initNext(root string) string {
	const after = "write development/project.md, AGENTS.md and the roadmap (apex-plan), then `ape project check`"
	if m, _ := filepath.Glob(filepath.Join(root, ".claude", "skills", "apex-*")); len(m) == 0 {
		return "install the framework's skills with `ape framework setup`, then " + after
	}
	return after
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
	case "_apex/state/decisions.json":
		return "recorded the standard ADR-0001"
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
                           Gate, Goal; Budget a number when present; the line
                           ` + "`Next — not milestones: [next.md](next.md)`" + ` points at Next
                           (D217); a "## Next" section that still holds findings is
                           not a milestone (notice roadmap.next-inline). Its Delivery: line
                           links the current brief, e.g.
                           ` + "`Delivery: Phase 1 — \"…\" ([brief](briefs/2026-10-16-<slug>.md))`" + `;
                           a Markdown link or a bare briefs/….md path, relative to
                           roadmap.md (or development/briefs/… from the project root),
                           must resolve (roadmap.delivery-link); briefs with no linking
                           Delivery: line are roadmap.delivery-unlinked. An approved
                           milestone has ape's approval record (milestone.status-
                           unrecorded; a done one, ape's close) and its gate as approved
                           (milestone.gate-changed): a person approves it again
  development/next.md      optional (D217); only its items are checked: a leading date
                           is a real YYYY-MM-DD with text after it, a "(from <id>)"
                           names a change id (next.bad-item); an undated item, as
                           apex-plan wrote them, is accepted
  development/briefs/      YYYY-MM-DD-<slug>.md with a real date, or a brief folder
                           YYYY-MM-DD-<slug>/ whose brief.md is the brief and whose
                           other files (assets/**, source.txt) are part of it: the
                           name rule is the folder's (brief.bad-name, also for a
                           dated folder with no brief.md); one brief, and Delivery:
                           links its brief.md. Every file committed with the plan
                           (brief.uncommitted); never changed from the text of the
                           commit that added it, in the working tree or a later
                           commit (brief.edited: restore it, write a new dated brief)
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
                           marketplaces, plugins, tools, human_review, release (D120),
                           secrets (D155) — and is not ape's (config.changed, D50) — a
                           person reverts it or accepts it: ` + "`ape project accept-config`" + `
  hot/                     the project's secrets (D155): when hot/ exists, git ignores a new
                           file in it (hot/.gitignore "*", which init and the lift write, or
                           any ignore rule), and git tracks nothing under it — else
                           hot.not-ignored (action person: give it the .gitignore and
                           untrack the rest, ` + "`git rm -r --cached hot/`" + `); no hot/ is no finding
  caps                     _apex/config.yaml caps: (D69) — sizes like 16KB, 1MiB or plain
                           bytes, KB = KiB = 1024 bytes; default / hard ceiling:
                             project_md   16 KB / 48 KB    agents_md    16 KB / 48 KB
                             change_md    16 KB / 48 KB    design_md    16 KB / 48 KB
                             spec         24 KB / 64 KB    (per capability)
                             evidence_log  1 MiB / 16 MiB  (per check, evidence: archive)
                             architecture_md 16 KB / 48 KB (development/architecture.md, optional)
                             advise        8 KB / 16 KB   (one tools.advise entry's files, W6)
                             advise_session 16 KB / 32 KB (the advise files one session reads)
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
  .claude/skills/          shared through git too (D203): settings.ignored when git
                           ignores a skill folder there (asked of one no release ships),
                           since the framework's skills then reach no other checkout.
                           ` + "`ape project init`" + ` puts the .claude/ shape in the root
                           .gitignore (see its --help); an ignore elsewhere is removed
                           by hand
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
                           does not allow (allow: [hooks, mcp]) is plugin.hooks-or-mcp;
                           a listed plugin also installed or enabled at user scope, or a
                           tools: skill that is a user skill, is plugins.user-scope
                           (action person, W6): the user copy wins on this machine
  tools                    _apex/config.yaml tools: {design: [{name, skill, when, args?,
                           model?, effort?, paths?, touches?}], review: [...], advise:
                           [{name, skill, when, load, for?, paths?, touches?}]}:
                           well-formed — args: placeholders only {id} {task} {assets}
                           {change}, effort: a level, paths: relative globs, touches:
                           kebab slugs, load: paths inside the skill's folder, for:
                           plan, change, apply, review — (tools.invalid), each skill
                           installed — <plugin>:<skill> of a listed plugin, or a project
                           or user skill or command — (tools.not-installed) and, for
                           design and review, not user-invocable: false
                           (tools.not-invocable); an advise entry's load: files in its
                           skill's folder (tools.not-installed) and within caps.advise
                           together (tools.too-big, W6); a plugin skill is judged only
                           when claude is on PATH. See ape plugins --help
  module fences            a library (_apex/config.yaml library: true) with a go.mod at
                           the root has a go.mod fence in each APEX folder that exists —
                           .claude/, _apex/, the development folder, a root evidence/ —
                           committed (fence.missing: absent, or git does not track it),
                           with the module line apex.invalid/fence/<folder> and the root
                           go.mod's go line (fence.altered; its other lines are the
                           project's); both action replan: ` + "`ape project fence`" + ` writes them
                           (W7; see its --help). No library: true, or no root go.mod (a
                           module in a subfolder), is no finding

Notices (D155 Q73) are not findings: listed as "notice: <file>:<line>: …" lines on stderr
and under "notices" with --json ({severity: "notice", code, message, fix, action, file,
line}; [] when none, beside the findings on a failure too), they never change the exit.
  gate.outside-gates       an open (proposed or approved) milestone's gate runs a script
                           file outside development/gates/ (scripts/e2e.sh, say; a gate
                           that runs no script file, like make e2e, has none): apex-plan
                           writes gates into development/gates/<M>-<slug>/, and moving an
                           approved gate's script is gate drift, so it moves with the
                           milestone's next re-approval (before its approval, when
                           proposed). See ape milestone close --help for the gate folders
  hooks.pre-commit-absent  git's pre-commit hook (core.hooksPath honoured) does not run
                           ape's secret scan, so commits ape does not make (a session's
                           own git commit, a person's) go unscanned until the archive:
                           absent — a fresh clone; ` + "`ape project init`" + ` installs it —,
                           another tool's hook — add ` + "`ape hook pre-commit || exit 1`" + ` to
                           it; ape never edits it —, or not executable. ape doctor reports
                           it too (hooks.pre-commit). See ape hook pre-commit --help
  hooks.prompt-absent      .claude/settings.json does not register ` + "`ape hook user-prompt`" + ` for
                           UserPromptSubmit (or it is altered), so the person's prompts are
                           not recorded and a person act from a Claude Code session is
                           refused (E2); ` + "`ape project init`" + ` merges it back. ape doctor
                           reports it too (hooks.prompt). See ape hook pre-tool --help
  models.orchestrator-row  _apex/config.yaml has a models.apex-orchestrator row (Q96;
                           formerly models.operator-row, kept as its alias until v1.5):
                           with no models.apex-loop row it is read as apex-loop's (a
                           project made before rc.7: ` + "`ape project operator-interface`" + `
                           renames it); beside one it is a leftover apex-loop's row
                           overrides (drop it). The operator's apex-orchestrator runs
                           in your own session and has no models row
  decision.standard-stale  ADR-0001, APEX's standard ADR (D205, D206), is absent, or is
                           framework-owned and not the installed standard (an earlier
                           text, or the apex-v0 governance standard an earlier lift
                           converted): ` + "`ape decision standard`" + ` writes it (the
                           framework's migration v1.0.0_seq-08 runs it, and the entry
                           of a release that changes the standard)
  decision.standard-superseded
                           an accepted ADR supersedes ADR-0001: the project opted out
                           of the standard, and updates leave ADR-0001 alone
  decision.standard-differs
                           ADR-0001 is the project's own decision, not the standard:
                           ape never replaces it; a person decides
  roadmap.next-inline      the roadmap still holds Next — a "## Next" section, or items
                           under apex-plan's "Next — not milestones:" label — while it
                           lives in development/next.md (D217): ` + "`ape next move`" + ` moves
                           it (the framework's migration v1.0.0_seq-09 runs it)
  spec.check-names-removed (action replan) a lasting spec scenario's check names what
                           ` + "`ape decision standard`" + ` removed (D232; see its --help), so it
                           can never pass: a change retires it with a REMOVED delta
                           citing v1.0.0_seq-08 — never a new supersede of ADR-0001
  regression.slow          (action replan) regression: is one command and its last
                           recorded run took over checks.slow_regression (default 5m),
                           so every start and verify runs the whole suite: split it,
                           regression: {change: <fast>, milestone: <the suite>} (W2)
  checks.duplicate-run     (action replan) two or more scenario checks start with the same
                           command (to the first top-level && or |) and differ after it,
                           and one is full or each ran over a minute at the last gate: a
                           gate runs that command once per check (ape memoizes nothing);
                           names the scenarios and their time per gate (W2)
  check.full-suggested     (action replan) a scenario check that is not full ran over a
                           minute at the last gate: mark it ` + "`- check (full): <cmd>`" + ` if it
                           need not run on every change (W2; see ape change verify --help;
                           formerly check.live-suggested, kept as its alias until v1.5)
  gate.root-module-change  (action replan) a library's gate program (Go files under
                           development/gates/) imports a package its module does not
                           provide — not the standard library's, the module's own, or a
                           required module's: from the root module, running it changes
                           the root go.mod and go.sum, which never change for a gate
                           (D207); from the development fence, the fence lacks the
                           require. Its module is the fence with its own require lines
                           (` + "`require <root> v0.0.0`" + ` + ` + "`replace <root> => ../`" + ` for the root
                           module's packages) and go.sum, run as
                           ` + "`GOWORK=off go -C development run ./gates/<x>`" + ` (W7)
  docs.unaccepted          _apex/config.yaml's docs block was changed by a commit that is
                           not ape's and no person accepted, or differs uncommitted from
                           HEAD's, while the docs delegate is on (no configured delegate,
                           human_review.docs not person): docs is not guarded, but it
                           bounds that delegate, which refuses a docs change's human-review
                           task until a person accepts it (delegate.unaccepted-config):
                           ` + "`ape project accept-config --by <who>`" + `, or revert it (A18, W8)
  limits.context-relay-low limits.context_relay times the loop's context window
                           (models.apex-loop's model, else the smallest window of the
                           models the family words name) is under 100000 tokens: a fresh
                           apex-loop session starts at about 46000, so the loop would
                           relay at its first or second child launch and spend
                           limits.relays without work (D226, W8); a person raises it
  gate.data-outside        an open milestone's gate script reads a file by an input
                           redirection (` + "`while read …; done < scripts/checks/m1.checks`" + `)
                           that is none of the gate's own files (its gate folders,
                           development/gates/lib/, what it sources, a path under its
                           own non-root, non-development folder, what it declares:
                           ` + "`ape milestone approve --help`" + `), so the approval
                           does not record it: an edit to it is no gate drift and no
                           change.touches-gate (D313a). A gate's own check list moves
                           into development/gates/<M>-<slug>/, or the script declares it
                           ("# ape: gate-files <path>", v1.2) (approved: with its next
                           re-approval); a work product the gate checks stays (D319, W8)
The gate's per-scenario run times these read are in _apex/state/logs/gate-<M>.json.

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
			// Notices never change the exit (D155 Q73).
			notices := p.Notices()
			if notices == nil {
				notices = []apexv1.Diagnostic{}
			}
			if !f.json {
				printNotices(cmd.ErrOrStderr(), notices)
			}
			if len(fs) > 0 {
				return v1FailWith(cmd, &f, &apexv1.FailError{
					Code:     "project.check-failed",
					Msg:      fmt.Sprintf("%d finding(s) — fix them and run `ape project check` again", len(fs)),
					Findings: fs,
				}, nil, map[string]any{"notices": notices})
			}
			return v1Emit(cmd.OutOrStdout(), &f, map[string]any{"ok": true, "findings": []apexv1.Finding{}, "notices": notices}, func(w io.Writer) {
				fmt.Fprintln(w, "project check: green")
			})
		},
	}
	addV1Flags(cmd, &f)
	return cmd
}

// printNotices prints project check's notices, one `notice:` line each.
func printNotices(w io.Writer, notices []apexv1.Diagnostic) {
	for _, n := range notices {
		loc := n.File
		if n.Line > 0 {
			loc += ":" + strconv.Itoa(n.Line)
		}
		line := "notice: " + loc + ": " + n.Message
		if n.Fix != "" {
			line += " — " + n.Fix
		}
		// A renamed code names itself and its former name, which readers
		// still count (CodeAliases, D512).
		if n = n.WithAlias(); n.Alias != "" {
			line += " (code " + n.Code + apexv1.FormerlyNote(n.Code) + ")"
		}
		fmt.Fprintln(w, line)
	}
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
"fix" are for people: message is the sentence up to its first " — ", fix the rest
(the human line joins them). Codes are stable: a new rule gets a new
code, and a code's meaning never changes. A code whose name came to read wrong is renamed:
its old name stays an alias for a while, as a row of its own here ("renamed to <code>",
with renamed_to in --json; the new code's row lists aliases), and each diagnostic of the
code carries the old name in "alias" (the human line: "formerly <alias>"). severity is always "error" (no warning tier);
project check's notices, severity "notice", are no failure's diagnostics (exit 0 here).
Exit: 1 check failed · 2 usage · 3 conflict (nothing written).`,
		Example: "  ape project codes\n  ape project codes --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			codes := apexv1.PublishedCodes()
			return v1Emit(cmd.OutOrStdout(), &f, codes, func(w io.Writer) {
				for _, c := range codes {
					fmt.Fprintf(w, "%-30s %d  %-7s %s\n", c.Code, c.Exit, c.Action, c.Meaning)
				}
			})
		},
	}
	cmd.Flags().BoolVar(&f.json, "json", false, "machine output (JSON on stdout)")
	return cmd
}

func newProjectArchiveLayoutCmd() *cobra.Command {
	var f v1Flags
	var check bool
	cmd := &cobra.Command{
		Use:   "archive-layout [--check]",
		Short: "Move archived changes into development/archive/changes/ (the v1.0.0_seq-06 / v1.2.0_seq-01 migration)",
		Long: `Move every archived or abandoned change still in the older layout,
development/archive/<YYYY-MM-DD>-<id>/, into development/archive/changes/<YYYY-MM-DD>-<id>/,
where change archive and change abandon put them (D155). The framework's migration
v1.0.0_seq-06 runs it, and from framework v1.2 v1.2.0_seq-01, which supersedes it
(command: ape project archive-layout; check: ape project archive-layout --check, so a
fixture elsewhere in the repo never reads as half-applied). Inside ape framework update
the update's own install is uncommitted, so only the archive's own uncommitted changes
stand in the way. development/archive/milestones/ stays where it is.

One commit, "apex: archive layout (seq-06)": each folder moved with git mv, and in each
moved state.json the recorded paths that named the old folder (archived_to, and an
acceptance's evidence inside the folder) rewritten to the new one; nothing else in the
files changes. The refs refs/apex/archive/<id> and refs/apex/abandoned/<id>, and
state.json's tip_ref, name commits and are untouched. Idempotent: with nothing left to
move it writes nothing and says "nothing to move" (exit 0).

Every ape command reads archived changes from both layouts, always, so a project that has
not moved yet, or a branch cut before the move, works the same either way.

Refused on a change branch (branch.change-branch), with uncommitted changes under
development/archive/ (tree.dirty; changes elsewhere are left out of its commit),
outside a git repository (git.not-a-repo), and when a folder's new place already exists
(archive.folder-exists, exit 3: nothing is written; a person merges the two by hand). A
command any session may run (not person-only).

--check writes nothing: exit 0 when no archived change is left in the older layout, 1
(archive.legacy-layout, naming them) when some is — the migration's check: 0 applied,
1 not applied.

JSON: {"moved": [{"id", "from", "to"}], "rewritten": [<state.json>], "commit"}; "moved"
is always there, [] when nothing moved. Under --check it lists what is left to move, at
the top level beside the failure's ok/exit/diagnostics when some is (exit 1), and is []
when nothing is (exit 0).

Exit codes: 0 moved, or nothing to move (--check: nothing left) · 1 refused, or (--check)
some left · 3 a folder's new place exists.`,
		Example: "  ape project archive-layout --check\n  ape project archive-layout\n  ape project archive-layout --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			if check {
				res, err := p.ArchiveLayoutCheck()
				if err != nil {
					// R27: what is left to move at the top level, as when
					// nothing is, not inside the failure's result.
					return v1FailWith(cmd, &f, err, nil, map[string]any{"moved": res.Moved})
				}
				return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
					fmt.Fprintln(w, "archive layout: nothing left to move")
				})
			}
			res, err := p.ArchiveLayout(cmd.Context())
			if err != nil {
				return v1Fail(cmd, &f, err, res)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				if len(res.Moved) == 0 {
					fmt.Fprintln(w, "archive layout: nothing to move")
					return
				}
				for _, m := range res.Moved {
					fmt.Fprintf(w, "moved %s → %s\n", m.From, m.To)
				}
				for _, r := range res.Rewritten {
					fmt.Fprintf(w, "rewrote the old paths in %s\n", r)
				}
				fmt.Fprintf(w, "commit %.7s %s\n", res.Commit, apexv1.ArchiveLayoutSubject)
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().BoolVar(&check, "check", false, "write nothing: exit 0 when nothing is left in the older layout, 1 when some is")
	return cmd
}

func newProjectOperatorInterfaceCmd() *cobra.Command {
	var f v1Flags
	var check bool
	cmd := &cobra.Command{
		Use:   "operator-interface [--check]",
		Short: "Bring a project made before rc.7 to the operator interface (the v1.0.0_seq-07 migration)",
		Long: `Since rc.7 (D171) apex-orchestrator is the operator's interface — a skill a person
opens in their own Claude Code session by typing /apex-orchestrator — and the unattended
runner of one approved milestone or tweak is apex-loop. This brings a project that ape
project init or the lift made before rc.7 there; the framework's migration
v1.0.0_seq-07 runs it (command: ape project operator-interface; check: ape project
operator-interface --check). A project init or the lift made with rc.7b has nothing to do.

One commit, "apex: operator interface (seq-07)", of what is left of:
  models     _apex/config.yaml's models.apex-orchestrator row renamed to apex-loop, its
             value kept byte for byte (D176: sonnet/high unless the project chose
             otherwise); when a models.apex-loop row exists it wins and the old row's
             lines are removed. Every other byte of the file is kept. models: is not a
             guarded block, so this is no config.changed. A models: written as a flow
             mapping ({…}) whose old row must be removed is a person's to edit
             (project.operator-interface-pending, nothing written)
  hooks      ape's hooks merged into .claude/settings.json with ape project init's
             merge: a missing one added, an altered one restored, every other key and
             hook kept (hooks were only merged by init before)
  AGENTS.md  the line
               Talk to APEX: type ` + "`/apex-orchestrator`" + ` in Claude Code.
             after the project-context line (else after its first # heading, else at
             the top), when no line outside code names /apex-orchestrator (A11)
  .gitignore the .claude/ shape (D203, Q114): .claude/*, !.claude/settings.json,
             !.claude/skills/ and CLAUDE.local.md, as ape project init writes it — appended
             (the file created when absent), or a whole-folder .claude ignore rewritten in
             place; every other line kept. Files under .claude/ it un-ignores that are not
             settings.json are listed ("revealed"), not committed
Idempotent: with nothing left it writes nothing and says "nothing to do" (exit 0).

How ape knows seq-07 ran: from the config, not a marker (a fresh clone would lose one). A
models.apex-orchestrator row with no models.apex-loop row is the state before seq-07:
` + "`ape task apex-loop`" + ` reads it as apex-loop's, with a warning on stderr, and ape project
check lists the notice models.orchestrator-row (formerly models.operator-row). Beside an apex-loop row it is a leftover,
the notice says to drop it, and apex-loop's row is the one read.

Refused on a change branch (branch.change-branch), outside a git repository
(git.not-a-repo), and while _apex/config.yaml, .claude/settings.json or AGENTS.md (or
the .gitignore, when it has the shape to take) has uncommitted changes or is untracked
(tree.dirty): nothing is written. A command any session may run (not
person-only).

--check writes nothing: exit 0 when nothing is left, 1
(project.operator-interface-pending, action person, naming what is left) when something
is — the migration's check: 0 applied, 1 not applied.

JSON: {"models": "moved"|"removed" (absent when there is no old row), "hooks": [<Event>:
<command>, …], "agents_line": true|false, "gitignore": "added"|"rewritten" (absent when
the shape is there), "revealed": [files it un-ignored, not committed], "commit"}; "hooks"
is always there, [] when none. Under --check they say what is left, at the top level beside the failure's
ok/exit/diagnostics when something is (exit 1).

Exit codes: 0 applied, or nothing to do (--check: nothing left) · 1 refused, or (--check)
something left.`,
		Example: "  ape project operator-interface --check\n  ape project operator-interface\n  ape project operator-interface --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			if check {
				res, err := p.OperatorInterfaceCheck()
				if err != nil {
					return v1FailWith(cmd, &f, err, nil, operatorLeft(res))
				}
				return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
					fmt.Fprintln(w, "operator interface: nothing left to do")
				})
			}
			res, err := p.OperatorInterface(cmd.Context())
			if err != nil {
				return v1Fail(cmd, &f, err, res)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				if res.Commit == "" {
					fmt.Fprintln(w, "operator interface: nothing to do")
					return
				}
				switch res.Models {
				case "moved":
					fmt.Fprintln(w, "models.apex-orchestrator → models.apex-loop (value kept)")
				case "removed":
					fmt.Fprintln(w, "models.apex-orchestrator removed (models.apex-loop wins)")
				}
				for _, h := range res.Hooks {
					fmt.Fprintf(w, "hook added: %s\n", h)
				}
				if res.AgentsLine {
					fmt.Fprintf(w, "AGENTS.md: added %q\n", apexv1.OperatorLine)
				}
				for _, l := range apexv1.ClaudeIgnoreLines(&apexv1.ClaudeIgnoreResult{Action: res.GitIgnore, Revealed: res.Revealed}) {
					fmt.Fprintln(w, l)
				}
				fmt.Fprintf(w, "commit %.7s %s\n", res.Commit, apexv1.OperatorInterfaceSubject)
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().BoolVar(&check, "check", false, "write nothing: exit 0 when nothing is left to do, 1 when something is")
	return cmd
}

// operatorLeft is what --check found left of seq-07, at the top level of
// the failure's JSON.
func operatorLeft(res *apexv1.OperatorInterfaceResult) map[string]any {
	left := map[string]any{"hooks": res.Hooks, "agents_line": res.AgentsLine}
	if res.Models != "" {
		left["models"] = res.Models
	}
	if res.GitIgnore != "" {
		left["gitignore"] = res.GitIgnore
	}
	return left
}

func newStatusCmd() *cobra.Command {
	var f v1Flags
	var brief, board bool
	var until string
	var since int64
	var wait time.Duration
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
  - each milestone's newest gate run (D170): JSON milestones[].last_gate {sha (the commit
    the gate ran at), exit, at, log}, absent before its gate ever ran
  - the checked-out branch (JSON branch; "(on <branch>)" in the first line)
  - tweaks first (trial decision D40, D240): a change with "milestone: none" is a tweak
    (JSON changes[].lane "tweak"; else "milestone"). Approved tweaks (changes[].approved:
    {by, at, plan_digest}) are listed before milestone changes — started ones first, then
    by approval time — and JSON "tweaks" holds the ids of those not blocked, in the order they
    run: the first runs ahead of milestone work, at the loop's next change boundary
    (never in parallel), or alone with ` + "`ape task apex-loop --args <id>`" + `, and next
    points at it. A tweak awaiting approval is listed last, with the command a person runs
    (` + "`ape change approve <id> --by <who>`" + `). A tweak's runs (` + "`ape task <skill> --args <tweak-id>`" + `)
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
    left — the change is reviewed, or archived. The loop skips such a change. A
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
  - advise (W6, Q-E1): every change lists advise, one row per file of each tools.advise
    entry its advise: frontmatter names, as [{name, skill, for, file, path, bytes,
    sha256}] — path absolute (a plugin skill's in its install, else the project's or
    the user's .claude/skills/<skill>/), for the sessions that read it (change, apply,
    review), file the load: entry; a row that cannot be read has error instead (not
    declared, skill not installed, file missing) and no sha256. Always present ([]).
    Resolved when ape status runs: a plugin skill through the claude plugin CLI.
    The top-level advise (v1.4.1, D611) has the same rows for every tools.advise entry
    whose for: names plan: the planning session (apex-plan) reads them before it plans a
    milestone. No change names them, so it is all of them, within caps.advise_session
    (project check). Always present ([]); an entry without for: is never a plan one
  - acceptances (JSON changes[].accepted: [{task, by, at, evidence, run}]): by a person,
    or by "delegate:<skill>" with its run when _apex/config.yaml human_review delegates
    them (D75; "Accepted by a delegate" in the long output); and each human-review task's
    newest delegate verdict (JSON changes[].delegate: {"T2": {verdict, reason, run, at,
    voided, defects, returned}}): a fail or a voided run leaves the task awaiting a
    person, and the brief says why — unless the fail went back to the change (D188:
    returned, it named a defect in the change's own diff; JSON changes[].delegate_returns:
    {"T2": [{run, reason, defects, at}]}, as state.json records them): the change is
    in-progress again, the brief shows the reason and next names apex-change, and once
    it is reviewed again the task is the delegate's again. JSON changes[].delegable: [{task, reason}] (D152, omitted when none) are the
    awaiting tasks a delegate may take now — reason "configured" (human_review delegates)
    or "docs-default" (a docs change's docs check, change.md docs: true, its diff only
    the docs (release.docs, docs.changelog, docs.user_docs) and the development folder,
    human_review.docs not person); a task whose
    delegate failed (not returned) or was voided is a person's and not listed. next names the delegate
    for the first (` + "`ape change accept <id> <T> --delegate --wait 9m`" + `) before a person
  - spend so far: the sum of finished ape task run manifests (and pipeline runs an older ape recorded)
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
  - next: what the way of working says comes next, derived from the state alone; JSON
    "next_why" beside it (and "Why next:" in the long output, not --brief) is its reason,
    one line (Q100) — next stays one string
  - Next (JSON "roadmap_next": ["2026-10-16 <text> (from <id>)", …], [] when none; a
    count line in the human output): the findings noticed out of scope that apex-plan
    triages, as the base branch holds them (` + "`ape next add`" + ` writes there from any
    branch) — development/next.md's items, then any the roadmap still holds (a project
    not yet moved by ` + "`ape next move`" + `, D217), each once. Not the same field as
    "next", the step above
  - JSON "docs": {changelog, user_docs} from _apex/config.yaml docs: ({} when unset), for
    the skills that keep a changelog and user docs current
  - JSON "config": {commit_types, release_scheme, release_docs, commit_subject} — the
    project's commit types (commit.types, default feat, fix, perf, refactor, docs, test,
    build, ci, chore), the archive's subject template (commit.subject, default
    "{type}: {title}", D193 — the effective one, for skills to read instead of guessing),
    release scheme (release.scheme: semver, the default, date or edition) and the docs a
    release brings up to date (release.docs, default README.md and docs/ where they exist;
    D129), defaults filled in, for apex-change and apex-release (D120)
  - the latest ape task run (JSON "latest_run": {skill, run_id, status, stopped,
    resets, resets_at, message, depth, parent_run}): how it ended, e.g. "stopped:
    usage-limit, resets 2026-10-16T23:00:00+02:00" when a claude.ai usage limit
    stopped it (see ape task --help, exit 14), or "stopped: usage-guard (not started:
    the 7-day window), resets …" when ape's usage guard did not start it (W4); a
    session a loop run started names that run ("started by <run>"; depth 2, parent_run)
  - the plan's usage (W4, ape usage --help): "usage: 5h 0.14 · 7d 0.55 (read 2 min
    ago)", or "usage: unknown" before the first reading; JSON "usage": {at, five_hour,
    five_hour_resets_at, seven_day, seven_day_resets_at, run, claude_version,
    age_seconds}, null when there is none
  - JSON "sessions": the model sessions ape launched that run in this project now
    ([{run_id, kind, skill, depth, parent_run, pid, started_at}], [] when none): an
    apex-loop run at depth 1 and its one child at depth 2 (D62). The loop's
    run also carries relay (OI-11): {context_tokens, context_window, context_fill,
    context_relay, relays, relays_cap, relay_of} — its session's context as the newest
    turn of its transcript shows it, over the model's window (context_fill null and
    context_window 0 when the window is unknown: no relay then), the threshold
    (limits.context_relay), the relays so far and their cap (limits.relays), and the
    run it relays; the brief says "context 12 % (relay at 60 %), relays 0/5". A
    session whose launch ape's usage guard holds on the 5-hour window (W4) carries
    held_until (", held: 5-hour window, launches <ts>")

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

` + statusWaitHelp + `

Exit codes: 0 ok (with --until: a condition holds) · 75 --until: --wait ran out, run it again
with --since <seq> · 2 usage (usage.until, usage.wait). --board exits 0 whatever the board did.`,
		Example: "  ape status\n  ape status --brief\n  ape status --json\n  ape status --board\n" +
			"  ape status --wait 9m --until person,stopped --since 41 --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RefuseV0(cmd, &f, p); err != nil {
				return err
			}
			wo, err := statusWaitOpts(cmd, until, since, wait, brief, board)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			if wo != nil {
				return runStatusWait(cmd, &f, p, wo)
			}
			// W3: a detached run found lost is logged (run-lost, once).
			_ = detachedRuns(cmd.Context(), p.Root)
			r := p.Status()
			p.AttachAdvise(cmd.Context(), r)
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
		"the tab is a view, never a record, and quiet: a refresh raises no notification or unread dot")
	cmd.Flags().StringVar(&until, "until", "", "wait until one of these holds: person, stopped, done, blocked, event (comma = any); "+
		"returns at once when one already does")
	cmd.Flags().DurationVar(&wait, "wait", defaultLongWait, "with --until: how long this call waits (at most 9m30s); exit 75 when it runs out — run it again with --since <seq>")
	cmd.Flags().Int64Var(&since, "since", 0, "with --until: the seq the last wait printed; the events after it count, so none is missed between two calls")
	return cmd
}

// statusWithBoard is `ape status --board --json`: the report, plus what
// the board refresh did.
type statusWithBoard struct {
	*apexv1.StatusReport
	Board *statusboard.Outcome `json:"board"`
}

// fullSkippedLines is status's account of the full checks not run (W2
// Part 2): by the milestone's last gate, whose Gate: command was red, and
// by each change's last verify. "" when none were skipped.
func fullSkippedLines(r *apexv1.StatusReport) string {
	var lines []string
	if m := r.Milestone; m != nil && m.GateLast != nil {
		g := m.GateLast
		regression := g.Regression != nil && g.Regression.Skipped == apexv1.RegressionSkippedGateRed
		if g.FullSkipped > 0 || regression {
			what := fmt.Sprintf("%d full scenario check(s)", g.FullSkipped)
			if regression {
				what += " and the regression command"
			}
			lines = append(lines, fmt.Sprintf("  %s gate %.7s: %s, as its Gate: command was red (close runs everything)", m.ID, g.SHA, what))
		}
	}
	for i := range r.Changes {
		if c := &r.Changes[i]; c.FullSkipped > 0 {
			lines = append(lines, fmt.Sprintf("  %s verify %.7s: %d full scenario check(s), neither its own nor red at the last gate", c.ID, c.VerifySHA, c.FullSkipped))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\nFull checks not run:\n" + strings.Join(lines, "\n") + "\n"
}

func statusLong(r *apexv1.StatusReport) string {
	var b strings.Builder
	b.WriteString(r.Brief())
	if r.NextWhy != "" {
		b.WriteString("Why next: " + r.NextWhy + "\n")
	}
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
	b.WriteString(fullSkippedLines(r))
	if len(r.Advise) > 0 {
		b.WriteString("\nAdvise for planning (tools.advise for: plan; ape status --json: advise):\n")
		printAdvise(&b, r.Advise)
	}
	if n := len(r.OutsideFlow); n > 0 {
		fmt.Fprintf(&b, "\nOutside the change flow: %d commit(s) on the base since the last archive touch code with no Change: trailer (newest %.7s %s; ape status --json: outside_flow)\n",
			n, r.OutsideFlow[0].SHA, r.OutsideFlow[0].Subject)
	}
	if n := len(r.RoadmapNext); n > 0 {
		fmt.Fprintf(&b, "\nNext: %d finding(s) to triage at the next delivery (next.md; ape status --json: roadmap_next)\n", n)
	}
	if u := r.BaseUpstream; u != nil && u.Behind == 0 {
		// Behind is in the brief already.
		fmt.Fprintf(&b, "\nBase: %s\n", u.Describe())
	}
	if r.LatestRun != nil {
		fmt.Fprintf(&b, "\nLatest run: %s\n", r.LatestRun.Describe())
	}
	if r.Initialized {
		// W4: the plan's usage as the sessions ape launched last read it.
		fmt.Fprintf(&b, "\n%s\n", r.Usage.Describe())
	}
	for i, s := range r.Sessions {
		if i == 0 {
			b.WriteString("\nRunning sessions:\n")
		}
		fmt.Fprintf(&b, "  %s %s %s, depth %d", s.Kind, s.RunID, s.Skill, s.Depth)
		if s.ParentRun != "" {
			fmt.Fprintf(&b, ", started by %s", s.ParentRun)
		}
		if s.Relay != nil {
			b.WriteString(", " + s.Relay.Describe())
		}
		if s.HeldUntil != nil {
			// W4: the usage guard holds the launch.
			b.WriteString(", held: 5-hour window, launches " + s.HeldUntil.Format(time.RFC3339))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nSpend note: %s\n", r.Spend.Note)
	return b.String()
}
