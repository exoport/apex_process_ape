package apecmd

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/effort"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/spf13/cobra"
)

// loadEffortTable reads the project's `_apex/effort-defaults.yaml`. nil
// with no error means the project has none, and the legacy default applies.
// A project whose config does not resolve has no apex folder to look in,
// which is the same answer.
func loadEffortTable(projectRoot string) (*effort.Defaults, error) {
	cfg, err := apexcfg.ResolveAt(projectRoot, nil)
	if err != nil {
		return nil, nil //nolint:nilerr,nilnil // no resolvable config means no table, not a failure
	}
	return effort.Load(cfg.Paths.Apex)
}

// prepareEffort validates every explicit effort a run could use and loads
// the table, before anything spawns: a typo in a spec's `effort:`, a bad
// --effort, or an invalid table fails the run here, in the same second,
// rather than reaching claude as a value it silently ignores.
func prepareEffort(spec *pipeline.Spec, projectRoot string, cfg *runConfig) error {
	if err := effort.CheckLevel(cfg.effort); err != nil {
		return usageErr(fmt.Errorf("--effort %w", err))
	}
	if errs := spec.EffortErrors(); len(errs) > 0 {
		return usageErr(errors.New("pipeline " + spec.Name + ": " + strings.Join(errs, "; ")))
	}
	table, err := loadEffortTable(projectRoot)
	if err != nil {
		return usageErr(err)
	}
	cfg.effortTable = table
	return nil
}

// effortReport is `ape config effort`'s payload: what a spawn in this
// project would get with no explicit effort. snake_case, like the
// `ape config resolve` payload it sits beside.
//
//nolint:tagliatelle // snake_case is the config command family's contract
type effortReport struct {
	Path    string `json:"path"    yaml:"path"`
	Present bool   `json:"present" yaml:"present"`
	// Mode is "table" when the file governs, "legacy-default" when it is
	// absent and every spawn gets LegacyDefault process-wide.
	Mode     string            `json:"mode"               yaml:"mode"`
	Version  int               `json:"version,omitempty"  yaml:"version,omitempty"`
	Defaults map[string]string `json:"defaults,omitempty" yaml:"defaults,omitempty"`
	Fallback string            `json:"fallback,omitempty" yaml:"fallback,omitempty"`
	// SettingsKeys are the exact modelSettings keys ape writes per family:
	// the family word, which follows the alias, and every id of that
	// family this binary's price table knows.
	SettingsKeys map[string][]string `json:"settings_keys,omitempty" yaml:"settings_keys,omitempty"`
	// LegacyDefault is set only in legacy mode.
	LegacyDefault string `json:"legacy_default,omitempty" yaml:"legacy_default,omitempty"`
	// Model is the --model resolution, when asked for.
	Model *effortForModel `json:"model,omitempty" yaml:"model,omitempty"`
}

type effortForModel struct {
	Requested string `json:"requested" yaml:"requested"`
	Canonical string `json:"canonical" yaml:"canonical"`
	Family    string `json:"family"    yaml:"family"`
	Effort    string `json:"effort"    yaml:"effort"`
	// Via is "family" (its family's row), "fallback" or "legacy-default".
	Via string `json:"via" yaml:"via"`
}

func newConfigEffortCmd() *cobra.Command {
	var cwdFlag, outputFormat, modelFlag string
	cmd := &cobra.Command{
		Use:   "effort",
		Short: "Show the per-model effort defaults a spawn in this project gets",
		Long: `Read _apex/effort-defaults.yaml and report what ape gives a spawned claude
session when no step, stage, pipeline or --effort value is declared.

With the file, every spawn carries the table in its --settings: each model,
sub-agents included, runs at its family's row, and any other model at the
fallback. An explicit effort still wins, process-wide. Without the file,
every spawn gets ` + effort.LegacyDefault + ` process-wide, which is what ape did before
the table existed.

settings_keys are the exact modelSettings keys written: the family word,
which follows the family's current alias, plus every id of that family
this ape knows. A model id newer than this binary that is not its family's
alias target gets the fallback.

--model resolves one model the way a spawn would.

Exit codes:
  0  resolved (with or without a table)
  2  the table exists and is invalid, or --model is empty after trimming
  4  no _apex/config.yaml in --cwd or any parent`,
		Args:    cobra.NoArgs,
		Example: "  ape config effort --output-format json\n  ape config effort --model sonnet",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := resolveProjectConfig(cwdFlag)
			rep, err := buildEffortReport(cfg.Paths.Apex, modelFlag, cmd.Flags().Changed("model"))
			if err != nil {
				return usageErr(err)
			}
			format := output.Format(outputFormat)
			if format != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), format, rep)
			}
			return emitEffortHuman(cmd.OutOrStdout(), rep)
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	cmd.Flags().StringVar(&modelFlag, "model", "", "Resolve this model's effort as a spawn would (e.g. sonnet, claude-opus-5-5)")
	return cmd
}

func buildEffortReport(apexDir, model string, modelSet bool) (*effortReport, error) {
	table, err := effort.Load(apexDir)
	if err != nil {
		return nil, err
	}
	rep := &effortReport{Path: effort.Path(apexDir), Present: table != nil}
	if table == nil {
		rep.Mode, rep.LegacyDefault = effort.SourceLegacy, effort.LegacyDefault
	} else {
		rep.Mode, rep.Version, rep.Defaults, rep.Fallback = effort.SourceTable, table.Version, table.Defaults, table.Fallback
		rep.SettingsKeys = table.SettingsKeys()
	}
	if !modelSet {
		return rep, nil
	}
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("--model is empty")
	}
	canonical, _ := cost.CanonicalModelArg(model)
	fm := &effortForModel{Requested: model, Canonical: canonical, Family: cost.ModelFamily(canonical)}
	switch plan := effort.Decide("", "", table, model); {
	case table == nil:
		fm.Effort, fm.Via = plan.Resolved, effort.SourceLegacy
	case plan.FromFamily:
		fm.Effort, fm.Via = plan.Resolved, "family"
	default:
		fm.Effort, fm.Via = plan.Resolved, "fallback"
	}
	rep.Model = fm
	return rep, nil
}

func emitEffortHuman(w io.Writer, rep *effortReport) error {
	if !rep.Present {
		fmt.Fprintf(w, "no %s — every spawn gets %s process-wide (the legacy default)\n", rep.Path, rep.LegacyDefault)
	} else {
		fmt.Fprintf(w, "%s (version %d)\n\n", rep.Path, rep.Version)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "FAMILY\tEFFORT\tKEYS WRITTEN")
		families := make([]string, 0, len(rep.Defaults))
		for f := range rep.Defaults {
			families = append(families, f)
		}
		sort.Strings(families)
		for _, f := range families {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", f, rep.Defaults[f], strings.Join(rep.SettingsKeys[f], ", "))
		}
		fmt.Fprintf(tw, "(any other)\t%s\tfallback: top-level effortLevel\n", rep.Fallback)
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Fprintln(w, "\nAn explicit step/stage/pipeline effort or --effort overrides this, process-wide.")
	}
	if m := rep.Model; m != nil {
		fmt.Fprintf(w, "\n--model %s → %s (family %q): %s via %s\n", m.Requested, m.Canonical, m.Family, m.Effort, m.Via)
	}
	return nil
}

// sessionKindOf is the APE_SESSION kind for a run: its own override, else
// its eventing kind.
func sessionKindOf(cfg runConfig) string {
	if cfg.sessionKind != "" {
		return cfg.sessionKind
	}
	return string(cfg.kind)
}
