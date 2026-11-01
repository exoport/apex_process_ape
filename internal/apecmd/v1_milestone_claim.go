package apecmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/member"
)

// errNotInEnsemble refuses a lease verb in a project with no
// coordination: block: an uncoordinated project has no milestone leases.
var errNotInEnsemble = errors.New("this project is in no ensemble (no coordination: block in _apex/config.yaml): its milestones have no leases, and its one loop runs any of them")

func newMilestoneClaimCmd() *cobra.Command {
	var f v1Flags
	cmd := &cobra.Command{
		Use:   "claim <M>",
		Short: "Take a milestone's lease for this checkout (a project in an ensemble)",
		Long: `In a project in an ensemble (a coordination: block), take the lease
milestone.<project>.<M> for this checkout: its holder is <user>/<checkout>, the
credential's user and the checkout's id, so one person's two checkouts are two holders
(v1.5 stage 1a, proposal section 3.1). Claiming a milestone this checkout already holds
keeps it. Held by another checkout, the claim is refused (session.not-lease-holder,
naming the holder).

The lease lives while a run renews it: TTL 30 s, renewed every 10 s by the apex-loop run
for M (` + "`ape task apex-loop --args <M>`" + `), whose lock is per milestone in an ensemble's
project. Between runs it may lapse; the next run, or change new/start of M, takes it
again unless another member claimed M meanwhile. change new --milestone M and change
start of M's changes refuse without it.

In a project integrating through GitHub (coordination.github), the claim also prepares
milestone/<M> (section 5.1): fetched from main's remote when it has one, else cut from
the remote's main and pushed there, with a local milestone/<M> tracking it. change new
--milestone M and change start of M's changes then run on milestone/<M>, switching to it
from another base branch when the tree is clean (milestone.branch-missing in a checkout
that never claimed M). A failed fetch or push is base.fetch-failed; the lease stays held,
and claiming again retries the branch step. The token is kept in this checkout's lease book
(_apex/state/coord/tokens.json), what a re-claim after a rebuild presents.

A person's act: refused in a model session. Needs NATS (coord.nats-unreachable).

Exit codes: 0 claimed · 1 refused (held by another, no ensemble, NATS) · 2 usage.`,
		Example: `  ape milestone claim M3`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := apexv1.RequirePersonFlag("ape milestone claim"); err != nil {
				return usageErr(err)
			}
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			c, err := member.ClaimMilestone(cmd.Context(), p, args[0])
			if errors.Is(err, member.ErrNotMember) {
				return usageErr(errNotInEnsemble)
			}
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			l := c.Lease
			out := map[string]any{"milestone": args[0], "key": l.Key, "holder": l.Holder, "token": l.Token.String()}
			if c.Branch != "" {
				out["branch"], out["branch_created"] = c.Branch, c.Created
			}
			return v1Emit(cmd.OutOrStdout(), &f, out, func(w io.Writer) {
				fmt.Fprintf(w, "%s claimed by %s (token %s); its loop keeps it: `ape task apex-loop --args %s`\n", args[0], l.Holder, l.Token, args[0])
				if c.Branch != "" {
					verb := "tracks the remote's"
					if c.Created {
						verb = "cut from main and pushed"
					}
					fmt.Fprintf(w, "%s: %s; %s's changes start from it\n", c.Branch, verb, args[0])
				}
			})
		},
	}
	addV1Flags(cmd, &f)
	return cmd
}

func newMilestoneReleaseCmd() *cobra.Command {
	var f v1Flags
	cmd := &cobra.Command{
		Use:   "release <M>",
		Short: "Give up a milestone's lease (a project in an ensemble)",
		Long: `Delete milestone.<project>.<M> at its revision when this checkout holds it, and mark it
released in the lease book, so no later run of M takes it again without a new claim
(v1.5 stage 1a). Releasing a milestone this checkout no longer holds (lapsed, or taken
by another) marks the book only. A person's act: refused in a model session.

Exit codes: 0 released · 1 refused (no ensemble, NATS) · 2 usage.`,
		Example: `  ape milestone release M3`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := apexv1.RequirePersonFlag("ape milestone release"); err != nil {
				return usageErr(err)
			}
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			held, err := member.ReleaseMilestone(cmd.Context(), p, args[0])
			if errors.Is(err, member.ErrNotMember) {
				return usageErr(errNotInEnsemble)
			}
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, map[string]any{"milestone": args[0], "held": held}, func(w io.Writer) {
				if held {
					fmt.Fprintf(w, "%s released\n", args[0])
					return
				}
				fmt.Fprintf(w, "%s was not held by this checkout; the book marks it released\n", args[0])
			})
		},
	}
	addV1Flags(cmd, &f)
	return cmd
}

func newMilestoneTakeInCmd() *cobra.Command {
	var f v1Flags
	cmd := &cobra.Command{
		Use:   "take-in <M> [--wait 9m]",
		Short: "Take main into milestone/<M> through its merge queue (a project integrating through GitHub)",
		Long: `In a project integrating through GitHub (coordination.github), take main into
milestone/<M> (v1.5 stage 1a, proposal section 5.2): when main moved past the milestone,
ape makes the merge of main into milestone/<M>, merged and never rebased (a rebase
would rewrite the protected branch), as take-in-<n>, a change of the milestone. It
pushes it to apex/take-in-<n>, opens its PR into milestone/<M> and enqueues it; the
queue's check is the regression. Main already in the milestone: up to date, nothing
opened.

The run then waits for the queue as a coordinated archive does: merged, the local
milestone/<M> moves to the merged tip; ejected, the take-in is made again and
re-enqueued, an attempt (limits.attempts: integration.ejections-exceeded). A merge
that conflicts is laid out on change/take-in-<n>, HEAD there, the conflict markers in
the tree (change.conflicted, exit 3, action apply): apex-apply resolves them, ` + "`git add`" + `
and ` + "`git commit --no-edit`" + `, and the next take-in pushes that resolution. A milestone
takes main in again before its close. Records that reach main through record PRs
(a milestone's approval, section 5.4) reach milestone/<M> the same way: a milestone
claimed before its approval merged starts its changes once a take-in brought it.

Needs this checkout's lease on M (` + "`ape milestone claim`" + `) and NATS.

Exit codes: 0 up to date, or merged · 1 refused (no lease, NATS, push, PR, the attempts
limit) · 2 usage, or a project not integrating through GitHub · 3 conflict · 75 still
queued when --wait ran out (the run goes on; call again).`,
		Example: "  ape milestone take-in M2 --wait 9m --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			ms := args[0]
			if err := member.Gate(cmd.Context(), p, "milestone take-in", ms, time.Now()); err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return runLong(cmd, &f, p, apexv1.TakeInPrefix+ms, func(ctx context.Context, opt apexv1.RunOptions) renderFn {
				wake, stop, _ := member.Watch(ctx, p)
				defer stop()
				progress := func(line string) {
					if opt.Progress != nil {
						fmt.Fprintln(opt.Progress, line)
					}
				}
				res, err := p.TakeInUntilMerged(ctx, ms, progress, wake)
				return func(cmd *cobra.Command, f *v1Flags) error {
					if err != nil {
						return v1Fail(cmd, f, err, res)
					}
					return v1Emit(cmd.OutOrStdout(), f, res, func(w io.Writer) {
						switch in := res.Integration; {
						case res.UpToDate:
							fmt.Fprintf(w, "%s is up to date with main (%.7s): nothing to take in\n", res.Branch, res.Main)
						case in != nil && in.State == apexv1.IntegrationMerged:
							fmt.Fprintf(w, "took main into %s: %s merged through %s\n", res.Branch, in.Change, in.PR.URL)
						case in != nil:
							fmt.Fprintf(w, "queued %s: %s in %s's merge queue (attempt %d)\n", in.Change, in.PR.URL, res.Branch, in.Attempts)
						}
					})
				}
			})
		},
	}
	addV1Flags(cmd, &f)
	addV1TimeoutFlag(cmd, &f)
	addV1WaitFlags(cmd, &f)
	return cmd
}
