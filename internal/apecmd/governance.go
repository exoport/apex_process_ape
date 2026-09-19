package apecmd

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/exoport/apex_process_ape/internal/governance"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/spf13/cobra"
)

func newGovernanceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "governance",
		Short: "Answer governance questions about a set of paths",
	}
	cmd.AddCommand(newGovernanceMatchCmd())
	return cmd
}

func newGovernanceMatchCmd() *cobra.Command {
	var (
		pathFlags []string
		pathsFile string
		cwdFlag   string
		format    string
	)
	cmd := &cobra.Command{
		Use:   "match",
		Short: "Which stories own these paths, and what that means for a maintenance change",
		Long: `Answer, for each path, which stories' File Lists claim it and what
follows for the lean maintenance lane.

A story owns a path when its "### File List" names it as a whole path
token. A (planned) or (deferred) entry claims nothing — it records an
intention, not a change — and a mention under "## Tasks" is advisory
and is never read here at all.

The owner's status decides:

  in-progress, review              veto — the path is owned
  the two statuses disagree        veto — fail-closed
  no status on either side         veto — ownership not established
  blocked                          Carries: <story-key> <path>
  backlog, ready-for-dev           Carries: <story-key> <path>
  done, cancelled                  Carries: <story-key> <path>
  no owner                         nothing

The status is read from the story file and its tracker row together,
after normalisation — the tracker writes "drafted" where a file reads
"ready-for-dev" — and where one side is missing the other is used.
More than five finished owners collapse to one "shared <path> (<N>
owners)" row, because a commit message is read by a person.

Paths come from --path (repeatable), --paths-file, or stdin. Never a
comma-separated list: a path may contain a comma, and a prescan lists
many.

This answers the OWNER question only. There is no adrs or patterns
field, not even an empty one: an empty list would read as "nothing
applies", where the truth is that this ape does not answer that.

Always exits 0 when it could answer. The verdict is the veto field, not
the exit code — a skill reading a non-zero exit as a content verdict is
the failure mode the framework's own rules warn about.`,
		Args: cobra.NoArgs,
		Example: `  ape governance match --path internal/repl/pty.go --path docs/reference/cli.md
  ape governance match --paths-file changed.txt --output-format json
  git diff --name-only | ape governance match --paths-file -`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := resolveMatchPaths(pathFlags, pathsFile, cmd.InOrStdin())
			if err != nil {
				return usageErr(err)
			}
			cfg := tryResolveProjectConfig(cwdFlag)
			if cfg == nil {
				return usageErr(errors.New("no _apex/config.yaml in this directory or any parent"))
			}
			report, err := governance.Match(cfg, paths)
			if err != nil {
				return usageErr(err)
			}
			if f := output.Format(format); f != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), f, report)
			}
			printGovernanceMatch(cmd.OutOrStdout(), report)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&pathFlags, "path", nil, "A path to match (repeatable)")
	cmd.Flags().StringVar(&pathsFile, "paths-file", "", `File holding one path per line; "-" reads stdin`)
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&format, "output-format", "human", helpFormat)
	return cmd
}

// resolveMatchPaths collects the paths from the one source given.
//
// Never a comma list, and that is a decision rather than an omission: a
// path may hold a comma, and the caller is usually a prescan with many
// paths — so the separator has to be the newline, which no path holds.
func resolveMatchPaths(pathFlags []string, pathsFile string, stdin io.Reader) ([]string, error) {
	if len(pathFlags) > 0 && pathsFile != "" {
		return nil, errors.New("--path and --paths-file are mutually exclusive: pass the paths once")
	}
	if len(pathFlags) > 0 {
		return pathFlags, nil
	}
	if pathsFile == "" {
		return nil, errors.New("no paths: pass --path (repeatable), --paths-file <file>, " +
			"or --paths-file - to read them from stdin")
	}
	data, err := readTextInput(pathsFile, stdin)
	if err != nil {
		return nil, err
	}
	var paths []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if p := strings.TrimSpace(line); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil, errors.New("no paths in the input")
	}
	return paths, nil
}

// printGovernanceMatch is the human rendering: the verdict first, then
// who owns what and why.
func printGovernanceMatch(w io.Writer, r *governance.Report) {
	if r.Veto {
		fmt.Fprintln(w, "VETO — an in-flight story owns one of these paths, so this is not lane work")
	} else {
		fmt.Fprintln(w, "clear — no in-flight story owns these paths")
	}
	for i := range r.Paths {
		p := &r.Paths[i]
		if len(p.Owners) == 0 {
			fmt.Fprintf(w, "\n%s\n  no owner\n", p.Path)
			continue
		}
		fmt.Fprintf(w, "\n%s\n", p.Path)
		for j := range p.Owners {
			o := &p.Owners[j]
			fmt.Fprintf(w, "  %-8s %s — %s\n", o.Effect, o.Key, o.Reason)
		}
	}
	if len(r.Carries) > 0 {
		fmt.Fprintln(w, "\ntrailers this change earns:")
		for _, row := range r.Carries {
			fmt.Fprintf(w, "  Carries: %s\n", row)
		}
	}
	for _, warning := range r.Warnings {
		fmt.Fprintf(w, "\nwarning: %s\n", warning)
	}
}
