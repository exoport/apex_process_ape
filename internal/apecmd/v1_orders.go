package apecmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

func newOrdersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "orders",
		Short: "A person's standing orders for their conductor: show, accept, revoke",
		Long: `Standing orders (v1.4.2) are a person's delegation to their conductor: a committed file
per person, ` + apexv1.OrdersDirRel + `/<person>.md, where <person> is their git user.email
in slug form (dana@example.com → dana-example-com). Its frontmatter is what ape checks:

  ---
  person: dana@example.com
  acts: [answer, tweak-approve, milestone-approve]   # see the tiers below
  conditions:
    tweak-approve: {types: [fix, docs]}             # only tweaks of these types
    milestone-approve: {max_changes: 6, gate: red}  # at most 6 changes; the last gate run red
    answer: {question_only: true}                   # only a change blocked on a question
  ---
  Free text the conductor reads: how to answer, what to bring back to me.

The acts, by tier:
  any accepted orders   answer (change answer), tweak-approve (change approve)
  only when named       milestone-approve, accept-task (a human-review task naming a
                        delegate), decision-accept
  never                 accept-config, accept-red, allow-pending-human, push, tag, release,
                        and the orders themselves

The orders count only once the person accepts them, from a terminal: ` + "`ape orders accept`" + `
writes "accepted" (who, when, and the sha256 of what was accepted) into the file on the base
branch. Any later edit, the person's own included, leaves them unaccepted until accepted
again. ` + "`ape orders revoke`" + ` writes "revoked": no delegated act runs under them from then
on. Both commit on the base branch wherever HEAD is, so they count at once. Neither runs in a
model session, with or without the person's words (exit 2, session.person-only).

A conductor acts under them only through ` + "`ape conductor act`" + `, which checks the orders, the
act's tier, the conditions and the evidence before it runs the act.`,
	}
	cmd.AddCommand(newOrdersShowCmd(), newOrdersMarkCmd("accept"), newOrdersMarkCmd("revoke"))
	return cmd
}

func newOrdersShowCmd() *cobra.Command {
	var f v1Flags
	var person string
	cmd := &cobra.Command{
		Use:   "show [--person <email>]",
		Short: "Print a person's standing orders as ape reads them on the base branch, and their state",
		Long: `Print the orders of --person (default: git config user.email) as ape reads them on the
base branch: their state (accepted, unaccepted or revoked), the acts with their tiers, the
conditions, the marks, and the text. --json prints the same, for the conductor.

Exit codes: 0 printed (whatever the state) · 1 no orders (orders.missing) or a file that does
not parse (orders.invalid) · 2 usage.`,
		Example: "  ape orders show\n  ape orders show --person dana@example.com --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RefuseV0(cmd, &f, p); err != nil {
				return err
			}
			who, err := ordersPerson(cmd, p, person)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			o, err := p.LoadOrders(cmd.Context(), who)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			tiers := map[string]int{}
			for _, a := range o.Acts {
				tiers[a] = int(apexv1.OrderTierOf(a))
			}
			return v1Emit(cmd.OutOrStdout(), &f, map[string]any{"orders": o, "tiers": tiers}, func(w io.Writer) {
				fmt.Fprintf(w, "%s (%s on %s): %s\n", o.Person, o.Path, o.Branch, o.State)
				for _, a := range o.Acts {
					fmt.Fprintf(w, "  act %-18s tier %d\n", a, apexv1.OrderTierOf(a))
				}
				if o.Accepted != nil {
					fmt.Fprintf(w, "  accepted by %s at %s\n", o.Accepted.By, o.Accepted.At)
				}
				if o.Revoked != nil {
					fmt.Fprintf(w, "  revoked by %s at %s\n", o.Revoked.By, o.Revoked.At)
				}
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().StringVar(&person, "person", "", "whose orders (default: git config user.email)")
	return cmd
}

func newOrdersMarkCmd(verb string) *cobra.Command {
	var f v1Flags
	var by string
	short := map[string]string{
		"accept": "Accept your standing orders as they are on the base branch (a person, from a terminal)",
		"revoke": "Revoke your standing orders: no delegated act runs under them from now on (a person, from a terminal)",
	}[verb]
	cmd := &cobra.Command{
		Use:   verb + " --by <email>",
		Short: short,
		Long: short + `.

--by is the orders' person (their git user.email, or its slug form); another person's orders
are refused (session.by-mismatch). ape commits "apex: orders ` + verb + `ed by <person>" on the base
branch, wherever HEAD is (a change branch included), and it counts at once.

Refused in any model session (exit 2, session.person-only): the orders themselves are never
delegated, and no grant from the person's words covers them.

Exit codes: 0 done (prints the state) · 1 no orders, a file that does not parse, or git failed ·
2 usage, a model session, or --by not the orders' person.`,
		Example: "  ape orders " + verb + " --by dana@example.com",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RefuseV0(cmd, &f, p); err != nil {
				return err
			}
			mark := p.AcceptOrders
			if verb == "revoke" {
				mark = p.RevokeOrders
			}
			o, err := mark(cmd.Context(), strings.TrimSpace(by), by)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, map[string]any{"ok": true, "orders": o}, func(w io.Writer) {
				fmt.Fprintf(w, "orders of %s: %s\n", o.Person, o.State)
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().StringVar(&by, "by", "", "the orders' person: you (required)")
	return cmd
}

// ordersPerson is --person, or the project's git user.email.
func ordersPerson(cmd *cobra.Command, p *apexv1.Project, person string) (string, error) {
	if person = strings.TrimSpace(person); person != "" {
		return person, nil
	}
	if e := apexv1.GitUserEmail(cmd.Context(), p.Root); e != "" {
		return e, nil
	}
	return "", &apexv1.UsageError{Code: "usage.person", Msg: "no --person, and git config user.email is unset: name whose orders"}
}
