package apecmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
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

Every act runs only when the orders name it under acts:. The acts:
  everyday       answer (change answer), question-answer (ape question answer),
                 tweak-approve (change approve)
  weightier      milestone-approve, accept-task (a human-review task naming a delegate),
                 decision-accept, prepare-release, push-branch, open-pr (a work branch,
                 never main, the base branch or a tag)
  never          accept-config, accept-red, allow-pending-human, push (to main, the base
                 branch or a tag), tag, release, and the orders themselves

The orders count only once the person accepts them, from a terminal: ` + "`ape orders accept`" + `
writes "accepted" (who, when, and the sha256 of what was accepted) into the file on the base
branch. Any later edit, the person's own included, leaves them unaccepted until accepted
again. ` + "`ape orders revoke`" + ` writes "revoked": no delegated act runs under them from then
on. Both commit on the base branch wherever HEAD is, so they count at once. Neither runs in a
model session, with or without the person's words (exit 2, session.person-only).

The header is read strictly: an unknown key or act, a never-delegated act, an act listed twice,
a condition on an act the orders cannot use, a malformed condition, or a tweak type the project
does not have is orders.invalid, naming the line. Nothing in it is ever ignored.

A conductor acts under them only through ` + "`ape conductor act`" + `, which checks the orders, the
act's tier, the conditions and the evidence, and re-runs the act's mechanical check, before it
runs the act.`,
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
	var by, from string
	short := map[string]string{
		"accept": "Accept your standing orders as they are on the base branch (a person, from a terminal)",
		"revoke": "Revoke your standing orders: no delegated act runs under them from now on (a person, from a terminal)",
	}[verb]
	cmd := &cobra.Command{
		Use:   verb + " --by <email>",
		Short: short,
		Long: short + `.

--by is the orders' person (their git user.email, or its slug form); another person's orders
are refused (session.by-mismatch). ape commits "apex: orders ` + map[string]string{"accept": "accepted", "revoke": "revoked"}[verb] + ` by <person>" on the base
branch, wherever HEAD is (a change branch included), and it counts at once.

Refused in any model session (exit 2, session.person-only): the orders themselves are never
delegated, and no grant from the person's words covers them.

` + map[string]string{"accept": `
--from <draft> accepts a draft instead (your conductor drafts your orders with you and writes
them outside _apex/standing-orders/, which it cannot write): ape parses it strictly and shows
what it will enforce — each act with its tier, each condition — and what it reads as free text
only, then the diff against your current orders, and asks y/N. On y it writes the file and its
acceptance in one commit on the base branch; on anything else nothing is written
(orders.declined). A draft whose header ape cannot fully read is refused, naming the line
(orders.invalid): nothing in it is ever ignored.
`, "revoke": ""}[verb] + `
Exit codes: 0 done (prints the state) · 1 no orders, a file that does not parse, declined, or git
failed · 2 usage, a model session, or --by not the orders' person.`,
		Example: "  ape orders " + verb + " --by dana@example.com" + map[string]string{"accept": "\n  ape orders accept --by dana@example.com --from _output/standing-orders-draft.md", "revoke": ""}[verb],
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RefuseV0(cmd, &f, p); err != nil {
				return err
			}
			if from != "" {
				return acceptOrdersFrom(cmd, &f, p, by, from)
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
	if verb == "accept" {
		cmd.Flags().StringVar(&from, "from", "", "a draft to accept: shows the parsed orders and the diff, then asks y/N")
	}
	return cmd
}

// acceptOrdersFrom is `ape orders accept --from <draft>` (D662.1).
func acceptOrdersFrom(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, by, from string) error {
	if err := apexv1.RequirePersonFlag("ape orders accept --from"); err != nil {
		return v1Fail(cmd, f, err, nil)
	}
	data, err := os.ReadFile(from)
	if err != nil {
		return v1Fail(cmd, f, &apexv1.UsageError{Code: "usage.from", Msg: "--from " + from + ": " + err.Error()}, nil)
	}
	d, err := p.ReadOrdersDraft(cmd.Context(), by, data)
	if err != nil {
		return v1Fail(cmd, f, err, nil)
	}
	w := cmd.ErrOrStderr()
	printOrdersTable(w, d.Orders, from)
	if d.Diff == "" {
		fmt.Fprintln(w, "\nNo orders in force yet: these would be your first.")
	} else {
		fmt.Fprintf(w, "\nChanges from the orders in force:\n%s", d.Diff)
	}
	fmt.Fprint(w, "\nAccept these orders? [y/N] ")
	answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
		return v1Fail(cmd, f, &apexv1.FailError{Code: apexv1.CodeOrdersDeclined, Msg: "not accepted: nothing was written"}, nil)
	}
	o, err := p.AcceptOrdersFrom(cmd.Context(), d, by)
	if err != nil {
		return v1Fail(cmd, f, err, nil)
	}
	return v1Emit(cmd.OutOrStdout(), f, map[string]any{"ok": true, "orders": o}, func(w io.Writer) {
		fmt.Fprintf(w, "orders of %s: %s (%s)\n", o.Person, o.State, o.Path)
	})
}

// printOrdersTable is what the person approves at accept --from: what ape
// enforces, and what it only passes to the conductor.
func printOrdersTable(w io.Writer, o *apexv1.Orders, from string) {
	fmt.Fprintf(w, "Standing orders of %s, from %s — what ape will enforce:\n\n", o.Person, from)
	fmt.Fprintln(w, "  act                  tier  delegated")
	for _, a := range apexv1.DelegableActs() {
		state := "no (not named)"
		if o.Names(a) {
			state = "yes (named)"
		}
		fmt.Fprintf(w, "  %-20s %-5d %s\n", a, apexv1.OrderTierOf(a), state)
	}
	fmt.Fprintln(w, "  never: "+strings.Join(apexv1.NeverActs(), ", "))
	fmt.Fprintln(w, "\nEscalate (always to you, even for a named act):")
	var esc []string
	if o.Escalate.TouchesGate {
		esc = append(esc, apexv1.EscalateTouchesGate+": a change that touches its milestone's gate")
	}
	if o.Escalate.ChangesConfig {
		esc = append(esc, apexv1.EscalateChangesConfig+": a change that edits a guarded block of _apex/config.yaml")
	}
	if o.Escalate.TokensOver > 0 {
		esc = append(esc, fmt.Sprintf("%s: a milestone past %d tokens since its approval", apexv1.EscalateTokensOver, o.Escalate.TokensOver))
	}
	if len(esc) == 0 {
		esc = []string{"none"}
	}
	for _, e := range esc {
		fmt.Fprintln(w, "  "+e)
	}
	fmt.Fprintln(w, "\nConditions:")
	n := 0
	if c := o.Conditions.TweakApprove; c != nil {
		fmt.Fprintf(w, "  tweak-approve: only tweaks of type %s\n", strings.Join(c.Types, ", "))
		n++
	}
	if c := o.Conditions.MilestoneApprove; c != nil {
		if c.MaxChanges != nil {
			fmt.Fprintf(w, "  milestone-approve: at most %d changes\n", *c.MaxChanges)
		}
		if c.Gate != "" {
			fmt.Fprintf(w, "  milestone-approve: only when its last gate run is %s\n", c.Gate)
		}
		n++
	}
	if c := o.Conditions.Answer; c != nil && c.QuestionOnly {
		fmt.Fprintln(w, "  answer: only a change blocked on a question, never on a limit")
		n++
	}
	if n == 0 {
		fmt.Fprintln(w, "  none")
	}
	lines := len(strings.Split(strings.TrimSpace(o.Body), "\n"))
	if strings.TrimSpace(o.Body) == "" {
		lines = 0
	}
	fmt.Fprintf(w, "\nFree text: %d line(s) the conductor reads as advice; ape enforces none of it.\n", lines)
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
