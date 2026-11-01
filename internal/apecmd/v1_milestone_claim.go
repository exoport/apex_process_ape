package apecmd

import (
	"errors"
	"fmt"
	"io"

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
start of M's changes refuse without it. The token is kept in this checkout's lease book
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
			l, err := member.ClaimMilestone(cmd.Context(), p, args[0])
			if errors.Is(err, member.ErrNotMember) {
				return usageErr(errNotInEnsemble)
			}
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, map[string]any{"milestone": args[0], "key": l.Key, "holder": l.Holder, "token": l.Token.String()}, func(w io.Writer) {
				fmt.Fprintf(w, "%s claimed by %s (token %s); its loop keeps it: `ape task apex-loop --args %s`\n", args[0], l.Holder, l.Token, args[0])
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
