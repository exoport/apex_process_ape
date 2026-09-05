package apecmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/exoport/apex_process_ape/internal/memory"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/spf13/cobra"
)

func newContextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Inspect the project-context document",
		Long: `project-context.md is the standards document every skill loads whole —
architecture, ADR and pattern passes, both story batches, the reviewers.
It grows by append, and it is bounded by the same 256 KiB Read cap
team-memory.md is, so the same two budgets apply to it.

  check  size against the soft budget and hard ceiling, from a stat alone`,
	}
	cmd.AddCommand(newContextCheckCmd())
	return cmd
}

func newContextCheckCmd() *cobra.Command {
	var (
		cwdFlag      string
		outputFormat string
		soft         int64
		hard         int64
		failAt       string
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Report project-context size against the soft budget and hard ceiling",
		Long: `Classify {development_folder}/project-context.md against two budgets,
from an os.Stat alone — the file is never read, which is what lets the
generator take the measurement BEFORE the full read it would otherwise
die on.

  state: absent      no project-context.md yet (the governance pipeline
                     writes it; before that, not a problem)
  state: ok          under the soft budget
  state: over-soft   compaction is due
  state: over-hard   approaching Claude Code's 256 KiB Read cap — the file
                     is about to become unreadable by its own writer

The budgets are the pair 'ape memory check' already enforces on
team-memory.md, from one set of constants rather than two restatements of
the numbers: soft 40960 B, hard 204800 B.

Only the canonical path is measured. Reader skills carry a
'**/project-context.md' fallback for a lifted project that put the file
somewhere else; a size gate has to name the file it measured, so an
'absent' verdict here prints the path it looked at rather than searching
for another candidate.

EXIT 0 BY DEFAULT, whatever the state — the same contract 'ape memory
check' keeps, and for the same reason: the framework's prose convention
is "on non-zero exit: HALT", so a failing exit would abort the generator
at exactly the moment compaction is due.

--fail-at opts into a non-zero exit for CI, which wants one:
  never  (default) always exit 0
  soft   exit 1 at over-soft or worse
  hard   exit 1 at over-hard

The token count in the output is bytes/4, an estimate, and nothing gates
on it.`,
		Args:    cobra.NoArgs,
		Example: "  ape context check\n  ape context check --output-format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateFailAt(failAt); err != nil {
				return err
			}
			cfg := resolveProjectConfig(cwdFlag)
			// Not `absent`: with no development_folder there is no path to
			// stat, so the check has no basis to say the file is missing.
			// Exit 2, the preflight code `ape story` uses for the same
			// class of unconfigured folder.
			if cfg.Paths.ProjectContext == "" {
				return usageErr(errors.New(
					"development_folder is not configured, so project-context.md has no location"))
			}
			check := memory.CheckSize(cfg.Paths.ProjectContext, soft, hard)
			format := output.Format(outputFormat)
			if format != output.FormatHuman {
				if err := output.Print(cmd.OutOrStdout(), format, check); err != nil {
					return err
				}
			} else {
				emitContextCheckHuman(cmd.OutOrStdout(), check)
			}
			if sizeCheckShouldFail(check.State, failAt) {
				return gateErr(1, nil)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	cmd.Flags().Int64Var(&soft, "soft", 0,
		fmt.Sprintf("Soft budget in bytes (default %d)", memory.DefaultSoftBudget))
	cmd.Flags().Int64Var(&hard, "hard", 0,
		fmt.Sprintf("Hard ceiling in bytes (default %d)", memory.DefaultHardCeiling))
	cmd.Flags().StringVar(&failAt, "fail-at", failAtNever, "Exit 1 at this state or worse: never|soft|hard")
	return cmd
}

func emitContextCheckHuman(w io.Writer, c memory.Check) {
	emitSizeCheckLine(w, "project-context", c)
	switch c.State {
	case memory.StateOverSoft:
		fmt.Fprintln(w,
			"compaction is due — apex-generate-project-context's size gate folds it with apex-distillator --single-file")
	case memory.StateOverHard:
		fmt.Fprintf(w,
			"OVER THE HARD CEILING (%s): the soft gate should have caught this several runs ago.\n"+
				"Fold it before the next run that reads it whole, and treat the miss as a bug report rather than routine.\n",
			humanBytes(c.HardCeiling))
	case memory.StateAbsent, memory.StateOK:
	}
}
