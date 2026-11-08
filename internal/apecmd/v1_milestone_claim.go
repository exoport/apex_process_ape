package apecmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/member"
)

// errNotInEnsemble refuses a lease verb in a project with no
// coordination: block: an uncoordinated project has no milestone leases.
var errNotInEnsemble = errors.New("this project is in no ensemble (no coordination: block in _apex/config.yaml): its milestones have no leases, and its one loop runs any of them")

func newMilestoneClaimCmd() *cobra.Command {
	var f v1Flags
	var adopt bool
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
the remote's main and pushed there, with a local milestone/<M> tracking it. A remote
milestone/<M> that no claim of M recorded on main explains is judged first (D736): with
nothing of its own (an ancestor of main, such as a leftover probe) it is fast-forwarded
to main, so M starts from main; with commits main lacks the claim refuses
(milestone.branch-exists) unless --adopt, a person's act from a terminal only, takes it as it is. change new
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
			// D736+: --adopt is a person's act of its own, from a terminal
			// only, never granted or ordered (orders: milestone-adopt, never).
			if adopt {
				if err := apexv1.RequirePersonFlag(apexv1.PersonMilestoneAdopt); err != nil {
					return usageErr(err)
				}
			}
			if err := apexv1.RequirePersonFlag(apexv1.PersonMilestoneClaim); err != nil {
				return usageErr(err)
			}
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			c, err := member.ClaimMilestoneAdopt(cmd.Context(), p, args[0], adopt)
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
				if c.Note != "" {
					out["branch_note"] = c.Note
				}
			}
			if c.Record != nil {
				out["record_pr"] = c.Record.PR
			}
			return v1Emit(cmd.OutOrStdout(), &f, out, func(w io.Writer) {
				fmt.Fprintf(w, "%s claimed by %s (token %s); its loop keeps it: `ape task apex-loop --args %s`\n", args[0], l.Holder, l.Token, args[0])
				if r := c.Record; r != nil {
					fmt.Fprintln(w, "claim record: "+recordLine(r))
				}
				if c.Note != "" {
					fmt.Fprintln(w, c.Note)
				}
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
	cmd.Flags().BoolVar(&adopt, "adopt", false, "take a milestone/<M> the remote already has, with commits main lacks and no claim of M recorded on main, as it is: a person's act, from a terminal only (session.person-only in a model session; never delegated by orders); without it the claim refuses milestone.branch-exists")
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
			if err := apexv1.RequirePersonFlag(apexv1.PersonMilestoneRelease); err != nil {
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

func newMilestoneOwnerCmd() *cobra.Command {
	var f v1Flags
	var to, by string
	cmd := &cobra.Command{
		Use:   "owner --to <user> --by <who>",
		Short: "Hand a trunk project over to another person (a person's act)",
		Long: `A trunk project (integration: trunk, D876) is for one person per repository: its first
milestone claim records the claimant's NATS user as its owner
(milestone.<project>._trunk-owner, create-only, no TTL), and a claim by any other user is
refused (coord.trunk-one-member, which names queue/* for several people). This hands the
project to <user> (a NATS user name, the credential's user token), by a compare-and-swap
of the owner key, recording --by. Only the owner may, and only while it holds no live
milestone lease in the project (release them first: ` + "`ape milestone release <M>`" + `). The check and
the swap are two steps, so a claim by another of the owner's checkouts between them would run on
under the new owner: hand over with the old owner's loops stopped. A lost
owner's key is removed by an operator with the cluster's admin credential.

The owner key refuses a mistake, not an attack: any member's credential may write the
project's milestone keys. A person's act: refused in a model session. Needs NATS.

Exit codes: 0 handed over · 1 refused (not the owner, a lease still held, not trunk, NATS) · 2 usage.`,
		Example: `  ape milestone owner --to bob --by diegos`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := apexv1.RequirePersonFlag(apexv1.PersonMilestoneOwner); err != nil {
				return usageErr(err)
			}
			if strings.TrimSpace(to) == "" || strings.TrimSpace(by) == "" {
				return usageErr(errors.New("--to <user> and --by <who> are required"))
			}
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			from, err := member.HandTrunkOwner(cmd.Context(), p, to, strings.TrimSpace(by))
			if errors.Is(err, member.ErrNotMember) {
				return usageErr(errNotInEnsemble)
			}
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, map[string]any{"from": from, "to": to, "by": by}, func(w io.Writer) {
				fmt.Fprintf(w, "the project is %s's now (handed over by %s, decided by %s)\n", to, from, by)
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().StringVar(&to, "to", "", "the NATS user who owns the project after this")
	cmd.Flags().StringVar(&by, "by", "", "the person who decided the handover")
	return cmd
}

func newMilestonePauseCmd() *cobra.Command { return milestonePauseCmd(true) }

func newMilestoneResumeCmd() *cobra.Command { return milestonePauseCmd(false) }

// milestonePauseCmd is `ape milestone pause|resume` (Q-1C-1, D758).
func milestonePauseCmd(pause bool) *cobra.Command {
	var f v1Flags
	var reason string
	use, short, long, example := "resume <M>", "Resume a paused milestone (a person's act; a project in an ensemble)",
		`Publish milestone-resumed for M: the gate that refuses a paused milestone's change new,
start and archive, its take-in and its close (coord.milestone-paused) lets them through
again. It resumes a person's pause (`+"`ape milestone pause`"+`) and OQ24's (a question the
conductor left unanswered) alike. From the checkout whose lease book holds M's claim
(OQ6), at a terminal: a model session is refused (session.person-only). A stopped loop
does not restart itself: start it again (`+"`ape task apex-loop --args <M>`"+`).

Exit codes: 0 resumed · 1 refused (not this checkout's claim, NATS) · 2 usage, or a model session.`,
		"  ape milestone resume M3"
	if pause {
		use, short, long, example = `pause <M> --reason "…"`, "Pause a milestone: its next change, take-in and close wait (a person's act; a project in an ensemble)",
			`Publish milestone-paused for M, with who paused it (the credential's user) and the
reason: the gate (coord.milestone-paused, action person) then refuses M's change new,
start and archive, its take-in and its close, naming both, until `+"`ape milestone resume <M>`"+`.
A run under way is not stopped: it stops at its next such step, and a loop ends there.
Nothing else changes: no lease, no branch, no other milestone. OQ6: a hold on one
milestone is a person's act of the person whose checkout holds it, so only from the
checkout whose lease book holds M's claim, and at a terminal: a model session is refused
(session.person-only), and no standing order may do it.

Exit codes: 0 paused · 1 refused (not this checkout's claim, NATS) · 2 usage (no
--reason), or a model session.`,
			`  ape milestone pause M3 --reason "wait for M4's gate"`
	}
	verb := apexv1.PersonMilestoneResume
	if pause {
		verb = apexv1.PersonMilestonePause
	}
	cmd := &cobra.Command{
		Use: use, Short: short, Long: long, Example: example,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := apexv1.RequirePersonFlag(verb); err != nil {
				return usageErr(err)
			}
			if pause && strings.TrimSpace(reason) == "" {
				return usageErr(errors.New(`ape milestone pause needs --reason "…": what the milestone waits for`))
			}
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			res, err := member.PauseMilestone(cmd.Context(), p, args[0], strings.TrimSpace(reason), pause)
			if errors.Is(err, member.ErrNotMember) {
				return usageErr(errNotInEnsemble)
			}
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				if res.Paused {
					fmt.Fprintf(w, "%s paused by %s: %s — `ape milestone resume %s` resumes it\n", res.Milestone, res.By, res.Reason, res.Milestone)
					return
				}
				fmt.Fprintf(w, "%s resumed by %s; a loop that stopped on the pause starts again with `ape task apex-loop --args %s`\n", res.Milestone, res.By, res.Milestone)
			})
		},
	}
	if pause {
		cmd.Flags().StringVar(&reason, "reason", "", "what the milestone waits for (required)")
	}
	addV1Flags(cmd, &f)
	return cmd
}

func newMilestoneTakeInCmd() *cobra.Command {
	var f v1Flags
	cmd := &cobra.Command{
		Use:   "take-in <M> [--wait 9m]",
		Short: "Take main into milestone/<M>: the merge, checked by the regression, landed on the branch (a project integrating through GitHub)",
		Long: `In a project integrating through GitHub (coordination.github), take main into
milestone/<M> (v1.5 stage 1a, proposal section 5.2; D702: ape handles the milestone
branches): when main moved past the milestone, ape makes the merge of main into
milestone/<M>, merged and never rebased, as take-in-<n>, a change of the milestone. It
moves the local milestone/<M> to the merge, runs the regression command on it, and lands
it on the remote's milestone/<M> with archive's push: the lease fenced, a fast-forward
only (milestone.moved when the branch moved). Main already in the milestone: up to date,
nothing made.

A merge that conflicts is laid out on change/take-in-<n>, HEAD there, the conflict
markers in the tree (change.conflicted, exit 3, action apply); a merge whose regression
is red is laid out the same way (milestone.take-in-red, action apply). apex-apply
resolves or fixes it and commits (` + "`git add`" + `, ` + "`git commit --no-edit`" + `), and the next take-in
lands that. Nothing lands before the regression is green. A milestone takes main in again
before its close (the close does it itself). Records that reach main through record PRs
(a milestone's approval, section 5.4) reach milestone/<M> the same way: a milestone
claimed before its approval merged starts its changes once a take-in brought it.

Needs this checkout's lease on M (` + "`ape milestone claim`" + `) and NATS. Its run waits for NATS,
and for a clean read of M's pause state (D800+: at most 2 h; --wait 0 refuses at once). From
main (where a claim leaves HEAD) it switches to milestone/<M> on a clean tree, and refuses
otherwise.

Exit codes: 0 up to date, or landed · 1 refused (no lease, NATS under --wait 0, a red regression, the
branch moved, a push) · 2 usage, or a project not integrating through GitHub · 3 conflict ·
75 still running when --wait ran out (the run goes on; call again).`,
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
			stop, err := coordGateHold(cmd, &f, p, "milestone take-in", ms)
			defer stop()
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return runLongArg(cmd, &f, p, apexv1.TakeInPrefix+ms, ms, func(ctx context.Context, opt apexv1.RunOptions) renderFn {
				res, err := p.TakeIn(ctx, ms, opt)
				return func(cmd *cobra.Command, f *v1Flags) error {
					if err != nil {
						return v1Fail(cmd, f, err, res)
					}
					return v1Emit(cmd.OutOrStdout(), f, res, func(w io.Writer) {
						if res.UpToDate {
							fmt.Fprintf(w, "%s is up to date with main (%.7s): nothing to take in\n", res.Branch, res.Main)
							return
						}
						fmt.Fprintf(w, "took main (%.7s) into %s: %s landed as %.7s\n", res.Main, res.Branch, res.ID, res.Commit)
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
