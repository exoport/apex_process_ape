package apecmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/sessions"
	"github.com/spf13/cobra"
)

// sessionsListResult is the structured payload for `ape sessions` in
// JSON/YAML mode.
type sessionsListResult struct {
	Sessions []sessions.Session `json:"sessions" yaml:"sessions"`
}

// sessionsPruneResult is the structured payload for `ape sessions prune`.
type sessionsPruneResult struct {
	Live int `json:"live" yaml:"live"`
}

func newSessionsCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List, prune, or open the URL of live ape sessions",
		Long: `Live ape sessions are tracked in ~/.ape/registry.json, and this
subcommand inspects that registry. Every model session ape launches is a row
while it runs — ape task, ape prompt, ape chat, and the change review --run
and ape script sessions that run through them — with its run id, the skill
ape typed, its depth and parent run (the nesting rule, D62), the ape
process running it (PID) and the claude it spawned (claude_pid in JSON).
ape reads those rows to tell which session a command runs under, so a
pruned row only ever belongs to a process that is gone. Rows an older ape
wrote for ` + "`ape pipeline`" + `'s web mode, retired with APEX v0, carry a port and URL.

  ape sessions               Show one row per live session.
  ape sessions prune         Drop rows whose PID is no longer running.
  ape sessions open [<pfx>]  xdg-open the URL of the live session whose
                             cwd starts with <pfx>. Errors if zero or
                             multiple sessions match.`,
		Example: "  ape sessions\n  ape sessions --output-format json",
		RunE: func(_ *cobra.Command, _ []string) error {
			rows, err := sessions.Prune(sessions.Path())
			if err != nil {
				return err
			}
			return printSessions(rows, output.Format(outputFormat))
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	cmd.AddCommand(newSessionsPruneCmd(), newSessionsOpenCmd())
	return cmd
}

func newSessionsPruneCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use: "prune",
		// Takes none: without this, a stray argument is silently ignored
		// and the command answers 0 to an invocation nobody meant.
		Args:    cobra.NoArgs,
		Short:   "Drop registry rows whose PID is no longer running",
		Example: "  ape sessions prune\n  ape sessions prune --output-format json",
		RunE: func(_ *cobra.Command, _ []string) error {
			rows, err := sessions.Prune(sessions.Path())
			if err != nil {
				return err
			}
			res := sessionsPruneResult{Live: len(rows)}
			switch output.Format(outputFormat) {
			case output.FormatJSON:
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			case output.FormatYAML:
				return output.Print(os.Stdout, output.FormatYAML, res)
			default:
				fmt.Fprintf(os.Stderr, "pruned. %d session(s) live.\n", res.Live)
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

func newSessionsOpenCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "open [<project-prefix>]",
		Short:   "xdg-open the URL of the live session whose cwd matches <project-prefix>",
		Example: "  ape sessions open ~/projects/foo",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := sessions.Prune(sessions.Path())
			if err != nil {
				return err
			}
			prefix := ""
			if len(args) == 1 {
				prefix = args[0]
			}
			var matches []sessions.Session
			for _, r := range rows {
				if prefix == "" || strings.HasPrefix(r.CWD, prefix) {
					matches = append(matches, r)
				}
			}
			if len(matches) == 0 {
				return errors.New("no live sessions match")
			}
			if len(matches) > 1 {
				return fmt.Errorf("%d sessions match — narrow the prefix", len(matches))
			}
			return openBrowser(cmd.Context(), matches[0].URL)
		},
	}
}

func printSessions(rows []sessions.Session, format output.Format) error {
	switch format {
	case output.FormatJSON, output.FormatYAML:
		res := sessionsListResult{Sessions: rows}
		if res.Sessions == nil {
			res.Sessions = []sessions.Session{}
		}
		return output.Print(os.Stdout, format, res)
	default:
		if len(rows) == 0 {
			fmt.Fprintln(os.Stderr, "no live sessions.")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PID\tAGE\tCOMMAND\tRUN\tSKILL\tDEPTH\tPARENT\tURL\tCWD")
		now := time.Now()
		for _, r := range rows {
			fmt.Fprintf(
				tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.PID,
				ageOf(now, r.StartedAt),
				r.Command,
				orDash(r.RunID),
				orDash(r.Skill),
				orDash(depthText(r.Depth)),
				orDash(r.ParentRun),
				orDash(r.URL),
				r.CWD,
			)
		}
		return tw.Flush()
	}
}

func depthText(d int) string {
	if d <= 0 {
		return ""
	}
	return strconv.Itoa(d)
}

func ageOf(now, t time.Time) string {
	d := now.Sub(t)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// suppress unused import warning when exec is only referenced by other files
var _ = exec.Command
