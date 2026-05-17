package apecmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/diegosz/apex_process_ape/internal/sessions"
	"github.com/spf13/cobra"
)

func newSessionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List, prune, or open the URL of live ape sessions",
		Long: `Live ape chat / ape pipeline (web mode) invocations are tracked in
~/.ape/registry.json. This subcommand inspects that registry.

  ape sessions               Show one row per live session.
  ape sessions prune         Drop rows whose PID is no longer running.
  ape sessions open [<pfx>]  xdg-open the URL of the live session whose
                             cwd starts with <pfx>. Errors if zero or
                             multiple sessions match.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			rows, err := sessions.Prune(sessions.DefaultPath())
			if err != nil {
				return err
			}
			return printSessions(rows)
		},
	}
	cmd.AddCommand(newSessionsPruneCmd(), newSessionsOpenCmd())
	return cmd
}

func newSessionsPruneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "Drop registry rows whose PID is no longer running",
		RunE: func(_ *cobra.Command, _ []string) error {
			rows, err := sessions.Prune(sessions.DefaultPath())
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "pruned. %d session(s) live.\n", len(rows))
			return nil
		},
	}
}

func newSessionsOpenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open [<project-prefix>]",
		Short: "xdg-open the URL of the live session whose cwd matches <project-prefix>",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			rows, err := sessions.Prune(sessions.DefaultPath())
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
			return openBrowser(matches[0].URL)
		},
	}
}

func printSessions(rows []sessions.Session) error {
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "no live sessions.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PID\tAGE\tPORT\tURL\tCOMMAND\tCWD")
	now := time.Now()
	for _, r := range rows {
		fmt.Fprintf(tw, "%d\t%s\t%d\t%s\t%s\t%s\n",
			r.PID,
			ageOf(now, r.StartedAt),
			r.Port,
			r.URL,
			r.Command,
			r.CWD,
		)
	}
	return tw.Flush()
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
