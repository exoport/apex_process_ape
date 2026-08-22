package apecmd

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/exoport/apex_process_ape/internal/apexdoc"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/spf13/cobra"
)

func newDocCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "doc",
		Aliases: []string{"docs"},
		Short:   "Shard, assemble and survey Markdown documents",
		Long: `Markdown document operations.

  verify    refuse-to-shard gate: duplicate heading slugs at a level
  shard     split a document into section files, rewriting relative links
  assemble  concatenate the shards back, reversing the rewrite
  analyze   survey a set of source documents: sizes, groups, routing`,
	}
	cmd.AddCommand(newDocVerifyCmd(), newDocShardCmd(), newDocAssembleCmd(), newDocAnalyzeCmd())
	return cmd
}

func newDocVerifyCmd() *cobra.Command {
	var (
		level  int
		format string
	)
	cmd := &cobra.Command{
		Use:   "verify <file>",
		Short: "Check a document for duplicate heading slugs",
		Long: `Scan a document for two headings at the same level that slugify to the
same value.

This is a GATE, not a report — the one exception to the verify contract,
and for the same reason as 'ape story verify --file': its caller relies on
the non-zero exit to stop before writing anything. Downstream skills
reference shard files by exact slug, so a deduplicated 'foo-2.md' sibling
would silently break them; the source document has to be fixed instead.

Exit codes:
  0  no duplicate slugs
  1  duplicates found (each pair is printed with its line numbers)`,
		Args:    cobra.ExactArgs(1),
		Example: "  ape doc verify development/planning/prd.md --level 2",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := apexdoc.Verify(args[0], level)
			if err != nil {
				return err
			}
			if f := output.Format(format); f != output.FormatHuman {
				if err := output.Print(cmd.OutOrStdout(), f, res); err != nil {
					return err
				}
			} else if res.OK() {
				fmt.Fprintf(cmd.OutOrStdout(), "no duplicate H%d slugs in %s (%d heading(s))\n",
					res.Level, res.File, res.Headings)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "duplicate H%d slugs in %s:\n", res.Level, res.File)
				for _, d := range res.Duplicates {
					fmt.Fprintf(cmd.ErrOrStderr(), "  - slug %q: %q (line %d) and %q (line %d)\n",
						d.Slug, d.First, d.FirstLine, d.Second, d.SecondLine)
				}
			}
			if !res.OK() {
				return gateErr(1, nil)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&level, "level", apexdoc.DefaultLevel, "Heading level to check")
	cmd.Flags().StringVar(&format, "output-format", "human", helpFormat)
	return cmd
}

func newDocShardCmd() *cobra.Command {
	var (
		level    int
		numbered bool
		format   string
	)
	cmd := &cobra.Command{
		Use:   "shard <file> <dir>",
		Short: "Split a document into section files",
		Long: `Split a document at a heading level, one file per section, and rewrite
relative links to account for the extra directory depth.

It ALWAYS writes an index.md listing and linking every section file. That
is a contract, not a convenience: apex-shard-doc treats a missing index.md
as proof the command did not complete, and verifies it in its own step. A
replacement that split and rewrote links perfectly but omitted the index
would pass a round-trip test and then fail its real caller.

It REFUSES when two headings slugify to the same value, rather than writing
'foo-2.md' siblings that hardcoded consumers cannot distinguish. Nothing is
written in that case — fix the source document.

A document with no headings at the requested level becomes index.md whole,
so 'assemble' still has something to work from.`,
		Args:    cobra.ExactArgs(2),
		Example: "  ape doc shard development/planning/prd.md development/planning/prd",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := apexdoc.Shard(args[0], args[1], apexdoc.ShardOptions{
				Level: level, Numbered: numbered,
			})
			if err != nil {
				return err
			}
			if f := output.Format(format); f != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), f, res)
			}
			emitShardHuman(cmd.OutOrStdout(), res)
			return nil
		},
	}
	cmd.Flags().IntVar(&level, "level", apexdoc.DefaultLevel, "Heading level to split at")
	cmd.Flags().BoolVar(&numbered, "numbered", false, "Prefix filenames with a zero-padded ordinal")
	cmd.Flags().StringVar(&format, "output-format", "human", helpFormat)
	return cmd
}

func emitShardHuman(w io.Writer, res *apexdoc.ShardResult) {
	if res.WholeDocument {
		fmt.Fprintf(w, "no H%d headings in %s — wrote the whole document as %s\n",
			res.Level, res.Source, res.Index)
		return
	}
	fmt.Fprintf(w, "sharded %s into %d section(s) in %s/\n",
		res.Source, len(res.Sections), res.Dir)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  %s\t%s\n", apexdoc.IndexFileName, "preamble + section listing")
	for _, sec := range res.Sections {
		fmt.Fprintf(tw, "  %s\t%s\n", sec.FileName, sec.Heading)
	}
	_ = tw.Flush()
}

func newDocAssembleCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "assemble <dir> <file>",
		Short: "Concatenate shards back into one document",
		Long: `Reverse a shard: concatenate the section files in index.md order and
strip the ../ that sharding added.

index.md supplies the ordering and its preamble, and is never itself
concatenated as a section. A ../ link that predated the shard survives
untouched, because the strip only applies when the target actually resolves
one level up.

A section file named in the index but missing on disk is reported and
skipped rather than fatal — an incomplete assembly you can see beats none.`,
		Args:    cobra.ExactArgs(2),
		Example: "  ape doc assemble development/planning/prd development/planning/prd.md",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := apexdoc.Assemble(args[0], args[1])
			if err != nil {
				return err
			}
			if f := output.Format(format); f != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), f, res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "assembled %d section(s) from %s/ into %s\n",
				res.Sections, res.Dir, res.Output)
			for _, missing := range res.Missing {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: section file named in the index is missing: %s\n", missing)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "output-format", "human", helpFormat)
	return cmd
}

func newDocAnalyzeCmd() *cobra.Command {
	var (
		format          string
		singleMaxFiles  int
		singleMaxTokens int64
		splitMinTokens  int64
	)
	cmd := &cobra.Command{
		Use:   "analyze <path|dir|glob>...",
		Short: "Survey source documents: sizes, groups, routing",
		Long: `Enumerate source documents and report sizes, estimated tokens, detected
types, suggested groupings, a routing recommendation and a split
prediction.

Inputs may be file paths, directories (walked for .md/.txt/.yaml/.yml/
.json) or glob patterns. node_modules, .git, __pycache__, .venv, .claude,
.cursor and .vscode are never source documents.

Routing is 'single' when the corpus is BOTH at or under the file limit and
at or under the token limit; otherwise 'fan-out'. The split prediction
estimates a distillate at a third of its sources.

Every token number here is bytes/4 — an estimate, labelled as one, and
nothing gates on it. The three thresholds are flags so the boundaries are
testable without building a 15k-token fixture; the defaults are the ones
the framework has always used.`,
		Args:    cobra.MinimumNArgs(1),
		Example: "  ape doc analyze _output/handoffs --output-format json",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := apexdoc.Analyze(args, apexdoc.AnalyzeOptions{
				SingleMaxFiles:  singleMaxFiles,
				SingleMaxTokens: singleMaxTokens,
				SplitMinTokens:  splitMinTokens,
			})
			if err != nil {
				return err
			}
			if f := output.Format(format); f != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), f, res)
			}
			emitAnalyzeHuman(cmd.OutOrStdout(), res)
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "output-format", "human", helpFormat)
	cmd.Flags().IntVar(&singleMaxFiles, "single-max-files", 0,
		fmt.Sprintf("File count at or below which routing is single (default %d)", apexdoc.DefaultSingleMaxFiles))
	cmd.Flags().Int64Var(&singleMaxTokens, "single-max-tokens", 0,
		fmt.Sprintf("Estimated tokens at or below which routing is single (default %d)", apexdoc.DefaultSingleMaxTokens))
	cmd.Flags().Int64Var(&splitMinTokens, "split-min-tokens", 0,
		fmt.Sprintf("Estimated distillate tokens above which a split is likely (default %d)", apexdoc.DefaultSplitMinTokens))
	return cmd
}

func emitAnalyzeHuman(w io.Writer, res *apexdoc.AnalyzeResult) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "FILE\tTYPE\tBYTES\t~TOKENS")
	for _, f := range res.Files {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\n", f.Path, f.DocType, f.SizeBytes, f.EstimatedTokens)
	}
	_ = tw.Flush()
	fmt.Fprintf(w, "\n%d file(s), %d B, ~%s estimated tokens\n",
		res.Summary.TotalFiles, res.Summary.TotalSizeBytes,
		thousands(res.Summary.TotalEstimatedTokens))
	if len(res.Groups) > 0 {
		fmt.Fprintln(w, "\ngroups:")
		for _, g := range res.Groups {
			fmt.Fprintf(w, "  %s\n", g.Key)
			for _, m := range g.Files {
				fmt.Fprintf(w, "    %-11s %s\n", m.Role, m.Path)
			}
		}
	}
	fmt.Fprintf(w, "\nrouting: %s — %s\n", res.Routing.Recommendation, res.Routing.Reason)
	fmt.Fprintf(w, "split:   %s — %s\n", res.SplitPrediction.Prediction, res.SplitPrediction.Reason)
}
