package apecmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/stamp"
	"github.com/spf13/cobra"
)

// Exit codes for `ape config resolve`. Command-local, in the style
// `ape framework` (3–7) and `ape bootstrap` (2–4) already use — NOT the
// shared table in exitcodes.go, where 4 means "claude exited before the
// Stop hook fired". That table governs the run commands; a project-data
// command that never spawns claude cannot produce its codes and must not
// borrow its meanings.
const (
	exitCodeConfigMalformed = 2
	exitCodeConfigNotFound  = 4
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Resolve the project's APEX configuration",
	}
	cmd.AddCommand(newConfigResolveCmd())
	return cmd
}

func newConfigResolveCmd() *cobra.Command {
	var (
		cwdFlag      string
		outputFormat string
	)
	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Resolve _apex/config.yaml + the config.local.yaml overlay",
		Long: `Walk up from --cwd for _apex/config.yaml, overlay _apex/config.local.yaml
key-wise, and emit the seventeen folder/name variables every framework
skill resolves on activation — plus the four derived ext_* flags, the
absolute paths those folders denote, and the local date/timestamp.

An absent config.local.yaml is a normal outcome (local_overlay_applied:
false). A config.local.yaml that exists but does not parse is a hard
failure, not a fall-back to base values: this resolution is the first act
of every skill, so one typo'd override would otherwise run a whole
pipeline against folders nobody chose.

governance_repository_path names the CANONICAL governance repository that
apex-adr-reconciliation / apex-pattern-reconciliation import from. It is
not an alternative home for this project's own records — those always
live under governance_folder, which is what paths.adrs and paths.patterns
report.

Exit codes:
  0  resolved
  2  a config file exists but is malformed (the message names it)
  4  no _apex/config.yaml in --cwd or any parent`,
		Args:    cobra.NoArgs,
		Example: "  ape config resolve --output-format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			start := cwdFlag
			if start == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("cannot determine working directory: %w", err)
				}
				start = wd
			}
			res, err := apexcfg.Resolve(start, nil)
			if err != nil {
				return handleConfigError(err)
			}
			format := output.Format(outputFormat)
			if format == output.FormatHuman {
				return emitResolvedHuman(cmd.OutOrStdout(), res)
			}
			return output.Print(cmd.OutOrStdout(), format, res)
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", "Project root to resolve from (default: current working dir)")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

// handleConfigError maps a resolution failure onto the command-local exit
// codes. Shared by every command that resolves config, so "no project
// here" and "your override is broken" never blur into one code.
func handleConfigError(err error) error {
	var malformed *apexcfg.MalformedError
	switch {
	case errors.Is(err, apexcfg.ErrNotFound):
		fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
		os.Exit(exitCodeConfigNotFound)
	case errors.As(err, &malformed):
		fmt.Fprintf(os.Stderr, "Error: %s\n", malformed.Error())
		os.Exit(exitCodeConfigMalformed)
	default:
		fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
	}
	return err
}

func emitResolvedHuman(w io.Writer, res *apexcfg.Resolved) error {
	fmt.Fprintf(w, "root:   %s\n", res.Root)
	fmt.Fprintf(w, "config: %s\n", res.ConfigPath)
	if res.LocalOverlayApplied {
		fmt.Fprintf(w, "local:  %s (overlaid: %v)\n", res.LocalPath, res.OverlaidKeys)
	} else {
		fmt.Fprintln(w, "local:  (absent)")
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "project_name\t%s\n", res.ProjectName)
	fmt.Fprintf(tw, "extensions\t%v\n", res.Extensions)
	fmt.Fprintf(tw, "ext_adrs\t%t\n", res.Ext.ADRs)
	fmt.Fprintf(tw, "ext_patterns\t%t\n", res.Ext.Patterns)
	fmt.Fprintf(tw, "ext_capabilities\t%t\n", res.Ext.Capabilities)
	fmt.Fprintf(tw, "ext_features\t%t\n", res.Ext.Features)
	fmt.Fprintf(tw, "date\t%s\n", res.Date)
	fmt.Fprintf(tw, "timestamp\t%s\n", res.Timestamp)
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "paths:")
	pw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range [][2]string{
		{"development", res.Paths.Development},
		{"implementation", res.Paths.Implementation},
		{"planning", res.Paths.Planning},
		{"governance", res.Paths.Governance},
		{"functionality", res.Paths.Functionality},
		{"adrs", res.Paths.ADRs},
		{"patterns", res.Paths.Patterns},
		{"features", res.Paths.Features},
		{"capabilities", res.Paths.Capabilities},
		{"team_memory", res.Paths.TeamMemory},
		{"sprint_status", res.Paths.SprintStatus},
		{"deferred", res.Paths.Deferred},
	} {
		value := row[1]
		if value == "" {
			value = "(not configured)"
		} else if rel, err := filepath.Rel(res.Root, value); err == nil {
			value = rel
		}
		fmt.Fprintf(pw, "  %s\t%s\n", row[0], value)
	}
	return pw.Flush()
}

// resolveProjectConfig is the shared entry point for every command that
// needs the project's paths. cwdFlag may be empty (use the working dir).
// A resolution failure exits with the command-local code rather than
// returning, so no caller can accidentally proceed against zero paths —
// the failure mode PLAN-25 D1 exists to prevent.
func resolveProjectConfig(cwdFlag string) *apexcfg.Resolved {
	start := cwdFlag
	if start == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot determine working directory: %v\n", err)
			os.Exit(exitCodeConfigNotFound)
		}
		start = wd
	}
	res, err := resolveMonotonic(start)
	if err != nil {
		_ = handleConfigError(err)
		os.Exit(exitCodeConfigNotFound)
	}
	return res
}

// resolveMonotonic resolves the project config with ape's monotonic clock
// rather than a bare wall-clock read.
//
// This is the single wiring point for the timestamp guarantee. Every
// project-data command routes through resolveProjectConfig or
// tryResolveProjectConfig, so the `timestamp` that `ape config resolve`
// emits — the value skills copy into `updated_at`, `generated_at`,
// `frozen_at` and their own report headers — is issued by
// internal/stamp and can never move backwards. See that package for why
// the guarantee lives in the binary rather than in ~53 copies of a
// paragraph.
//
// The root is found first because the clock is per-project: the floor is
// persisted under that project's output folder.
func resolveMonotonic(start string) (*apexcfg.Resolved, error) {
	root, err := apexcfg.Find(start)
	if err != nil {
		return nil, err
	}
	return apexcfg.ResolveAt(root, stamp.New(root, nil).Clock())
}

// tryResolveProjectConfig is resolveProjectConfig for callers that must
// degrade instead of exiting — the legacy `list` commands, which have
// always been a soft no-op outside a project.
func tryResolveProjectConfig(cwdFlag string) *apexcfg.Resolved {
	start := cwdFlag
	if start == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil
		}
		start = wd
	}
	res, err := resolveMonotonic(start)
	if err != nil {
		return nil
	}
	return res
}
