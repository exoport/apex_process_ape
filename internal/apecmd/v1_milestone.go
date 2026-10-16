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
development/.apex/milestones.json when it exists) as "apex: <M> proposed|approved|done"
with trailer "Milestone: <M>". milestones.json holds each gate's result (green|red)
and is committed, as "apex: <M> gate green|red", only when that result changes; the
latest run of any result is in the gitignored development/.apex/logs/gate-<M>.json.
No milestone command leaves the tree dirty.`,
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

Exit codes: 0 ok (prints the id) · 1 no roadmap.md · 2 usage.`,
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
		Long: `Approve a proposed milestone: its Status: line becomes "approved (by <who> <ts>)".
Changes can start only under an approved milestone. Committed as "apex: <M> approved".
Approving an approved milestone is a no-op; a done one is refused.

Exit codes: 0 ok · 1 no such milestone, done, or malformed (Gate/Goal missing) · 2 usage.`,
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
development/.apex/logs/gate-<M>/). The gate is green only when both pass. The run (exit, sha, time,
output hash, last lines) goes to development/.apex/logs/gate-<M>.json (gitignored; shown by
` + "`ape status`" + `). When the result differs from the recorded one (green vs red), or is
the first, development/.apex/milestones.json is updated and committed as
"apex: <M> gate green|red". Does not change the milestone's status — ` + "`ape milestone close`" + ` does.

Exit codes: 0 gate green · 1 gate red (last output lines shown) or no such milestone · 2 usage.`,
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
				return v1Fail(cmd, &f, &apexv1.FailError{Msg: fmt.Sprintf("%s gate is red (%s) — plan the next change toward it", args[0], g.Run.Why())}, g)
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
verified or reviewed (archive or block it first). Otherwise runs the gate (recorded as by
` + "`ape milestone gate`" + `) and, only on exit 0, writes "Status: done (<ts> <sha>)" into
roadmap.md, committed with milestones.json as "apex: <M> done". Green means the gate command
AND every scenario check of the spec pass. A red gate is recorded as
by ` + "`ape milestone gate`" + `. A milestone is done when this command exits 0 — never because
anyone says so.

Exit codes: 0 done · 1 gate red, changes in flight, or not approved · 2 usage.`,
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
	}
	var f v1Flags
	var title string
	newCmd := &cobra.Command{
		Use:   "new <slug>",
		Short: "Allocate the next ADR number and write the skeleton",
		Long: `Write development/decisions/NNNN-<slug>.md with the next free number:

  # ADR-NNNN: <title>
  Status: proposed
  Date: YYYY-MM-DD
  ## Context / ## Decision / ## Consequences
  Enforced by: review

Committed as "apex: ADR-NNNN proposed". Fill it in (your own commit); set Enforced by: to
a command, hook or review. Reference it from a change's
frontmatter (decisions: [ADR-NNNN]) or a requirement (Decisions: ADR-NNNN).

Exit codes: 0 ok (prints id and path) · 1 slug already used · 2 usage.`,
		Example: "  ape decision new use-sqlite\n  ape decision new use-sqlite --title \"Store tenants in SQLite\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			res, err := p.NewDecision(cmd.Context(), args[0], title)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				fmt.Fprintf(w, "%s  %s\n", res.ID, res.Path)
			})
		},
	}
	addV1Flags(newCmd, &f)
	newCmd.Flags().StringVar(&title, "title", "", "ADR title (default: from the slug)")
	cmd.AddCommand(newCmd)
	return cmd
}
