package apecmd

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/story"
	"github.com/spf13/cobra"
)

func newStoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "story",
		Aliases: []string{"stories"},
		Short:   "Project and verify story frontmatter",
	}
	cmd.AddCommand(newStoryFieldsCmd(), newStoryVerifyCmd())
	return cmd
}

func newStoryFieldsCmd() *cobra.Command {
	var (
		cwdFlag      string
		outputFormat string
		selectFlag   string
	)
	cmd := &cobra.Command{
		Use:   "fields",
		Short: "Project named frontmatter keys across every story",
		Long: `Walk the implementation folder, read at most 8 KiB per .md file, stop at
the closing '---', and emit only the named top-level keys plus their
nested blocks. A body is never opened.

A file counts as a story only if it has a story_id — which is what keeps
retrospectives, epic briefs and the deferred-work stub out of the result
without teaching this command about each of them.

The trailer carries files_scanned, stories_matched, per-field presence
counts and bytes_read. Those numbers are the point: "this field is absent
everywhere" and "this field was never looked for" are different answers,
and only the trailer distinguishes them. A field present in zero stories
reports 0 and exits 0.

One unreadable file loses that file and nothing else — it is named in
warnings and the rest are still returned.`,
		Args:    cobra.NoArgs,
		Example: "  ape story fields --select story_id,epic,status,features --output-format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			selected, err := story.ParseSelect(selectFlag)
			if err != nil {
				return usageErr(err)
			}
			cfg := resolveProjectConfig(cwdFlag)
			if cfg.Paths.Implementation == "" {
				return usageErr(errors.New(apexcfg.MsgImplementationFolderUnset))
			}
			res, err := story.Project(cfg.Paths.Implementation, selected)
			if err != nil {
				return err
			}
			for _, w := range res.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			format := output.Format(outputFormat)
			if format != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), format, res)
			}
			return emitStoryFieldsHuman(cmd.OutOrStdout(), res, selected)
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	cmd.Flags().StringVar(&selectFlag, "select", "", "Comma-separated frontmatter keys to emit (required)")
	return cmd
}

func emitStoryFieldsHuman(w io.Writer, res *story.FieldsResult, selected []string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	header := "PATH"
	var headerSb83 strings.Builder
	for _, key := range selected {
		headerSb83.WriteString("\t" + key)
	}
	header += headerSb83.String()
	fmt.Fprintln(tw, header)
	for _, s := range res.Stories {
		line := s.Path
		var lineSb89 strings.Builder
		for _, key := range selected {
			lineSb89.WriteString("\t" + formatFieldValue(s.Values[key]))
		}
		line += lineSb89.String()
		fmt.Fprintln(tw, line)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	t := res.Trailer
	fmt.Fprintf(w, "\n%d file(s) scanned, %d stor%s matched, %d B read\n",
		t.FilesScanned, t.StoriesMatched, pluralY(t.StoriesMatched), t.BytesRead)
	for _, key := range selected {
		fmt.Fprintf(w, "  %s: present in %d\n", key, t.PerFieldPresent[key])
	}
	return nil
}

// formatFieldValue renders a projected value compactly for the human
// table. Nested blocks are summarised by shape rather than dumped — the
// JSON payload is where a caller reads structure.
func formatFieldValue(v any) string {
	switch typed := v.(type) {
	case nil:
		return "-"
	case []any:
		return fmt.Sprintf("[%d]", len(typed))
	case map[string]any:
		return fmt.Sprintf("{%d}", len(typed))
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func newStoryVerifyCmd() *cobra.Command {
	var (
		cwdFlag      string
		outputFormat string
		strict       bool
		fileFlag     string
		activeExts   string
		fix          bool
		fixCheck     bool
	)
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify story frontmatter (corpus report, or a single-file gate)",
		Long: `Three check classes over story frontmatter, and no others:

  1. presence  the four required keys, plus the extension-gated ones
               (governance.adrs, governance.patterns, features,
               capabilities) for whichever ext_* flags are set
  2. type      features items must be objects, not bare strings;
               depends_on items must be strings, not YAML floats
  3. refs      every cited ADR / pattern / feature / capability id
               resolves to a record on disk

No enum checks, no contribution vocabulary, no JSON Schema engine. The
contribution field has no normative source, so coercing it would fabricate
a lifecycle edge — a test asserts this command reports nothing for any
value of it.

TWO MODES, with deliberately different contracts:

  corpus (default)  a REPORT. Exit 0 even with findings; --strict makes it
                    1. --strict must never be set from inside
                    apex-review-story, apex-code-review or
                    apex-epic-batch-review: a non-zero exit on those paths
                    converts a defer into a patch and demotes the story.

  --file <path>     a GATE, replacing verify-story-frontmatter.py with its
                    exit codes intact:
                      0  valid
                      2  parse failure (bad YAML, or no --- delimiters)
                      3  a required or extension-conditional key is absent,
                         or an optional key is present but malformed
                    Referential integrity is not asserted here — a single
                    file cannot see the corpus, exactly as the Python
                    could not.

--fix REPAIRS ONE CLASS AND SAYS SO. A depends_on item that decoded as a
number is re-quoted: same characters, one right answer, no second source
needed. Nothing else is touched, and the findings --fix does not own are
reported as remaining rather than dropped.

The two classes it declines look mechanical and are not. A features item
needs a contribution, whose only source is the prose table inside the
feature record — and where that table has no row for the story, the
disagreement IS the defect. A missing features:/capabilities: key needs a
value, and [] claims the story contributes to nothing, which is a registry
question. Both belong to apex-frontmatter-repair, which may read documents
and judge. Every touched file is re-verified and rolled back if its
finding count did not fall.`,
		Args: cobra.NoArgs,
		Example: "  ape story verify --output-format json\n" +
			"  ape story verify --file development/implementation/1-1_thing.md --active-extensions ext-adrs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if fileFlag != "" {
				if fix {
					return errors.New("--fix is a corpus repair; drop --file")
				}
				return runStoryVerifyFile(cmd, fileFlag, activeExts, outputFormat)
			}
			if fix {
				return runStoryFix(cmd.OutOrStdout(), cwdFlag, outputFormat, fixCheck)
			}
			cfg := resolveProjectConfig(cwdFlag)
			report, err := story.VerifyCorpus(cfg)
			if err != nil {
				return err
			}
			format := output.Format(outputFormat)
			if format != output.FormatHuman {
				if err := output.Print(cmd.OutOrStdout(), format, report); err != nil {
					return err
				}
			} else {
				emitStoryReportHuman(cmd.OutOrStdout(), report)
			}
			if strict && !report.OK() {
				return gateErr(1, nil)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	cmd.Flags().BoolVar(&strict, "strict", false,
		helpStrict+" — NEVER set this from apex-review-story, apex-code-review or apex-epic-batch-review")
	cmd.Flags().StringVar(&fileFlag, "file", "", "Verify one story file as a gate (exit 0/2/3)")
	cmd.Flags().BoolVar(&fix, "fix", false,
		"Repair the findings with a single derivable answer (depends_on quoting), and report the rest")
	cmd.Flags().BoolVar(&fixCheck, "check", false, "With --fix: report the repairs without writing")
	cmd.Flags().StringVar(&activeExts, "active-extensions", "",
		"Comma-separated active extensions for --file mode (e.g. ext-adrs,ext-features)")
	return cmd
}

func runStoryFix(w io.Writer, cwdFlag, outputFormat string, check bool) error {
	cfg := resolveProjectConfig(cwdFlag)
	res, err := story.FixCorpus(cfg, check)
	if err != nil {
		return err
	}
	format := output.Format(outputFormat)
	if format != output.FormatHuman {
		return output.Print(w, format, res)
	}
	if !res.Changed() && len(res.Remaining) == 0 {
		fmt.Fprintln(w, "story frontmatter is clean — nothing to fix")
		return nil
	}
	if res.Changed() {
		verb := "repaired"
		if check {
			verb = "would repair"
		}
		fmt.Fprintf(w, "%s %d story header(s):\n", verb, len(res.Changes))
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, c := range res.Changes {
			fmt.Fprintf(tw, "  %s\t%s\t%s -> %s\n", c.Story, c.Field, c.Before, c.After)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	if len(res.Remaining) == 0 {
		return nil
	}
	// Reported, never dropped: a fixer that prints only its successes reads
	// as having finished.
	byCheck := map[string]int{}
	for _, f := range res.Remaining {
		byCheck[f.Check]++
	}
	fmt.Fprintf(w, "\n%d finding(s) --fix does not own:\n", len(res.Remaining))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, name := range sortedKeys(byCheck) {
		fmt.Fprintf(tw, "  %s\t%d\n", name, byCheck[name])
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(w, "\nThese need a document read or a judgment call — run /apex-frontmatter-repair.")
	return nil
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func runStoryVerifyFile(cmd *cobra.Command, path, activeExts, outputFormat string) error {
	ext := story.ParseActiveExtensions(activeExts)
	verdict := story.VerifyFile(path, ext)
	format := output.Format(outputFormat)
	switch {
	case format != output.FormatHuman:
		if err := output.Print(cmd.OutOrStdout(), format, verdict); err != nil {
			return err
		}
	case verdict.Code == story.FileOK:
		fmt.Fprintf(cmd.OutOrStdout(), "OK: %s frontmatter is valid\n", path)
	default:
		fmt.Fprintf(cmd.ErrOrStderr(), "FAIL: %s\n", path)
		for _, f := range verdict.Findings {
			fmt.Fprintf(cmd.ErrOrStderr(), "  %s %s: %s\n", f.Check, f.Field, f.Message)
		}
	}
	return gateErr(verdict.Code, nil)
}

func emitStoryReportHuman(w io.Writer, report *story.Report) {
	fmt.Fprintf(w, "%d file(s) scanned, %d stor%s checked\n",
		report.Summary.FilesScanned, report.Summary.StoriesChecked,
		pluralY(report.Summary.StoriesChecked))
	if report.OK() {
		fmt.Fprintln(w, "no findings")
		return
	}
	fmt.Fprintf(w, "\n%d finding(s):\n", len(report.Findings))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, f := range report.Findings {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", f.Check, f.Path, f.Field, f.Message)
	}
	_ = tw.Flush()
}
