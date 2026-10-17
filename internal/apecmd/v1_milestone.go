package apecmd

import (
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
done (<ts> <sha>). Every one of those writes is committed by ape (roadmap.md, plus
_apex/state/milestones.json when it exists) as "apex: <M> proposed|approved|done"
with trailer "Milestone: <M>". milestones.json holds each gate's result (green|red)
and is committed, as "apex: <M> gate green|red", only when that result changes; the
latest run of any result is in the gitignored _apex/state/logs/gate-<M>.json.
No milestone command leaves the tree dirty.

Approvals are records (H2): ` + "`ape milestone approve`" + ` records who approved, when, and the gate
approved — its command and the SHA-256 of every file it names — under the milestone's
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
	var by string
	cmd := &cobra.Command{
		Use:   "approve <M> --by <who>",
		Short: "Write Status: approved (by <who> <ts>)",
		Long: `Approve a proposed milestone: its Status: line becomes "approved (by <who> <ts>)", and
milestones.json records the approval (H2): {by, at, gate_cmd, gate_files, gate_files_sha256}
— the Gate: command, and the SHA-256 of each gate file: every word of the command (split at
whitespace and ; | & < > ( ), the value of a KEY=value or --flag=value word) that names a
regular file under the project root, e.g. ./scripts/e2e.sh (absolute paths, paths leaving
the root, options and words with $ or a glob are not looked up; what a script runs in
turn is not covered). Changes can start only under an approved milestone; gate and close
refuse a gate that differs from the approved one. Committed together as "apex: <M>
approved".

Approving an approved milestone records the approval again — "apex: <M> re-approved", the
Status line kept as it was (the milestone's spend counts from it) — when ape holds no
record of it or its gate changed since; otherwise it is a no-op. A done one is refused.
Refused on a change branch (see ` + "`ape milestone --help`" + `).

Only a person approves (D47 Q6): refused in a model session — APE_SESSION or CLAUDECODE
set, which includes a ` + "`!`" + ` line inside Claude Code — with exit 2, code
session.person-only. Run it from a plain terminal.

Exit codes: 0 ok · 1 no such milestone, done, malformed (Gate/Goal missing), or HEAD on a
change branch · 2 usage, or a model session.`,
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
			m, err := p.ApproveMilestone(cmd.Context(), args[0], by)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, m, func(w io.Writer) {
				fmt.Fprintf(w, "%s — %s: %s\n", m.ID, m.Title, m.StatusText)
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().StringVar(&by, "by", "", "who approves (required)")
	return cmd
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
		}
		printChecks(w, g.Run.SpecFailed, true)
		printRegression(w, g.Run.Regression)
		printStrays(w, g.Run.StrayFiles)
		fmt.Fprintf(w, "%s status: %s\n", g.Milestone, g.Status)
	}
}

func newMilestoneGateCmd() *cobra.Command {
	var f v1Flags
	cmd := &cobra.Command{
		Use:   "gate <M>",
		Short: "Run the milestone's gate and record the result",
		Long: `Run the milestone's Gate: command from the project root, and every scenario check of
the whole spec beside it as the regression suite (human-review not run; output under
_apex/state/logs/gate-<M>/), and the project's "regression: <cmd>" from _apex/config.yaml
when one is configured. The gate is green only when all pass — the regression command
only needs not to be newly red: it is judged against the earliest regression baseline,
for the same command, recorded by any change of the milestone (active or archived; a fix,
milestone: none, is no change of any milestone and never supplies it — D40);
red there and red now is reported (JSON run.regression.pre_existing_red), not failed.
When no change of the milestone has a baseline for the command, a red regression fails
the gate: nothing shows it was red before the milestone's work. Files the gate
or a check leaves outside development/ are undone and reported ("stray_files"). The run (exit, sha, time,
output hash, last lines) goes to _apex/state/logs/gate-<M>.json (gitignored; shown by
` + "`ape status`" + `). When the result differs from the recorded one (green vs red), or is
the first, _apex/state/milestones.json is updated and committed as
"apex: <M> gate green|red". Does not change the milestone's status — ` + "`ape milestone close`" + ` does.
Refused on a change branch: the gate judges the base branch (see ` + "`ape milestone --help`" + `).
On an approved milestone, refused when ape holds no approval record (milestone.status-unrecorded)
or the gate differs from the approved one (milestone.gate-changed): a person approves it again.

Exit codes: 0 gate green · 1 gate red (last output lines shown), no such milestone, no
approval record, a changed gate, or HEAD on a change branch · 2 usage.`,
		Example: "  ape milestone gate M1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			g, err := p.RunGate(cmd.Context(), args[0], f.runOptions())
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			if !g.Run.Green() {
				if !f.json {
					gateHuman(g)(cmd.OutOrStdout())
				}
				return v1Fail(cmd, &f, &apexv1.FailError{Code: "gate.red", Msg: fmt.Sprintf("%s gate is red (%s) — plan the next change toward it", args[0], g.Run.Why())}, g)
			}
			return v1Emit(cmd.OutOrStdout(), &f, g, gateHuman(g))
		},
	}
	addV1Flags(cmd, &f)
	addV1TimeoutFlag(cmd, &f)
	return cmd
}

func newMilestoneCloseCmd() *cobra.Command {
	var f v1Flags
	cmd := &cobra.Command{
		Use:   "close <M>",
		Short: "Run the gate; on exit 0 write Status: done (<ts> <sha>)",
		Long: `Close an approved milestone. Refused while a change of this milestone is in-progress,
verified or reviewed (archive or block it first) — each change's status read from its own
branch, change/<id>; a fix in flight (milestone: none, D40) counts toward no milestone and
does not hold the close — and on a change branch (see ` + "`ape milestone --help`" + `). Otherwise runs the gate (recorded as by
` + "`ape milestone gate`" + `) and, only on exit 0, writes "Status: done (<ts> <sha>)" into
roadmap.md, committed with milestones.json as "apex: <M> done". Green means the gate command
AND every scenario check of the spec pass, AND the regression command (when configured) is
not newly red against the milestone's earliest baseline for it — with no baseline, red fails
(see ` + "`ape milestone gate`" + `). A red gate is recorded as
by ` + "`ape milestone gate`" + `. A milestone is done when this command exits 0 — never because
anyone says so. Refused, as by ` + "`ape milestone gate`" + `, when ape holds no approval record or the
gate changed since the approval (H2). ape records the close in milestones.json (done_at).

Changes in flight refuse with milestone.changes-in-flight, action apply — or person when
one of them is reviewed with human-review tasks not accepted (awaiting_human; D49).

Exit codes: 0 done · 1 gate red, changes in flight, not approved, no approval record, a
changed gate, or HEAD on a change branch · 2 usage.`,
		Example: "  ape milestone close M1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			g, err := p.CloseMilestone(cmd.Context(), args[0], f.runOptions())
			if err != nil {
				if g != nil && !f.json && g.Run.Cmd != "" {
					gateHuman(g)(cmd.OutOrStdout())
				}
				return v1Fail(cmd, &f, err, g)
			}
			return v1Emit(cmd.OutOrStdout(), &f, g, func(w io.Writer) {
				if g.Run.Cmd != "" {
					gateHuman(g)(w)
				} else {
					fmt.Fprintf(w, "%s is already done\n", g.Milestone)
				}
			})
		},
	}
	addV1Flags(cmd, &f)
	addV1TimeoutFlag(cmd, &f)
	return cmd
}

func newDecisionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "decision",
		Short: "Record architecture decisions (ADR-NNNN) in development/decisions/",
		Long: `ADRs live in development/decisions/NNNN-<slug>.md. A decision is proposed, then
accepted (` + "`ape decision accept`" + `); an accepted decision's text is fixed — ` + "`ape project check`" + `
refuses an edit to it — and a different decision is a new ADR that supersedes it
(` + "`ape decision new <slug> --supersedes ADR-NNNN`" + `).`,
	}
	cmd.AddCommand(newDecisionNewCmd(), newDecisionAcceptCmd())
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

An ADR whose Status says accepted with no record in decisions.json — accepted by hand, or
before ape recorded acceptances — is reported by ` + "`ape project check`" + `
(decision.accepted-unrecorded) and held to the text git first committed it accepted with.
Accept records it as it is ("apex: ADR-NNNN accepted (recorded)"), unless its text changed
since that commit (decision.accepted-edited: restore it first). The lift migration's
records (by "lift") count as records.

A person's acceptance is recorded on the base branch (D11, D38): refused on a change's
branch, change/<id> (code branch.change-branch; ` + "`git switch <base>`" + ` first).

Exit codes: 0 accepted · 1 no such ADR, not proposed, malformed, or HEAD on a change
branch · 2 usage.`,
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
			res, err := p.AcceptDecision(cmd.Context(), args[0], by)
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
