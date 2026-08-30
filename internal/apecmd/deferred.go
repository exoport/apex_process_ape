package apecmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/exoport/apex_process_ape/internal/deferred"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/sprint"
	"github.com/spf13/cobra"
)

func newDeferredCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deferred",
		Short: "The deferred-work record store",
		Long: `One file per deferred record under {development_folder}/deferred/, with
YAML frontmatter and a verbatim Markdown body.

This replaces a single 456,144-byte deferred-work.md whose only eviction
mechanism was deletion — git shows 524 records removed in one commit — and
which had grown too large for the skills that append to it to read.

One file per record is not a style choice: the single-store alternative
returns ZERO records when handed one malformed entry, while this shape
loses exactly the one bad file.

The store sits OUTSIDE {implementation_folder} deliberately. Ten skills
glob {implementation_folder}/**/*.md across 17 sites, and one record file
per deferred item under that folder would feed every one of them.`,
	}
	cmd.AddCommand(
		newDeferredIngestCmd(),
		newDeferredListCmd(),
		newDeferredCloseCmd(),
		newDeferredVerifyCmd(),
		newDeferredMigrateCmd(),
		newDeferredRepairCmd(),
	)
	return cmd
}

// storeFor resolves the project's deferred store, plus the two pieces of
// resolved config its operations need: the project root (for anchor
// resolution) and today's local date (for record ids and discharge
// stamps).
func storeFor(cwdFlag string) (store *deferred.Store, projectRoot, date string) {
	cfg := resolveProjectConfig(cwdFlag)
	return deferred.New(cfg.Paths.Deferred), cfg.Root, cfg.Date
}

func newDeferredIngestCmd() *cobra.Command {
	var (
		cwdFlag  string
		storyKey string
		skill    string
		cycle    int
		bodyFile string
		format   string
	)
	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Store defer bullets as records (bullets on stdin)",
		Long: `Read defer bullets and write one record per bullet.

Input comes from --body-file or stdin, NEVER from an argv string: 108 of
109 real bodies contain backticks, which shell-expand inside an argument.
--body-file is the form the framework calls, because
apex-review-story/steps/step-04-present.md:13 restricts shell constructs
inside code blocks and a plain path argument sidesteps the question
entirely. The skill writes the bullets with the Write tool (never a
heredoc) and passes the path.

EXIT-CODE CONTRACT — this command is reachable from apex-review-story's
emit path, where a non-zero exit converts a defer into a patch, raises
unfixed_patches, and DEMOTES THE STORY to in-progress. Therefore:

  - a bullet whose shape is unrecognised is stored verbatim as free-form,
    with a warning on stderr;
  - empty input is a rc-0 no-op;
  - only a genuine setup failure (an unwritable store) fails.

Nothing about the CONTENT of a defer can make this command exit non-zero.`,
		Args: cobra.NoArgs,
		Example: "  ape deferred ingest --story 54-1 --skill apex-review-story " +
			"--body-file /tmp/defer-54-1.txt",
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, _, date := storeFor(cwdFlag)
			data, err := readDeferredInput(cmd.InOrStdin(), bodyFile)
			if err != nil {
				// Reading the input is setup, not content.
				return usageErr(err)
			}
			res, err := store.Ingest(data, deferred.IngestOptions{
				Story: storyKey, Skill: skill, Cycle: cycle, Date: date,
			})
			if err != nil {
				return err
			}
			for _, w := range res.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			if f := output.Format(format); f != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), f, res)
			}
			if res.Count == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no defer bullets on input — nothing stored")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "stored %d record(s):\n", res.Count)
			for i := range res.Records {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s  %s\n", res.Records[i].ID, res.Records[i].Title)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&storyKey, "story", "", "Story key this defer was filed from")
	cmd.Flags().StringVar(&skill, "skill", "", "Skill that filed it (e.g. apex-review-story)")
	cmd.Flags().IntVar(&cycle, "cycle", 0, "Review cycle number")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "File holding the bullets (default: stdin)")
	cmd.Flags().StringVar(&format, "output-format", "human", helpFormat)
	return cmd
}

func readDeferredInput(stdin io.Reader, bodyFile string) ([]byte, error) {
	if bodyFile == "" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read bullets from stdin: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(bodyFile)
	if err != nil {
		return nil, fmt.Errorf("read --body-file: %w", err)
	}
	return data, nil
}

func newDeferredListCmd() *cobra.Command {
	var (
		cwdFlag string
		format  string
		detail  string
		filter  deferred.Filter
	)
	cmd := &cobra.Command{
		Use:   cmdUseList,
		Short: "List deferred records (open by default)",
		Long: `Project the record set. Open records only unless --status says otherwise:
closed records stay on disk but leave the working set, which is what stops
an LLM re-filing work it already did.

--detail picks how much the human rendering shows; --output-format picks
the encoding. They are separate axes.`,
		Args: cobra.NoArgs,
		Example: "  ape deferred list --owner platform\n" +
			"  ape deferred list --status all --output-format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, _, _ := storeFor(cwdFlag)
			res, err := store.Load(deferred.LoadOptions{IncludeClosed: includeClosed(filter.Status)})
			if err != nil {
				return err
			}
			for _, w := range res.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			selected := deferred.Select(res.Records, filter)
			if f := output.Format(format); f != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), f, selected)
			}
			emitDeferredList(cmd.OutOrStdout(), selected, detail)
			return nil
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&format, "output-format", "human", helpFormat)
	cmd.Flags().StringVar(&detail, "detail", "brief", "Human rendering detail: brief|full")
	cmd.Flags().StringVar(&filter.Status, "status", "open", "Which records: open|closed|all")
	cmd.Flags().StringVar(&filter.Owner, "owner", "", "Only records with this owner")
	cmd.Flags().StringVar(&filter.Story, "story", "", "Only records filed from this story")
	cmd.Flags().StringVar(&filter.Path, "path", "", "Only records anchored under this path prefix")
	cmd.Flags().StringVar(&filter.Group, "group", "", "Only records in this group")
	return cmd
}

func includeClosed(status string) bool {
	switch status {
	case "closed", "all", deferred.StatusDiscarded:
		return true
	default:
		return false
	}
}

func emitDeferredList(w io.Writer, records []deferred.Record, detail string) {
	if len(records) == 0 {
		fmt.Fprintln(w, "no matching deferred records")
		return
	}
	if detail == "full" {
		for i := range records {
			rec := &records[i]
			fmt.Fprintf(w, "%s  [%s]  %s\n", rec.ID, rec.Status, rec.Title)
			if rec.Owner != "" || rec.Trigger != "" {
				fmt.Fprintf(w, "    owner: %s   trigger: %s\n",
					dashIfEmpty(rec.Owner), dashIfEmpty(rec.Trigger))
			}
			if len(rec.Anchors) > 0 {
				fmt.Fprintf(w, "    anchors: %v\n", rec.Anchors)
			}
			fmt.Fprintf(w, "    %s\n\n", rec.Path)
		}
		fmt.Fprintf(w, "%d record(s)\n", len(records))
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tOWNER\tTITLE")
	for i := range records {
		rec := &records[i]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", rec.ID, rec.Status, dashIfEmpty(rec.Owner), rec.Title)
	}
	_ = tw.Flush()
	fmt.Fprintf(w, "\n%d record(s)\n", len(records))
}

func newDeferredCloseCmd() *cobra.Command {
	var (
		cwdFlag string
		by      string
		format  string
	)
	cmd := &cobra.Command{
		Use:   "close <id>",
		Short: "Discharge a record, moving it to closed/",
		Long: `Mark a record closed and move it to closed/. The record is NEVER deleted:
deletion is what destroyed the audit trail the first time, and an LLM that
cannot see a closed defer re-files it.

The body gains a discharge marker in the shape apex-pattern-reconciliation
established, and keeps everything it already said.

Closing is judgment — it requires re-verifying the record's premises
against HEAD — so this is invoked by a person or a skill, never
automatically. 'ape deferred verify' flags CANDIDATES and never closes one.`,
		Args:    cobra.ExactArgs(1),
		Example: `  ape deferred close DW-20260822-a1b2c3 --by "54-2"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if by == "" {
				return usageErr(errors.New("--by is required: record what discharged this"))
			}
			store, _, date := storeFor(cwdFlag)
			rec, err := store.Close(args[0], by, date)
			if err != nil {
				return err
			}
			if f := output.Format(format); f != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), f, rec)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "closed %s (%s) — moved to %s\n", rec.ID, by, rec.Path)
			return nil
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&by, "by", "", "Story key or reason that discharged it (required)")
	cmd.Flags().StringVar(&format, "output-format", "human", helpFormat)
	return cmd
}

func newDeferredVerifyCmd() *cobra.Command {
	var (
		cwdFlag string
		format  string
		strict  bool
	)
	cmd := &cobra.Command{
		Use:     "verify",
		Aliases: []string{"lint"},
		Short:   "Check the store: invariants, and candidates for a human",
		Long: `Two kinds of finding, and the tag on each is the important part:

  confidence: certain    a fact. Schema problems, and related[]/supersedes[]
                         pointing at records that do not exist.
  confidence: candidate  a heuristic, NEVER auto-actionable. A dead anchor
                         (the record may be moot, or the code may just have
                         moved), a trigger naming a story that is now done
                         (the closing condition MAY have fired), a
                         near-duplicate title, a free-form record.

Nothing here ever closes a record. Closing requires re-verifying the
premises against HEAD, which is judgment — and on the reference ledger an
unbiased sample of 20 records found 5 already delivered, including its own
flagship entry, whose defect had been eliminated four weeks earlier.

Exit 0 even with findings; --strict makes it 1.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, root, _ := storeFor(cwdFlag)
			report, err := store.Verify(deferred.VerifyOptions{
				ProjectRoot: root,
				DoneStories: doneStories(cwdFlag),
			})
			if err != nil {
				return err
			}
			if f := output.Format(format); f != output.FormatHuman {
				if err := output.Print(cmd.OutOrStdout(), f, report); err != nil {
					return err
				}
			} else {
				emitDeferredVerify(cmd.OutOrStdout(), report)
			}
			if strict && !report.OK() {
				return gateErr(1, nil)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&format, "output-format", "human", helpFormat)
	cmd.Flags().BoolVar(&strict, "strict", false, helpStrict)
	return cmd
}

// doneStories reads the tracker for the fired-trigger heuristic. Best
// effort: no tracker simply means that heuristic does not run.
func doneStories(cwdFlag string) map[string]bool {
	cfg := tryResolveProjectConfig(cwdFlag)
	if cfg == nil || cfg.Paths.SprintStatus == "" {
		return nil
	}
	tracker, err := sprint.Load(cfg.Paths.SprintStatus)
	if err != nil || tracker.Missing {
		return nil
	}
	out := map[string]bool{}
	for _, row := range tracker.Rows {
		if row.Kind == sprint.KindStory && row.Status == sprint.StatusDone {
			out[row.Key] = true
		}
	}
	return out
}

func emitDeferredVerify(w io.Writer, report *deferred.VerifyReport) {
	s := report.Summary
	fmt.Fprintf(w, "%d record(s): %d open, %d closed\n", s.Records, s.Open, s.Closed)
	if report.OK() {
		fmt.Fprintln(w, "no findings")
		return
	}
	fmt.Fprintf(w, "\n%d finding(s), %d of them candidates for a human:\n", s.Findings, s.Candidates)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, f := range report.Findings {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", f.Confidence, f.Check, dashIfEmpty(f.ID), f.Message)
	}
	_ = tw.Flush()
}
