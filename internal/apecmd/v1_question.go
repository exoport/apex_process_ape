package apecmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

func newQuestionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "question",
		Short: "Questions for the person: ask (any session), answer (a person, or their conductor under orders), list",
		Long: `Questions as acts (v1.4.2): an orchestrator asks the person a design question through ape
instead of in its chat, so it is recorded and reaches them.

  ask      record a question (any session may ask): the person event and the notifier carry it
  answer   it: a person's act — or their conductor's, under standing orders that name
           question-answer` + "`ape conductor act question-answer Q<n> --text …`" + `)
  list     the questions, open or all

An open question is a person action: ` + "`ape status`" + ` lists it ("answer Q<n>: <answer>"), and
` + "`ape status --wait --until person`" + ` wakes on it. The questions are kept in
_apex/state/questions.json (local).`,
	}
	cmd.AddCommand(newQuestionAskCmd(), newQuestionAnswerCmd(), newQuestionListCmd())
	return cmd
}

func newQuestionAskCmd() *cobra.Command {
	var f v1Flags
	var milestone, change string
	var options []string
	cmd := &cobra.Command{
		Use:   "ask \"<question>\" [--milestone M] [--change <id>] [--option \"…\"]…",
		Short: "Record a question for the person; prints its id (Q<n>)",
		Long: `Record a question for the person, with the milestone or change it is about and the options
you see. Any session may ask: asking is no person act. ape logs the question event, so the
project's notify: command runs, and the question is a person action until it is answered.

Exit codes: 0 recorded (prints the id) · 2 usage (no text).`,
		Example: "  ape question ask \"Invoices in EUR or the customer's currency?\" --milestone M3 --option EUR --option \"customer's\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RefuseV0(cmd, &f, p); err != nil {
				return err
			}
			q, err := p.AskQuestion(cmd.Context(), apexv1.Question{Text: args[0], Milestone: milestone, Change: change, Options: options}, cmd.ErrOrStderr())
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, q, func(w io.Writer) {
				fmt.Fprintf(w, "%s asked\n", q.ID)
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().StringVar(&milestone, "milestone", "", "the milestone it is about")
	cmd.Flags().StringVar(&change, "change", "", "the change it is about")
	cmd.Flags().StringArrayVar(&options, "option", nil, "an option you see (repeatable)")
	return cmd
}

func newQuestionAnswerCmd() *cobra.Command {
	var f v1Flags
	var text, by string
	cmd := &cobra.Command{
		Use:   "answer <Q-id> --text \"<answer>\" --by <who>",
		Short: "Answer a question (a person's act)",
		Long: `Answer a recorded question. A person's act: refused in a model session unless, in the
person's own Claude Code session, the person typed "answer Q<n>: <answer>" in this turn (E2;
--by is then git config user.name, recorded with the prompt). A conductor answers under the
person's standing orders with ` + "`ape conductor act question-answer Q<n> --text …`" + `.

Exit codes: 0 answered · 1 no such question (question.not-found), or answered already
(question.answered) · 2 usage, or a model session.`,
		Example: "  ape question answer Q3 --text \"EUR\" --by dana",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RefuseV0(cmd, &f, p); err != nil {
				return err
			}
			if err := v1PersonAct(cmd, p, personCallOf(cmd, args)); err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			q, err := p.AnswerQuestion(cmd.Context(), args[0], text, personBy(cmd.Context(), by))
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, q, func(w io.Writer) {
				fmt.Fprintf(w, "%s answered by %s\n", q.ID, q.Answer.By)
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().StringVar(&text, "text", "", "the answer (required)")
	cmd.Flags().StringVar(&by, "by", "", "who answers (required from a terminal)")
	return cmd
}

func newQuestionListCmd() *cobra.Command {
	var f v1Flags
	var open bool
	cmd := &cobra.Command{
		Use:   "list [--open]",
		Short: "List the questions (open, or all)",
		Long: `List the project's questions, oldest first: id, what it is about, the text, and the answer
when there is one. --open lists only those not answered. --json prints the list.

Exit codes: 0 printed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			qs, err := p.Questions()
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			if open {
				qs = p.OpenQuestions()
			}
			return v1Emit(cmd.OutOrStdout(), &f, qs, func(w io.Writer) {
				if len(qs) == 0 {
					fmt.Fprintln(w, "no questions")
				}
				for _, q := range qs {
					state := "open"
					if q.Answer != nil {
						state = "answered by " + q.Answer.By + ": " + q.Answer.Text
					}
					fmt.Fprintf(w, "%s %s%s — %s (%s)\n", q.ID, q.Milestone, q.Change, q.Text, state)
				}
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().BoolVar(&open, "open", false, "only the open questions")
	return cmd
}
