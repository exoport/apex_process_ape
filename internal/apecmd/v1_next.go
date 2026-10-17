package apecmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newNextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "next",
		Short: "Record a finding for later under the roadmap's Next",
		Long: `The roadmap's Next holds what was noticed but is out of scope now (H26): one dated line
per finding, which apex-plan triages at the next delivery — a milestone, kept, or deleted
with the reason in its commit. A "## Next" section of roadmap.md is not a milestone.`,
	}
	cmd.AddCommand(newNextAddCmd())
	return cmd
}

func newNextAddCmd() *cobra.Command {
	var f v1Flags
	var from string
	cmd := &cobra.Command{
		Use:   `add "<text>" [--from <change-id>]`,
		Short: "Append a dated finding under the roadmap's Next, on the base branch",
		Long: `Append one line to development/roadmap.md's Next:

  - YYYY-MM-DD <text> (from <change-id>)

(without "(from …)" when --from is not given) at the end of its "## Next" section; else
under apex-plan's "Next — not milestones:" label above the first milestone, after its list;
else in a new "## Next" section at the end of the file. Committed alone on the base branch
as "apex: next — <text>": from the base branch itself, or from a change's branch,
change/<id>, without a checkout — the change's branch and working tree do not move, and a
base branch moved only by such a record does not make the change catch up. Refused when
the base branch is checked out in another worktree (git.base-checked-out).

For models and people alike: apex-apply and apex-change record what they notice outside
the change's scope here instead of building it (--from <id>, the change that noticed it).

Exit codes: 0 recorded · 1 no roadmap.md, or the base branch is checked out elsewhere ·
2 usage (empty or multi-line text, --from not a change id).`,
		Example: `  ape next add "the CSV export ignores --tz" --from add-tenant-export
  ape next add "ask finance whether refunds need an audit trail"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			res, err := p.AddNext(cmd.Context(), args[0], from)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				on := ""
				if res.Branch != "" {
					on = " on " + res.Branch
				}
				fmt.Fprintf(w, "%s: %s (committed%s)\n", res.Path, res.Line, on)
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().StringVar(&from, "from", "", "the change that found it")
	return cmd
}
