package apecmd

import (
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/sprint"
	"github.com/spf13/cobra"
)

func newSprintCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sprint",
		Short: "Inspect and maintain sprint-status.yaml",
		Long: `Three operations on the tracker, with deliberately different contracts:

  check      compare tracker rows against story files; report divergence,
             never pick a winner, always exit 0
  verify     assert one row landed as written (a gate, exit 0/2/3/4/5)
  reconcile  project an epic's row from its story rows (a mutation)`,
	}
	cmd.AddCommand(newSprintCheckCmd(), newSprintVerifyCmd(), newSprintReconcileCmd())
	return cmd
}

func newSprintCheckCmd() *cobra.Command {
	var (
		cwdFlag      string
		outputFormat string
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Report divergence between the tracker and story files",
		Long: `Set-compare sprint-status.yaml's story rows against story files on disk,
and compare each row's status against that story's own frontmatter.

epic-* and *-retrospective rows are classified out: they have no story
file to diverge from. The tracker's 'drafted' is normalised to a story
file's 'ready-for-dev' for comparison only — neither file is touched — or
every drafted story would report a false divergence.

ALWAYS EXITS 0, even with findings, and there is no --strict. Which side of
a divergence is right is judgment, so this reports both values and picks
neither; wiring it into a build loop would stop runs over something no tool
can resolve. It belongs in 'ape doctor' and nowhere else.`,
		Args:    cobra.NoArgs,
		Example: "  ape sprint check --output-format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := resolveProjectConfig(cwdFlag)
			report, err := sprint.RunCheck(cfg)
			if err != nil {
				return err
			}
			format := output.Format(outputFormat)
			if format != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), format, report)
			}
			emitSprintCheckHuman(cmd.OutOrStdout(), report)
			return nil
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	return cmd
}

func emitSprintCheckHuman(w io.Writer, report *sprint.CheckReport) {
	s := report.Summary
	fmt.Fprintf(w, "%d story row(s), %d on disk (%d epic, %d retrospective, %d other row(s) classified out)\n",
		s.StoryRows, s.StoriesOnDisk, s.EpicRows, s.RetroRows, s.OtherRows)
	if report.OK() {
		fmt.Fprintln(w, "no divergence")
		return
	}
	fmt.Fprintf(w, "\n%d divergence(s) — neither side is assumed correct:\n", len(report.Findings))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  CHECK\tKEY\tTRACKER\tSTORY")
	for i := range report.Findings {
		f := &report.Findings[i]
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", f.Check, f.Key, dashIfEmpty(f.Tracker), dashIfEmpty(f.Story))
	}
	_ = tw.Flush()
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func newSprintVerifyCmd() *cobra.Command {
	var (
		fileFlag     string
		keyFlag      string
		expectedFlag string
		outputFormat string
	)
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify one tracker row landed as written",
		Long: `Re-read the tracker from disk and confirm the write landed: the row
equals --expected, updated_at does not precede created_at, and updated_at
is not earlier than the value in the last committed revision.

That last comparison is the one that matters. A row write can silently
fail to land, and comparing updated_at against created_at alone passes
trivially because both are written in the same operation.

Exit codes, preserved exactly from verify-sprint-status-row.py because two
review skills branch on them:
  0  the row matches and the timestamps are ordered
  2  file unreadable or YAML malformed
  3  key missing from development_status, or its value differs
  4  updated_at precedes created_at (a corrupt write)
  5  updated_at is earlier than the last committed value (a backwards
     write). Deterministically repairable: re-write the field as the
     reported clamp value or later, re-run, and report the clamp. NEVER a
     reason to stop the run.

Exit 1 is deliberately unreachable. In the Python it meant "PyYAML is not
installed" — an environment failure that forced apex-review-story and
apex-code-review to carry an eye-check fallback. A static binary cannot
produce it, which is what lets those fallback branches be deleted.

This command carries no --cwd. --file is required and names the tracker
outright, so nothing is resolved from the project's config and there is no
project for --cwd to select.`,
		Args: cobra.NoArgs,
		Example: "  ape sprint verify --file development/implementation/sprint-status.yaml " +
			"--key 1-1 --expected done",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if fileFlag == "" || keyFlag == "" || expectedFlag == "" {
				return usageErr(errors.New("--file, --key and --expected are all required"))
			}
			res := sprint.VerifyRow(cmd.Context(), fileFlag, keyFlag, expectedFlag)
			format := output.Format(outputFormat)
			switch {
			case format != output.FormatHuman:
				if err := output.Print(cmd.OutOrStdout(), format, res); err != nil {
					return err
				}
			case res.Code == sprint.VerifyOK:
				fmt.Fprintf(cmd.OutOrStdout(), "OK: %s\n", res.Message)
			default:
				fmt.Fprintf(cmd.ErrOrStderr(), "FAIL(%d): %s\n", res.Code, res.Message)
			}
			return gateErr(res.Code, nil)
		},
	}
	cmd.Flags().StringVar(&fileFlag, "file", "", "Path to sprint-status.yaml (required)")
	cmd.Flags().StringVar(&keyFlag, "key", "", "development_status row key (required)")
	cmd.Flags().StringVar(&expectedFlag, "expected", "", "Status the row must equal (required)")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	return cmd
}

func newSprintReconcileCmd() *cobra.Command {
	var (
		cwdFlag      string
		fileFlag     string
		outputFormat string
		epic         int
		all          bool
		check        bool
	)
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Project epic rows from their story rows",
		Long: `An epic's status is not a fact any skill asserts — it is a projection of
the story rows beneath it. This is the single implementation of that
projection, tracker-only, with no story-file reads:

  rows   = development_status keys matching ^{N}-\d+[-_]
  active = rows whose status is not 'cancelled'

  no rows              -> leave unchanged (never close an epic with none)
  no active rows       -> leave unchanged (all-cancelled is a scope call)
  all active 'done'    -> done
  all active 'backlog' -> backlog
  otherwise            -> in-progress

'blocked' lands in the final clause, so a blocked story holds its epic
open. An unrecognised status can only ever hold an epic open, never close
it, and is named in the output rather than swallowed.

The write is TARGETED: only the matched epic-N line and the body
updated_at change. Comments, key order, story rows and the sync-generated
header are untouched. updated_at moves only on mutation and never
backwards — it is clamped, reported, and never fatal.

The read-modify-write takes an exclusive lock on a sidecar file, because
concurrent per-epic sub-agents reconcile the same tracker and the last
writer would otherwise silently drop a sibling's update.

Exit 0 for every content outcome, including an unrecognised status.
Non-zero only for a genuine I/O failure.`,
		Args:    cobra.NoArgs,
		Example: "  ape sprint reconcile --epic 12\n  ape sprint reconcile --all --check",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The timestamp is resolved whether or not --file was passed.
			// Refreshing the body updated_at on mutation is part of what
			// reconcile IS, so it must not depend on how the tracker was
			// located: `--file` is how the framework's call sites name the
			// tracker, and a --file run that quietly stopped stamping would
			// leave every reconciled tracker claiming it had not changed
			// since its last full sync.
			//
			// tryResolve, not resolve: `--file` has to keep working against
			// a tracker outside any project, exactly as the script it
			// replaces does. Absent a config, the clock is the fallback —
			// local wall-clock either way, per D1.
			cfg := tryResolveProjectConfig(cwdFlag)
			timestamp := ""
			path := fileFlag
			if cfg != nil {
				timestamp = cfg.Timestamp
				if path == "" {
					path = cfg.Paths.SprintStatus
				}
			}
			if timestamp == "" {
				timestamp = time.Now().Format(apexcfg.TimestampLayout)
			}
			if path == "" {
				return usageErr(errors.New("no sprint-status.yaml resolved; pass --file"))
			}
			res, err := sprint.Reconcile(path, sprint.ReconcileOptions{
				Epic: epic, All: all, Timestamp: timestamp, Check: check,
			})
			if err != nil {
				return err
			}
			format := output.Format(outputFormat)
			if format != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), format, res)
			}
			emitReconcileHuman(cmd.OutOrStdout(), res, check)
			return nil
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&fileFlag, "file", "", "Tracker path (default: resolved from config)")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	cmd.Flags().IntVar(&epic, "epic", 0, "Reconcile one epic by number")
	cmd.Flags().BoolVar(&all, "all", false, "Reconcile every epic with rows")
	cmd.Flags().BoolVar(&check, "check", false, "Report the projection without writing")
	return cmd
}

// emitReconcileHuman prints one line per epic, matching the single-line
// output shape reconcile-epic-status.py produced so the calling skills'
// "record the script's output line" instruction still works.
func emitReconcileHuman(w io.Writer, res *sprint.ReconcileResult, check bool) {
	for _, c := range res.Changes {
		verb := "reconciled"
		if check {
			verb = "would reconcile"
		}
		fmt.Fprintf(w, "%s %s: %s -> %s (%s)\n", verb, c.Key, c.From, c.To, c.Reason)
	}
	for _, s := range res.Unchanged {
		fmt.Fprintf(w, "epic-%d unchanged: %s\n", s.Epic, s.Reason)
	}
	if len(res.Unrecognised) > 0 {
		fmt.Fprintf(w, "unrecognised status value(s), which can only hold an epic open: %v\n",
			res.Unrecognised)
	}
	if len(res.BareRowKeys) > 0 {
		// Stderr would be the tidier home, but the calling skills are
		// instructed to record "the command's output line", and a warning a
		// skill does not read is a warning that does not exist.
		fmt.Fprintf(w, "warning: %d story row(s) keyed as N-M with no slug — counted here, "+
			"NOT counted by the reconcile-epic-status.py this replaces, so the two disagree "+
			"on these epics: %v\n", len(res.BareRowKeys), res.BareRowKeys)
		fmt.Fprintf(w, "         fix: %s\n", sprint.BareRowKeyRemediation)
	}
	if res.Clamped {
		fmt.Fprintf(w, "updated_at clamped to %s (the supplied timestamp was earlier)\n", res.UpdatedAt)
	}
	if !res.Changed() {
		fmt.Fprintln(w, "no epic row needed to move")
	}
}
