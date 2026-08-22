package apecmd

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/exoport/apex_process_ape/internal/memory"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/spf13/cobra"
)

// failAt values for `ape memory check`.
const (
	failAtNever = "never"
	failAtSoft  = "soft"
	failAtHard  = "hard"
)

func newMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Read the team-memory file without loading it whole",
		Long: `team-memory.md outgrew whole-file reading: at 431,950 bytes on the
reference project a Read fails outright ("exceeds maximum allowed size
(256KB)") — including for the retrospective that is instructed to re-read
it before editing it.

  index  what is in there: ordinal, section, date, size, title
  show   the verbatim body of named entries
  check  size against two budgets, from a stat alone`,
	}
	cmd.AddCommand(newMemoryIndexCmd(), newMemoryShowCmd(), newMemoryCheckCmd())
	return cmd
}

func newMemoryIndexCmd() *cobra.Command {
	var (
		cwdFlag      string
		outputFormat string
	)
	cmd := &cobra.Command{
		Use:   "index",
		Short: "List every team-memory entry",
		Long: `One line per '### ' entry: ordinal, section, date, byte size, title.

Structurally lossless, and carrying no filter, ranking or predicate —
deliberately. Most call sites sit inside '## On Activation', which runs
BEFORE the story is identified, so no predicate keyed on "this story's
domain" could work there. Selection has to be possible from ordinal,
section, date, size and title alone.

A '### ' line inside a fenced code block is content, not an entry. That
matters more than it sounds: counting one would shift every ordinal after
it, and 'show <n>' would then hand back the wrong entry with nothing to
signal the mistake.`,
		Args:    cobra.NoArgs,
		Example: "  ape memory index --output-format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := resolveProjectConfig(cwdFlag)
			idx, err := memory.Load(cfg.Paths.TeamMemory)
			if err != nil {
				return err
			}
			format := output.Format(outputFormat)
			if format != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), format, idx)
			}
			return emitMemoryIndexHuman(cmd.OutOrStdout(), idx)
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	return cmd
}

func emitMemoryIndexHuman(w io.Writer, idx *memory.Index) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tSECTION\tDATE\tBYTES\tTITLE")
	for _, e := range idx.Entries {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\n", e.Ordinal, e.Section, e.Date, e.Bytes, e.Title)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "\n%d entr%s · %d B in entries, %d B in headings, %d B other, %d B total\n",
		idx.Count, pluralY(idx.Count), idx.EntryBytes, idx.HeadingBytes, idx.OtherBytes, idx.TotalBytes)
	return nil
}

func newMemoryShowCmd() *cobra.Command {
	var cwdFlag string
	cmd := &cobra.Command{
		Use:   "show <n>[,<n>,...]",
		Short: "Print the verbatim body of the named entries",
		Long: `Print entries by ordinal, in the order asked for, byte-for-byte as they
appear in the file.

Exit codes:
  0  printed
  2  an ordinal the file does not have`,
		Args:    cobra.ExactArgs(1),
		Example: "  ape memory show 3,7,12",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := resolveProjectConfig(cwdFlag)
			ordinals, err := memory.ParseOrdinals(args[0])
			if err != nil {
				return usageErr(err)
			}
			bodies, err := memory.Bodies(cfg.Paths.TeamMemory, ordinals)
			if err != nil {
				return gateErr(exitCodeMemoryOutOfRange, err)
			}
			for _, b := range bodies {
				fmt.Fprint(cmd.OutOrStdout(), b)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	return cmd
}

// exitCodeMemoryOutOfRange is `ape memory show`'s only failure: an
// ordinal that does not exist. Command-local, like the config codes.
const exitCodeMemoryOutOfRange = 2

func newMemoryCheckCmd() *cobra.Command {
	var (
		cwdFlag      string
		outputFormat string
		soft         int64
		hard         int64
		failAt       string
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Report team-memory size against the soft budget and hard ceiling",
		Long: `Classify team-memory.md against two budgets, from an os.Stat alone —
the file is never read, which is what makes this cheap enough to run on
every retrospective.

  state: absent      no team-memory.md yet (a fresh project, not a problem)
  state: ok          under the soft budget
  state: over-soft   compaction is due; schedule it at the next epic close
  state: over-hard   approaching Claude Code's 256 KiB Read cap — the file
                     is about to become unreadable by its own writer

EXIT 0 BY DEFAULT, whatever the state. The verdict is the 'state' field,
not the exit code, and that is deliberate: the framework's prose
convention is "on non-zero exit: HALT", so a failing exit here would abort
the retrospective at exactly the moment compaction is most needed — the
gate would break the ceremony it exists to trigger.

--fail-at opts into a non-zero exit for CI, which wants one:
  never  (default) always exit 0
  soft   exit 1 at over-soft or worse
  hard   exit 1 at over-hard

'ape doctor' maps the state to WARN/FAIL itself, so a hard-ceiling breach
is still visible and non-ignorable without this flag.

The token count in the output is bytes/4, an estimate, and nothing gates
on it.`,
		Args:    cobra.NoArgs,
		Example: "  ape memory check\n  ape memory check --fail-at hard",
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch failAt {
			case failAtNever, failAtSoft, failAtHard:
			default:
				return usageErr(fmt.Errorf("--fail-at must be never|soft|hard, got %q", failAt))
			}
			cfg := resolveProjectConfig(cwdFlag)
			check := memory.CheckSize(cfg.Paths.TeamMemory, soft, hard)
			format := output.Format(outputFormat)
			if format != output.FormatHuman {
				if err := output.Print(cmd.OutOrStdout(), format, check); err != nil {
					return err
				}
			} else {
				emitMemoryCheckHuman(cmd.OutOrStdout(), check)
			}
			if memoryCheckShouldFail(check.State, failAt) {
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

// memoryCheckShouldFail applies the --fail-at policy.
func memoryCheckShouldFail(state memory.State, failAt string) bool {
	switch failAt {
	case failAtSoft:
		return state == memory.StateOverSoft || state == memory.StateOverHard
	case failAtHard:
		return state == memory.StateOverHard
	default:
		return false
	}
}

func emitMemoryCheckHuman(w io.Writer, c memory.Check) {
	if !c.Exists {
		fmt.Fprintf(w, "memory: absent (%s)\n", c.Path)
		return
	}
	fmt.Fprintf(w, "memory: %s / %s — %s (~%s tokens, estimated)\n",
		humanBytes(c.Bytes), humanBytes(c.SoftBudget), c.State, thousands(c.EstimatedTokens))
	switch c.State {
	case memory.StateOverSoft:
		fmt.Fprintln(w, "compaction is due — schedule apex-distillator at the next epic close")
	case memory.StateOverHard:
		fmt.Fprintf(w,
			"OVER THE HARD CEILING (%s): the soft gate should have caught this several runs ago.\n"+
				"Compact now, and treat the miss as a bug report rather than routine.\n",
			humanBytes(c.HardCeiling))
	case memory.StateAbsent, memory.StateOK:
	}
}

// thousands groups an integer with thin separators for readability.
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var out strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		out.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if out.Len() > 0 {
			out.WriteByte(',')
		}
		out.WriteString(s[i : i+3])
	}
	return out.String()
}
