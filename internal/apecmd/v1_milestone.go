package apecmd

import (
	"context"
	"fmt"
	"io"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
)

func newMilestoneCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "milestone",
		Short: "Add, approve, gate and close roadmap milestones",
		Long: `Milestones live in development/roadmap.md as "## M<n> — <title>" sections.
Their Status: line is written only by ape: proposed → approved (by <who> <ts>) →
done (<ts> <sha>), or done (accepted red by <who> <ts> <sha>) when a person closed it
with its gate red (` + "`ape milestone close --help`" + `). Every one of those writes is committed by ape (roadmap.md, plus
_apex/state/milestones.json when it exists) as "apex: <M> proposed|approved|done"
with trailer "Milestone: <M>". milestones.json holds each gate's result (green|red)
and is committed, as "apex: <M> gate green|red", only when that result changes; the
latest run of any result is in the gitignored _apex/state/logs/gate-<M>.json.
No milestone command leaves the tree dirty.

Approvals are records (H2): ` + "`ape milestone approve`" + ` records who approved, when, and the gate
approved — its command and the SHA-256 of each script it runs — under the milestone's
"approval" in milestones.json. change start, gate and close trust that record, never the
Status line alone: an approved milestone with no record (approved by hand, or before ape
recorded approvals) is refused with milestone.status-unrecorded, and gate and close refuse
one whose gate changed since with milestone.gate-changed — in both cases a person runs
` + "`ape milestone approve <M> --by <who>`" + ` (again). ` + "`ape project check`" + ` reports both.

Branches (trial decision D38): every milestone command records on the base branch — the
one changes are started from and archived into (D11). On a change's branch, change/<id>,
new, approve, gate and close are refused (exit 1, code branch.change-branch): run
` + "`git switch <base>`" + ` first. A change's status is read from its own branch, so close sees
every change in flight wherever it is worked.`,
	}
	cmd.AddCommand(newMilestoneNewCmd(), newMilestoneApproveCmd(), newMilestoneGateCmd(), newMilestoneCloseCmd())
	return cmd
}

func newMilestoneNewCmd() *cobra.Command {
	var f v1Flags
	var gate, goal string
	var budget float64
	cmd := &cobra.Command{
		Use:   `new "<title>" --gate "<cmd>" [--budget N]`,
		Short: "Append a milestone (Status: proposed) to roadmap.md",
		Long: `Append "## M<n> — <title>" to development/roadmap.md with Status: proposed, the gate
command, the budget (USD list-price equivalent, when given) and a Goal: line. The id is
the next free M<n>. Write the Goal paragraph and the Out of scope bullets afterwards
(or pass --goal); the Status: line is ape's. Committed as "apex: <M> proposed".
The gate's scripts must exist, and the gate must run red, before a person approves it
(D70 B: ` + "`ape milestone approve --help`" + `).

Refused on a change branch (see ` + "`ape milestone --help`" + `).

Exit codes: 0 ok (prints the id) · 1 no roadmap.md, or HEAD on a change branch · 2 usage.`,
		Example: `  ape milestone new "Walking skeleton" --gate "./scripts/e2e.sh m1" --budget 30`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			var b *float64
			if cmd.Flags().Changed("budget") {
				b = &budget
			}
			id, err := p.NewMilestone(cmd.Context(), args[0], gate, b, goal)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, map[string]any{"id": id, "title": args[0], "status": apexv1.MilestoneProposed}, func(w io.Writer) {
				fmt.Fprintf(w, "%s — %s: proposed (a person approves with `ape milestone approve %s --by <who>`)\n", id, args[0], id)
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().StringVar(&gate, "gate", "", "the one command that is red until the milestone is done (required)")
	cmd.Flags().Float64Var(&budget, "budget", 0, "budget in USD list-price equivalent")
	cmd.Flags().StringVar(&goal, "goal", "", "the Goal: paragraph (else a placeholder to fill in)")
	return cmd
}

func newMilestoneApproveCmd() *cobra.Command {
	var f v1Flags
	var by, acceptGreen string
	cmd := &cobra.Command{
		Use:   `approve <M> --by <who> [--accept-green "<reason>"]`,
		Short: "Write Status: approved (by <who> <ts>)",
		Long: `Approve a proposed milestone: its Status: line becomes "approved (by <who> <ts>)", and
milestones.json records the approval (H2): {by, at, gate_cmd, gate_files, gate_files_sha256,
gate_own_files, base_sha} — base_sha the milestone's start (D130): HEAD at its first approval, kept by a
re-approval, passed to every gate run as APE_MILESTONE_BASE (see ` + "`ape milestone gate --help`" + `)
— the Gate: command, exactly as written, and the SHA-256 of each gate file: a script the
command runs that is a regular file under the project root at approval. Of each simple
command (split at ; && || | & newlines and parentheses; leading NAME=value assignments,
env, exec, command, time, !, if/then/else/elif/while/until/do skipped) the gate file is
the executable word when it is a path (it has a slash: ./scripts/e2e.sh, scripts/x.sh),
or the script given to an interpreter — sh bash zsh dash python python3 node ruby perl —
after its options (bash scripts/gate.sh, python3 scripts/gate.py; python3 -m/-c, bash -c,
node -e and friends name no script). No other word is hashed: arguments and data files
(grep -q x CHANGELOG.md), commands inside $(…) or backquotes (test "$(python3 greet.py)"
= Hello hashes nothing), bare command names (looked up on PATH), absolute paths, paths
leaving the root and words with $ or a glob; what a script runs in turn is not covered.
gate_files_sha256 digests the gate files. gate_own_files (RK-6, rc.7o) records the gate's own
files beside its scripts, each with its SHA-256: every file under the milestone's gate
folders (development/gates/<M>-*/) and development/gates/lib/; every file a recorded script
sources (". <path>", "source <path>", literal or "$(dirname "$0")/<path>"); and every
literal relative path a recorded script names under its own folder, when that folder is
not the project root or the development folder (scripts/checks/m4.checks beside
scripts/m4.sh) — never the files a gate checks elsewhere (README.md, docs/), which are the
milestone's work. One level: a file found is not read in turn. The gate differs from the
approved one when its command changed, or a recorded gate file it still runs, or a recorded
own file, is gone or changed (compared file by file); a gate file or own file created since
the approval, and a file an older approval recorded that the gate does not run as a
script, are not drift (the next re-approval records it). A change's commits that edit a
recorded gate file are refused by verify and archive (change.touches-gate). Changes can start only under an
approved milestone; gate and close refuse a gate that differs from the approved one.
Committed together as "apex: <M> approved".

Before it records anything, approve runs the gate once against the current tree (D70 B),
under timeouts.gate (default 30m without progress, 4h at most), in a detached run (OI-12; below), with progress lines
in its log. A new
gate must run and be red — it is red until the milestone's work is done:
  exit 0                                  refused, milestone.gate-green (action replan):
                                          a green gate proves nothing the milestone adds
  a gate file missing (a script the gate  refused, milestone.gate-broken (action replan)
  runs, as above), a shell gate file that
  fails ` + "`bash -n`" + `, exit 126 or 127, a
  requires: entry missing, or a timeout
  any other exit                          red: approved, and the run recorded in the
                                          approval as self_check {at, sha, exit, secs, log,
                                          output_sha256}; its full output is
                                          _apex/state/logs/gate-<M>-approve.log
A refusal writes nothing and its message ends with the gate's last output lines. So a gate
whose script the milestone's work would write cannot be approved: the script exists at
approval and fails until the work is done (test -x ./bin/app || exit 1, not a bare
./bin/app, which exits 127). A re-approval runs the same check.

--accept-green "<reason>" (RK-5, Q145): a re-approval of an approved milestone whose work is
done and whose gate a person or apex-plan fixed after it — the fixed gate is green, which
the check above refuses, and gate and close refuse the changed gate until it is approved
again. With the flag a person states that case: the green run is recorded as self_check
(exit 0) beside accept_green {reason, by, at}, committed as "apex: <M> re-approved (gate
green: <reason>)"; then ` + "`ape milestone close <M>`" + ` closes it. A gate fixed before the work is done
and already green proves nothing: re-plan it instead. On a proposed milestone (a first
approval) the flag is a usage error (exit 2); on a re-approval whose gate is red it is not
needed, and the approval is recorded as without it. A person's act, as approve is.

Approving an approved milestone records the approval again — "apex: <M> re-approved", the
Status line kept as it was (the milestone's spend counts from it) — when ape holds no
record of it, its gate changed since, it runs a gate file the record does not hold (created
since: approving freezes it), or changes of it were blocked in a row (which resets
that count, limits.consecutive_blocks: ` + "`ape change new --help`" + `); otherwise it is a no-op. A done one is refused.
Refused on a change branch (see ` + "`ape milestone --help`" + `).

Only a person approves (D47 Q6): refused in a model session — APE_SESSION or CLAUDECODE
set, which includes a ` + "`!`" + ` line inside Claude Code — with exit 2, code
session.person-only, unless, in the person's own Claude Code session, the person typed
"approve <M>" in this turn (E2: ` + "`ape hook pre-tool --help`" + `): --by is then optional and must be
git config user.name (else session.by-mismatch, exit 1), and the record's by is
"<user.name> (prompt <ts>: \"<the person's words>\")". The run lock records the act
(person_act): running the same command again while it runs (exit 75), or before its result
is printed, attaches with no new grant; another run on the milestone is run.busy.

JSON: the milestone {id, title, line, status, status_text, gate, budget, goal,
out_of_scope, approved_at}, and self_check — the gate's run at the approval on record, as
_apex/state/milestones.json holds it ({at, sha, exit, secs, log, output_sha256}; D224), so
"exit": 1 shows the gate was recorded red; on a no-op it is the earlier approval's.

Exit codes: 0 ok · 1 no such milestone, done, malformed (Gate/Goal missing), the gate green
or broken, HEAD on a change branch, or session.by-mismatch · 2 usage, or a model session
with no grant.`,
		Example: "  ape milestone approve M1 --by eval",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			// OI-12: the self-check gate can outlive one Bash call, so
			// approve is a long run (its lock M<n>), and a person act (E2).
			id := args[0]
			f.person = personCallOf(cmd, args)
			return runLong(cmd, &f, p, id, func(ctx context.Context, opt apexv1.RunOptions) renderFn {
				approve := p.ApproveMilestone
				if cmd.Flags().Changed("accept-green") {
					approve = func(ctx context.Context, id, by string, progress io.Writer) (*apexv1.Milestone, error) {
						return p.ApproveMilestoneAcceptGreen(ctx, id, by, acceptGreen, progress)
					}
				}
				m, err := approve(ctx, id, personBy(ctx, by), opt.Progress)
				out := approveOut{Milestone: m}
				if err == nil {
					out.SelfCheck = p.ApprovalSelfCheck(id)
				}
				return func(cmd *cobra.Command, f *v1Flags) error {
					if err != nil {
						return v1Fail(cmd, f, err, nil)
					}
					return v1Emit(cmd.OutOrStdout(), f, out, func(w io.Writer) {
						fmt.Fprintf(w, "%s — %s: %s\n", m.ID, m.Title, m.StatusText)
					})
				}
			})
		},
	}
	addV1Flags(cmd, &f)
	addV1WaitFlags(cmd, &f)
	cmd.Flags().StringVar(&by, "by", "", "who approves (required from a terminal; in a Claude Code session, git config user.name)")
	cmd.Flags().StringVar(&acceptGreen, "accept-green", "", "re-approval only: the gate is green because the milestone's work is done and the gate was fixed after it — why (Q145)")
	return cmd
}

// approveOut is milestone approve's JSON: the milestone, and the gate's
// run at the approval on record (D224): the operator can tell the gate was
// recorded red without reading milestones.json.
type approveOut struct {
	*apexv1.Milestone
	SelfCheck *apexv1.GateSelfCheck `json:"self_check,omitempty"`
}

func gateHuman(g *apexv1.GateResult) func(io.Writer) {
	return func(w io.Writer) {
		verdict := "GREEN"
		if !g.Run.Green() {
			verdict = "RED (" + g.Run.Why() + ")"
		}
		fmt.Fprintf(w, "%s gate %s in %.1fs at %.7s: `%s` + %d spec scenario check(s)\n",
			g.Milestone, verdict, g.Run.Secs, g.Run.SHA, g.Run.Cmd, g.Run.SpecChecks)
		if g.Run.Exit != 0 {
			for _, l := range g.Run.Tail {
				fmt.Fprintln(w, "  | "+l)
			}
			fmt.Fprintln(w, "  full output: "+g.Run.Log)
			if g.Run.ShellNote != "" {
				fmt.Fprintln(w, "  note: "+g.Run.ShellNote)
			}
		}
		printChecks(w, g.Run.SpecFailed, true)
		if n := g.Run.LiveSkipped; n > 0 {
			fmt.Fprintf(w, "  %d live scenario check(s) skipped: the Gate: command is red (close runs them)\n", n)
		}
		printRegression(w, g.Run.Regression)
		printStrays(w, g.Run.StrayFiles, g.Run.StraysKept)
		fmt.Fprintf(w, "%s status: %s\n", g.Milestone, g.Status)
	}
}

func newMilestoneGateCmd() *cobra.Command {
	var f v1Flags
	var specOnly bool
	cmd := &cobra.Command{
		Use:   "gate <M> [--spec-only]",
		Short: "Run the milestone's gate and record the result",
		Long: `Run the milestone's Gate: command from the project root — with APE_MILESTONE=<M> and
APE_MILESTONE_BASE=<the milestone's start: HEAD at its first approval, ape status --json
milestone.base_sha> in its environment (D130), so a gate can test what changed since, e.g.
` + "`git diff --quiet \"$APE_MILESTONE_BASE\" HEAD -- docs/ && exit 1`" + `; the same two at approve's
self-check and at close; empty base for an approval before rc.3 — and every scenario check of
the whole spec beside it as the regression set (human-review not run; output under
_apex/state/logs/gate-<M>/), and the project's regression command from _apex/config.yaml
when one is configured — its milestone tier, "regression: {change: <cmd>, milestone:
<cmd>}", else the one command (H10). The gate is green only when all pass — the regression command
only needs not to be newly red: it is judged against the earliest regression baseline,
for the same command, recorded by any change of the milestone (active or archived; a tweak,
milestone: none, is no change of any milestone and never supplies it — D40);
red there and red now is reported (JSON run.regression.pre_existing_red), not failed —
unless, with failing tests named by both runs (as for ` + "`ape change verify`" + `), a test fails
now that did not at that baseline: red, the names in run.regression.new_failures.
For a milestone tier that differs from the change tier, the baseline is the one the
milestone's first change to start recorded for that tier (milestone_regression_baseline,
W2). When no change of the milestone has a baseline for the command, a red regression fails
the gate: nothing shows it was red before the milestone's work. The
Gate: command runs first; when it is red the gate is red already, and it skips the live
scenario checks (` + "`- check (live):`" + `, W2) and the regression command (JSON run.live_skipped: N,
run.regression.skipped: "gate-red"); ` + "`ape milestone close`" + ` runs everything. Each run's
per-scenario times go to logs/gate-<M>.json (scenario_secs), for project check. Timeouts:
the Gate: command runs under timeouts.gate (default idle 30m, max 4h), each scenario check
under timeouts.check (idle 15m, max 2h), the regression command under timeouts.regression
(idle 30m, max 2h) — killed at idle without progress (output or CPU time) or at max: exit
124, reason idle|ceiling; --timeout sets max for all three. A vacuous or unrun gate command, scenario or regression command is
red (see ` + "`ape change verify --help`" + `; JSON reason/detail). Files the gate
or a check leaves outside development/ are undone and reported ("stray_files"), a copy of
each kept under _apex/state/strays/ ("strays_kept"; see ` + "`ape change verify --help`" + `). A red scenario
whose check names what ` + "`ape decision standard`" + ` removed says so on its line and in
run.spec_failed[].stale_names (D232; see ` + "`ape decision standard --help`" + `). The run (exit, sha, time,
output hash, last lines) goes to _apex/state/logs/gate-<M>.json (gitignored; shown by
` + "`ape status`" + `). When the result differs from the recorded one (green vs red), or is
the first, _apex/state/milestones.json is updated and committed as
"apex: <M> gate green|red". Does not change the milestone's status — ` + "`ape milestone close`" + ` does.
Refused on a change branch: the gate judges the base branch (see ` + "`ape milestone --help`" + `).
On an approved milestone, refused when ape holds no approval record (milestone.status-unrecorded)
or the gate differs from the approved one (milestone.gate-changed): a person approves it again.
On a done milestone, refused (milestone.done) before anything runs: its gate was recorded at
the close, which archived its gate folder; nothing is run, written or committed.

--spec-only (W2): runs the spec's scenario checks alone, as the gate runs them — no Gate:
command, no regression command, no live check (JSON live_skipped) — and records nothing: no milestones.json, no gate-<M>.json,
no commit. It runs in this process (no detached worker, so never exit 75) under the
milestone's run lock (run.busy beside a gate or close of it), with the same refusals as
the gate (a done milestone's milestone.done included), and is allowed on a proposed milestone: apex-plan runs it before the approval to
see which scenarios of the plan are red. Output under _apex/state/logs/gate-<M>-spec/, the
result beside it in gate-<M>-spec.json (never the gate's gate-<M>.json). JSON {milestone,
spec_only: true, mode: "spec-only", spec_checks, spec_failed: [...], log, at, sha}; a red
scenario is the diagnostic gate.spec-red (exit 1, action replan), the result under "result".

Exit codes: 0 gate green (--spec-only: every scenario passed) · 1 gate red (last output
lines shown), a red scenario under --spec-only, no such milestone, a done milestone, no
approval record, a changed gate, or HEAD on a change branch · 2 usage · 3 run.busy (--spec-only).`,
		Example: "  ape milestone gate M1\n  ape milestone gate M2 --spec-only --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			id := args[0]
			if specOnly {
				return runSpecOnly(cmd, &f, p, id)
			}
			return runLong(cmd, &f, p, id, func(ctx context.Context, opt apexv1.RunOptions) renderFn {
				g, err := p.RunGate(ctx, id, opt)
				return func(cmd *cobra.Command, f *v1Flags) error {
					if err != nil {
						return v1Fail(cmd, f, err, nil)
					}
					if !g.Run.Green() {
						if !f.json {
							gateHuman(g)(cmd.OutOrStdout())
						}
						return v1Fail(cmd, f, &apexv1.FailError{Code: "gate.red", Msg: fmt.Sprintf("%s gate is red (%s) — plan the next change toward it", id, g.Run.Why())}, g)
					}
					return v1Emit(cmd.OutOrStdout(), f, g, gateHuman(g))
				}
			})
		},
	}
	addV1Flags(cmd, &f)
	addV1TimeoutFlag(cmd, &f)
	addV1WaitFlags(cmd, &f)
	cmd.Flags().BoolVar(&specOnly, "spec-only", false, "run only the spec's scenario checks (no Gate: command, no regression) and record nothing; allowed on a proposed milestone")
	return cmd
}

// runSpecOnly is `ape milestone gate <M> --spec-only` (W2 Part 4): in this
// process, under the milestone's run lock.
func runSpecOnly(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, id string) error {
	ctx := cmd.Context()
	lock, held, err := p.TakeRunLock(ctx, "milestone gate --spec-only", id)
	if err != nil {
		return v1Fail(cmd, f, err, nil)
	}
	if held != nil {
		return v1Fail(cmd, f, apexv1.RunBusy(held), nil)
	}
	defer p.ReleaseRunLock(lock)
	r, err := p.RunSpecOnly(ctx, id, f.runOptions())
	if err != nil {
		return v1Fail(cmd, f, err, nil)
	}
	human := func(w io.Writer) {
		fmt.Fprintf(w, "%s spec-only: %d scenario check(s), %d red — nothing recorded (log %s/)\n", id, r.SpecChecks, len(r.SpecFailed), r.Log)
		if r.LiveSkipped > 0 {
			fmt.Fprintf(w, "  %d live scenario check(s) not run: a spec-only run runs no live check\n", r.LiveSkipped)
		}
		for i := range r.SpecFailed {
			fmt.Fprintf(w, "  red  %s: %s\n", r.SpecFailed[i].Task, r.SpecFailed[i].Cmd)
		}
	}
	if err := r.Err(); err != nil {
		if !f.json {
			human(cmd.OutOrStdout())
		}
		return v1Fail(cmd, f, err, r)
	}
	return v1Emit(cmd.OutOrStdout(), f, r, human)
}

func newMilestoneCloseCmd() *cobra.Command {
	var f v1Flags
	var acceptRed, by string
	cmd := &cobra.Command{
		Use:   `close <M> [--accept-red "<reason>" --by <who>]`,
		Short: "Run the gate; on exit 0 write Status: done (<ts> <sha>)",
		Long: `Close an approved milestone. Refused while a change of this milestone is in-progress,
verified or reviewed (archive or block it first) — each change's status read from its own
branch, change/<id>; a tweak in flight (milestone: none, D40) counts toward no milestone and
does not hold the close — and on a change branch (see ` + "`ape milestone --help`" + `). Otherwise runs the gate (recorded as by
` + "`ape milestone gate`" + `) and, only on exit 0, writes "Status: done (<ts> <sha>)" into
roadmap.md, committed with milestones.json as "apex: <M> done". Green means the gate command
AND every scenario check of the spec pass, AND the regression command (when configured) is
not newly red against the milestone's earliest baseline for it — with no baseline, red fails
(see ` + "`ape milestone gate`" + `). A red gate is recorded as
by ` + "`ape milestone gate`" + `. A milestone is done when this command exits 0 — never because
anyone says so. Refused, as by ` + "`ape milestone gate`" + `, when ape holds no approval record or the
gate changed since the approval (H2). ape records the close in milestones.json (done_at).

Gate folders (D155 Q74): each folder development/gates/<M>-*/ (by name; development/gates/lib/
is no milestone's and stays) moves, in the close commit (a rename: every file's content and
sha256 unchanged), to development/archive/milestones/<M>/gate/ — one folder's contents into
gate/, several each into gate/<folder>/ — beside the close evidence; --accept-red too. A
gate outside development/gates/ (scripts/…) stays where it is. milestones.json records each
move as gate_archived: [{from, to}] (JSON "gate_archived"); the approval's gate_files keep
the paths as approved, so a verifier maps a path under "from" to "to" and compares the same
sha256. A gate/ already there refuses the close before the gate runs, with nothing written
(milestone.gate-archive-exists, action person: move it away).

With "evidence: archive" in _apex/config.yaml (H23; default off), a green close writes the
gate run's evidence to development/archive/milestones/<M>/evidence/ — one file per check
(the gate command, every spec scenario, the regression command): command, exit, duration,
output sha256 and the output, capped at caps.evidence_log, default 1 MiB (head and tail
kept, the full log named by path and sha256) — plus accept-red.json when milestones.json records an accept_red for
<M>; committed with the close record (JSON "evidence").

Changes in flight refuse with milestone.changes-in-flight, action apply — or person when
one of them is reviewed with human-review tasks not accepted (awaiting_human; D49).

--accept-red "<reason>" --by <who> (H22, D45): a person closes the milestone although its
gate is red — a release shipped with known defects, say. Everything else is as without it
(approval record, gate as approved, no change in flight); when the gate is red, the
milestone is done anyway: milestones.json records accept_red: {reason, by, at, gate: the red
run}, the Status line reads "done (accepted red by <who> <ts> <sha>)", committed as
"apex: <M> done (accepted red)", and ` + "`ape status`" + ` shows it (JSON milestones[].accept_red;
the brief names the last such milestone). A green gate closes as usual and accepts nothing.
Only a person passes it: refused in a model session (APE_SESSION or CLAUDECODE set, a ` + "`!`" + `
line inside Claude Code included) with exit 2, session.person-only — unless, in the person's
own Claude Code session, the person typed "accept red on <M>" in this turn (E2: ` + "`ape hook pre-tool --help`" + `;
--by is then git config user.name, recorded with the prompt). Such a close attaches only to
a run of the same command line: a plain close running on the milestone is run.busy (exit 3),
never an attach that would drop the flag. A person tags after close.

Exit codes: 0 done · 1 gate red (without --accept-red), changes in flight, not approved,
no approval record, a changed gate, an archived gate/ in the way, or HEAD on a change
branch · 2 usage, or --accept-red from a model session.`,
		Example: "  ape milestone close M1\n  ape milestone close M3 --accept-red \"ships with the known RF drift; fixed in M4\" --by diego",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			if by != "" && !cmd.Flags().Changed("accept-red") {
				return v1Fail(cmd, &f, &apexv1.UsageError{Code: "usage.by", Msg: "--by goes with --accept-red — drop it, or give the reason the red gate is accepted"}, nil)
			}
			id, accepting := args[0], cmd.Flags().Changed("accept-red")
			f.person = personCallOf(cmd, args)
			return runLong(cmd, &f, p, id, func(ctx context.Context, opt apexv1.RunOptions) renderFn {
				var g *apexv1.GateResult
				var err error
				if accepting {
					g, err = p.CloseMilestoneAcceptRed(ctx, id, acceptRed, personBy(ctx, by), opt)
				} else {
					g, err = p.CloseMilestone(ctx, id, opt)
				}
				return func(cmd *cobra.Command, f *v1Flags) error {
					if err != nil {
						if g != nil && !f.json && g.Run.Cmd != "" {
							gateHuman(g)(cmd.OutOrStdout())
						}
						return v1Fail(cmd, f, err, g)
					}
					return v1Emit(cmd.OutOrStdout(), f, g, func(w io.Writer) {
						if g.Run.Cmd != "" {
							gateHuman(g)(w)
						} else {
							fmt.Fprintf(w, "%s is already done\n", g.Milestone)
						}
						if a := g.AcceptRed; a != nil {
							fmt.Fprintf(w, "recorded: closed with its gate red, accepted by %s — %s\n", a.By, a.Reason)
						}
						for _, a := range g.GateArchived {
							fmt.Fprintf(w, "gate folder archived: %s/ -> %s/\n", a.From, a.To)
						}
					})
				}
			})
		},
	}
	addV1Flags(cmd, &f)
	addV1TimeoutFlag(cmd, &f)
	addV1WaitFlags(cmd, &f)
	cmd.Flags().StringVar(&acceptRed, "accept-red", "", "close although the gate is red, for this reason (a person only; with --by)")
	cmd.Flags().StringVar(&by, "by", "", "who accepts the red gate (with --accept-red)")
	return cmd
}

func newDecisionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "decision",
		Short: "Record architecture decisions (ADR-NNNN) in development/decisions/",
		Long: `ADRs live in development/decisions/NNNN-<slug>.md. A decision is proposed, then
accepted (` + "`ape decision accept`" + `); an accepted decision's text is fixed — ` + "`ape project check`" + `
refuses an edit to it — and a different decision is a new ADR that supersedes it
(` + "`ape decision new <slug> --supersedes ADR-NNNN`" + `).

ADR-0001 is APEX's standard ADR, "Record architecture decisions" (D205): ape project
init and the lift write it, ` + "`ape decision standard`" + ` keeps it at the installed standard,
and a project's own decisions number from 0002.`,
	}
	cmd.AddCommand(newDecisionNewCmd(), newDecisionAcceptCmd())
	// rc.7b (rc7b-adr).
	cmd.AddCommand(newDecisionStandardCmd())
	return cmd
}

func newDecisionNewCmd() *cobra.Command {
	var f v1Flags
	var title, supersedes string
	newCmd := &cobra.Command{
		Use:   "new <slug> [--supersedes ADR-NNNN]",
		Short: "Allocate the next ADR number and write the skeleton",
		Long: `Write development/decisions/NNNN-<slug>.md with the next free number:

  # ADR-NNNN: <title>
  Status: proposed
  Date: YYYY-MM-DD
  Supersedes: ADR-MMMM        (only with --supersedes)
  ## Context / ## Decision / ## Consequences
  Enforced by: review

--supersedes ADR-MMMM (proposed or accepted) also sets that ADR's Status to
"superseded by ADR-NNNN". Committed as "apex: ADR-NNNN proposed[, supersedes ADR-MMMM]".
Fill it in (your own commit); set Enforced by: to a command, hook or review. Reference it
from a change's frontmatter (decisions: [ADR-NNNN]) or a requirement (Decisions: ADR-NNNN).

Runs on any branch: an ADR written while a change is worked, on its branch change/<id>,
lands on the base branch with the change's squash commit at archive (D38) — and can be
accepted only once it is there.

Exit codes: 0 ok (prints id and path) · 1 slug already used, or the superseded ADR is
missing or already superseded · 2 usage.`,
		Example: "  ape decision new use-sqlite\n  ape decision new use-postgres --supersedes ADR-0002 --title \"Store tenants in Postgres\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			res, err := p.NewDecision(cmd.Context(), args[0], title, supersedes)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				fmt.Fprintf(w, "%s  %s", res.ID, res.Path)
				if res.Supersedes != "" {
					fmt.Fprintf(w, "  (supersedes %s)", res.Supersedes)
				}
				fmt.Fprintln(w)
			})
		},
	}
	addV1Flags(newCmd, &f)
	newCmd.Flags().StringVar(&title, "title", "", "ADR title (default: from the slug)")
	newCmd.Flags().StringVar(&supersedes, "supersedes", "", "ADR-NNNN this decision replaces")
	return newCmd
}

func newDecisionAcceptCmd() *cobra.Command {
	var f v1Flags
	var by string
	cmd := &cobra.Command{
		Use:   "accept <ADR-NNNN> --by <who>",
		Short: "Accept a proposed ADR and fix its text",
		Long: `Set a proposed ADR's Status to accepted and record the text it was accepted with in
_apex/state/decisions.json; both are committed as "apex: ADR-NNNN accepted". From
then on ` + "`ape project check`" + ` fails if the ADR's text (everything but its Status line)
changes: supersede it instead.

An ADR that still holds a placeholder comment of the skeleton ` + "`ape decision new`" + ` wrote
(<!-- the forces at play --> and the other two) records no decision yet, and is refused
(decision.placeholder): write its sections first. One accepted with them before this
refusal is a ` + "`project check`" + ` notice (decision.placeholder-accepted).

An ADR whose Status says accepted with no record in decisions.json — accepted by hand, or
before ape recorded acceptances — is reported by ` + "`ape project check`" + `
(decision.accepted-unrecorded) and held to the text git first committed it accepted with.
Accept records it as it is ("apex: ADR-NNNN accepted (recorded)"), unless its text changed
since that commit (decision.accepted-edited: restore it first). The lift migration's
records (by "lift") count as records.

Only a person accepts (OI-4): refused in a model session (APE_SESSION or CLAUDECODE set,
a ` + "`!`" + ` line inside Claude Code included) with exit 2, session.person-only — unless, in the
person's own Claude Code session, the person typed "accept <ADR-NNNN>" in that turn (E2:
` + "`ape hook pre-tool --help`" + `; --by is then git config user.name, recorded with the prompt;
another --by is session.by-mismatch, exit 1).

A person's acceptance is recorded on the base branch (D11, D38): refused on a change's
branch, change/<id> (code branch.change-branch; ` + "`git switch <base>`" + ` first).

Exit codes: 0 accepted · 1 no such ADR, not proposed, malformed, still the skeleton,
HEAD on a change branch, or session.by-mismatch · 2 usage, or a model session with no grant.`,
		Example: "  ape decision accept ADR-0003 --by diego",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			if err := v1PersonAct(cmd, p, personCallOf(cmd, args)); err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			res, err := p.AcceptDecision(cmd.Context(), args[0], personBy(cmd.Context(), by))
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				fmt.Fprintf(w, "%s accepted  %s\n", res.ID, res.Path)
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().StringVar(&by, "by", "", "who accepts (required)")
	return cmd
}
