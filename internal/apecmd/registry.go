package apecmd

import (
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/registry"
	"github.com/spf13/cobra"
)

// Shared flag help, so the four families and the fan-out cannot drift in
// how they describe the same flag.
const (
	helpCwd    = "Project root (default: current working dir)"
	helpFormat = "Output format: human|json|yaml"
	helpStrict = "Exit 1 when there are findings (default: report and exit 0)"
)

// registryVerbs attaches the family verbs to a family's noun command.
// `ape adr` and `ape pattern` already exist with list/new; the two new
// families get list too. Every family answers to its plural as a cobra
// alias — the framework's own vocabulary is plural wherever it names a
// collection (`{governance_folder}/adrs/`, the index's `adrs:` key,
// `extensions: [ext-adrs]`), so a skill author writing `ape adrs sync` is
// following the directory being pointed at. The canonical singular is
// what help and docs use; the alias is a courtesy, not a second surface.
func registryVerbs(family registry.Family) []*cobra.Command {
	return []*cobra.Command{
		newFamilyVerifyCmd(family),
		newFamilySyncCmd(family),
		newFamilyUpdateCmd(family),
	}
}

func newFeatureCmd() *cobra.Command {
	family, err := registry.FamilyByName("features")
	if err != nil {
		panic(err) // the family table is a compile-time constant
	}
	cmd := &cobra.Command{
		Use:     "feature",
		Aliases: []string{"features"},
		Short:   "Inspect and maintain the feature registry",
	}
	cmd.AddCommand(newFamilyListCmd(family))
	cmd.AddCommand(registryVerbs(family)...)
	return cmd
}

func newCapabilityCmd() *cobra.Command {
	family, err := registry.FamilyByName("capabilities")
	if err != nil {
		panic(err)
	}
	cmd := &cobra.Command{
		Use:     "capability",
		Aliases: []string{"capabilities"},
		Short:   "Inspect and maintain the capability registry",
	}
	cmd.AddCommand(newFamilyListCmd(family))
	cmd.AddCommand(registryVerbs(family)...)
	return cmd
}

func newRegistryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Verify or reconcile every record registry at once",
		Long: `Cross-family fan-out over the four record registries — ADRs, patterns,
features and capabilities. Each family also carries these verbs on its own
noun (` + "`ape adr verify`" + `); this is the whole-project view.`,
	}
	cmd.AddCommand(newRegistryVerifyCmd(), newRegistrySyncCmd())
	return cmd
}

// verifyLong is the shared explanation of the four checks. Stated on
// every verify command because the scope IS the contract: a reader has to
// be able to tell what this will never report.
const verifyLong = `Exactly four checks, and no others:

  1. registry.orphan_record / registry.phantom_entry
     set equality between the record directory and index.yaml, both
     directions
  2. registry.file_unresolved
     every index file: resolves, relative to the index's own directory
  3. registry.duplicate_id
     duplicate ids, in the index and on disk
  4. registry.record_unparseable
     the record parses as frontmatter at all

No schema validation, no field drift, no tag comparison, no updated_at
comparison — those are judgment, and a verifier that wanders into them
stops being trustworthy.

An index.yaml that is absent while records exist is reported once, as
registry.index_missing: the degenerate case of check 1, not a fifth check.
One finding beats one orphan per record, which would bury the only fact
that matters.

Findings travel in the payload. Exit is 0 even with findings unless
--strict is passed, so this is safe to call from anywhere.`

func newFamilyVerifyCmd(family registry.Family) *cobra.Command {
	var (
		cwdFlag      string
		outputFormat string
		strict       bool
	)
	cmd := &cobra.Command{
		Use:     "verify",
		Short:   "Verify the " + family.Name + " registry against its index",
		Long:    verifyLong,
		Args:    cobra.NoArgs,
		Example: "  ape " + family.Singular + " verify --output-format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := resolveProjectConfig(cwdFlag)
			report, err := registry.Verify(cfg, registry.VerifyOptions{Only: []string{family.Name}})
			if err != nil {
				return err
			}
			return emitRegistryReport(cmd.OutOrStdout(), report, output.Format(outputFormat), strict)
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	cmd.Flags().BoolVar(&strict, "strict", false, helpStrict)
	return cmd
}

func newRegistryVerifyCmd() *cobra.Command {
	var (
		cwdFlag      string
		outputFormat string
		strict       bool
		all          bool
		families     []string
	)
	cmd := &cobra.Command{
		Use:     "verify",
		Short:   "Verify every record registry (or a named subset)",
		Long:    verifyLong,
		Args:    cobra.NoArgs,
		Example: "  ape registry verify --all --output-format json\n  ape registry verify --family adrs,patterns",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := resolveProjectConfig(cwdFlag)
			only := families
			if all {
				only = nil
			}
			report, err := registry.Verify(cfg, registry.VerifyOptions{Only: only})
			if err != nil {
				return err
			}
			return emitRegistryReport(cmd.OutOrStdout(), report, output.Format(outputFormat), strict)
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	cmd.Flags().BoolVar(&strict, "strict", false, helpStrict)
	cmd.Flags().BoolVar(&all, "all", false, "Verify every family (the default when --family is not given)")
	cmd.Flags().StringSliceVar(&families, "family", nil,
		"Families to verify: "+strings.Join(registry.FamilyNames(), ","))
	return cmd
}

const syncLong = `Reconcile index.yaml against the records on disk: records with no entry
are added, entries whose id no record claims are removed, and an entry
whose file: no longer resolves is repointed at the record claiming its id.

This is the repair for the findings ` + "`verify`" + ` reports, and nothing more —
it copies what a record's own frontmatter states and invents no titles,
statuses or any other field. A renamed record keeps its authored entry
rather than being dropped and re-added.

--check makes it a dry run: the same diff, nothing written. generated_at
moves only when something else did.`

func newFamilySyncCmd(family registry.Family) *cobra.Command {
	var (
		cwdFlag      string
		outputFormat string
		check        bool
	)
	cmd := &cobra.Command{
		Use:     "sync",
		Short:   "Reconcile the " + family.Name + " index against records on disk",
		Long:    syncLong,
		Args:    cobra.NoArgs,
		Example: "  ape " + family.Singular + " sync --check",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRegistrySync(cmd.OutOrStdout(), cwdFlag, outputFormat, check, []string{family.Name})
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	cmd.Flags().BoolVar(&check, "check", false, "Report the diff without writing")
	return cmd
}

func newRegistrySyncCmd() *cobra.Command {
	var (
		cwdFlag      string
		outputFormat string
		check        bool
		all          bool
		families     []string
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Reconcile every record index against records on disk",
		Long:  syncLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			only := families
			if all {
				only = nil
			}
			return runRegistrySync(cmd.OutOrStdout(), cwdFlag, outputFormat, check, only)
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	cmd.Flags().BoolVar(&check, "check", false, "Report the diff without writing")
	cmd.Flags().BoolVar(&all, "all", false, "Reconcile every family (the default when --family is not given)")
	cmd.Flags().StringSliceVar(&families, "family", nil,
		"Families to reconcile: "+strings.Join(registry.FamilyNames(), ","))
	return cmd
}

func runRegistrySync(w io.Writer, cwdFlag, outputFormat string, check bool, only []string) error {
	cfg := resolveProjectConfig(cwdFlag)
	res, err := registry.Sync(cfg, registry.SyncOptions{
		Only:        only,
		Check:       check,
		GeneratedAt: cfg.Timestamp,
	})
	if err != nil {
		return err
	}
	format := output.Format(outputFormat)
	if format != output.FormatHuman {
		return output.Print(w, format, res)
	}
	if !res.Changed() {
		fmt.Fprintln(w, "registries already in sync — nothing to do")
		return nil
	}
	verb := "applied"
	if check {
		verb = "would apply"
	}
	fmt.Fprintf(w, "%s %d change(s):\n", verb, len(res.Changes))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range res.Changes {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", c.Action, c.Family, c.ID, c.Detail)
	}
	return tw.Flush()
}

func newFamilyUpdateCmd(family registry.Family) *cobra.Command {
	var (
		cwdFlag      string
		outputFormat string
		updatesPath  string
		generatedAt  string
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Apply field deltas to existing " + family.Name + " index entries",
		Long: `Apply per-entry field deltas to index.yaml and refresh generated_at.

--updates takes {"<id>": {"<field>": "<value>"}} as a file path or '-' for
stdin. Only entries ALREADY listed may be updated: an unknown id is an
error raised before anything is written, because creating an index entry
is the job of the skill that authors the document it points at.

The rendered index is round-trip parsed before it replaces the file, and
the write is atomic — a crash mid-write cannot truncate an index. Key
order and comments survive, which a PyYAML round trip does not manage.

Exit codes:
  0  applied
  1  unknown id, unreadable updates, or an unwritable index (nothing written)`,
		Args:    cobra.NoArgs,
		Example: `  echo '{"ADR-0001":{"status":"superseded"}}' | ape adr update --updates -`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := resolveProjectConfig(cwdFlag)
			data, err := readUpdatesInput(cmd.InOrStdin(), updatesPath)
			if err != nil {
				return err
			}
			updates, err := registry.ParseUpdates(data)
			if err != nil {
				return err
			}
			stamp := generatedAt
			if stamp == "" {
				stamp = cfg.Timestamp
			}
			res, err := registry.Update(cfg, family, updates, stamp)
			if err != nil {
				return err
			}
			format := output.Format(outputFormat)
			if format != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), format, res)
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"OK: %s updated (%d entr%s, %d field delta(s)); round-trip parse verified\n",
				res.Index, res.Entries, pluralY(res.Entries), res.Fields)
			return nil
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	cmd.Flags().StringVar(&updatesPath, "updates", "-", "JSON file with per-entry field deltas, or '-' for stdin")
	cmd.Flags().StringVar(&generatedAt, "generated-at", "", "Timestamp to write as generated_at (default: resolved config timestamp)")
	return cmd
}

// pluralY renders the English "entry"/"entries" suffix, matching the
// message shape render-index-update.py printed so the calling prose in
// the framework reads identically.
func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func readUpdatesInput(stdin io.Reader, path string) ([]byte, error) {
	if path == "" || path == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read updates from stdin: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read updates: %w", err)
	}
	return data, nil
}

func newFamilyListCmd(family registry.Family) *cobra.Command {
	var (
		cwdFlag      string
		outputFormat string
	)
	cmd := &cobra.Command{
		Use:     cmdUseList,
		Short:   "List " + family.Name + " from the registry index",
		Args:    cobra.NoArgs,
		Example: "  ape " + family.Singular + " list --output-format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := resolveProjectConfig(cwdFlag)
			dir := family.Dir(cfg.Paths)
			if dir == "" {
				fmt.Fprintf(os.Stderr, "no %s directory configured\n", family.Name)
				return nil
			}
			idx, err := registry.LoadIndex(dir, family)
			if err != nil {
				return err
			}
			if idx.Missing {
				fmt.Fprintf(os.Stderr, "no %s index at %s\n", family.Name, idx.Path)
				return nil
			}
			entries, _ := idx.Entries()
			rows := make([]map[string]string, 0, len(entries))
			for _, e := range entries {
				row := map[string]string{"id": e.ID}
				maps.Copy(row, e.Fields)
				rows = append(rows, row)
			}
			format := output.Format(outputFormat)
			if format != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), format, rows)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			for _, e := range entries {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", e.ID, e.Fields["status"], e.Fields["title"])
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	return cmd
}

// emitRegistryReport renders a verify report and applies the --strict
// exit policy. Exit 0 with findings is the default and the whole reason
// these commands are safe to call from a review path.
func emitRegistryReport(w io.Writer, report *registry.Report, format output.Format, strict bool) error {
	if format != output.FormatHuman {
		if err := output.Print(w, format, report); err != nil {
			return err
		}
	} else {
		emitRegistryHuman(w, report)
	}
	if strict && !report.OK() {
		os.Exit(1)
	}
	return nil
}

func emitRegistryHuman(w io.Writer, report *registry.Report) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, f := range report.Families {
		if f.Skipped {
			fmt.Fprintf(tw, "%s\tskipped\t%s\n", f.Family, f.Reason)
			continue
		}
		fmt.Fprintf(tw, "%s\t%d record(s), %d entr%s\t%s\n",
			f.Family, f.Records, f.Entries, pluralY(f.Entries), f.Dir)
	}
	_ = tw.Flush()
	if report.OK() {
		fmt.Fprintln(w, "\nno findings")
		return
	}
	fmt.Fprintf(w, "\n%d finding(s):\n", len(report.Findings))
	ftw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, f := range report.Findings {
		id := f.ID
		if id == "" {
			id = "-"
		}
		fmt.Fprintf(ftw, "  %s\t%s\t%s\t%s\n", f.Check, f.Family, id, f.Message)
	}
	_ = ftw.Flush()
}
