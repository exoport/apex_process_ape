package apecmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/member"
)

// newCoordCmd is `ape coord`: a member's acts on the ensemble's
// coordination (v1.5).
func newCoordCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "coord",
		Short: "APEX v1.5 (in development): a member's acts on its ensemble's coordination",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	notice := &cobra.Command{
		Use:   "notice",
		Short: "The assessor's impact notices",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	notice.AddCommand(newCoordNoticeAckCmd())
	cmd.AddCommand(notice)
	return cmd
}

// newCoordNoticeAckCmd is `ape coord notice ack` (PLAN-V15-ASSESSOR step 6).
func newCoordNoticeAckCmd() *cobra.Command {
	var f v1Flags
	cmd := &cobra.Command{
		Use:   "ack <notice-id> acted|dismissed",
		Short: "Record what the person did about an impact notice (a person's act, from the checkout holding its milestone)",
		Long: `Record that an impact notice (the assessor's: does a change elsewhere affect this
milestone?) was acted on or dismissed (v1.5 §3.2, Q3). It publishes notice-acked for the
notice's milestone, by the credential's user (no --by), once per notice; ` + "`ape status`" + ` then
no longer lists the notice among the milestone's open ones. Only the person whose
checkout holds the notice's milestone records it, from that checkout and at a terminal:
a model session is refused (session.person-only), a loop never acks, and no standing
order covers it. The ids are in ` + "`ape status --json`" + `'s milestones[].notices and in the
conductor's delivery.

Exit codes: 0 recorded · 1 refused (not this checkout's claim, an unknown or already
acknowledged notice, NATS) · 2 usage, or a model session.`,
		Example: "  ape coord notice ack 20261103T040000Z-1-atomix-M1 acted",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := apexv1.RequirePersonFlag(apexv1.PersonCoordNoticeAck); err != nil {
				return usageErr(err)
			}
			if args[1] != member.NoticeActed && args[1] != member.NoticeDismissed {
				return usageErr(fmt.Errorf("ape coord notice ack <id> acted|dismissed: %q is neither", args[1]))
			}
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			res, err := member.AckNotice(cmd.Context(), p, args[0], args[1])
			if errors.Is(err, member.ErrNotMember) {
				return usageErr(errNotInEnsemble)
			}
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				fmt.Fprintf(w, "notice %s (%s): %s, by %s\n", res.ID, res.Target, res.Outcome, res.By)
			})
		},
	}
	addV1Flags(cmd, &f)
	return cmd
}
