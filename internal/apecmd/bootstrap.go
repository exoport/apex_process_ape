package apecmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diegosz/apex_process_ape/internal/output"
	"github.com/diegosz/apex_process_ape/internal/trait"
	"github.com/diegosz/apex_process_ape/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const (
	exitCodeCatalogNotFound  = 4
	exitCodeTraitNotFound    = 3
	exitCodeConflictDetected = 2
)

func newBootstrapCmd() *cobra.Command {
	var (
		traitsFlag   string
		outFlag      string
		dryRun       bool
		noPicker     bool
		noTUI        bool
		onConflict   string
		outputFormat string
	)

	cmd := &cobra.Command{
		Use:     "bootstrap",
		Short:   "Bootstrap governance artifacts from traits",
		Long:    "Bootstrap a project's governance artifacts by composing traits from the catalog.",
		Example: "  ape bootstrap --traits go-service,http-api\n  ape bootstrap --no-picker --traits go-service --dry-run",
		RunE: func(_ *cobra.Command, _ []string) error {
			// --no-tui is a deprecated alias for --no-picker; either one
			// disables the interactive picker.
			noPicker = noPicker || noTUI

			catalog, err := trait.LoadCatalog()
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: catalog not found: %v\n", err)
				os.Exit(exitCodeCatalogNotFound)
			}

			selectedTraits, err := selectTraits(catalog, traitsFlag, noPicker)
			if err != nil {
				return err
			}
			if len(selectedTraits) == 0 {
				return errors.New("no traits specified")
			}

			resolver := trait.NewResolver(catalog)
			result, err := resolver.Resolve(selectedTraits)
			if err != nil {
				if trait.IsNotFoundError(err) {
					fmt.Fprintf(os.Stderr, "error: %v\n", err)
					os.Exit(exitCodeTraitNotFound)
				}
				return fmt.Errorf("resolve error: %w", err)
			}

			conflicts := result.Conflicts
			if len(conflicts) > 0 {
				switch onConflict {
				case "error":
					fmt.Fprintf(os.Stderr, "conflict detected:\n")
					for _, c := range conflicts {
						fmt.Fprintf(os.Stderr, "  category %q owned by: %s\n", c.Category, strings.Join(c.Owners, ", "))
					}
					os.Exit(exitCodeConflictDetected)
				case "first":
					result = resolver.ResolveConflicts(result, trait.ConflictStrategyFirst)
				case "last":
					result = resolver.ResolveConflicts(result, trait.ConflictStrategyLast)
				case "all":
					result = resolver.ResolveConflicts(result, trait.ConflictStrategyAll)
				default:
					result = resolver.ResolveConflicts(result, trait.ConflictStrategyFirst)
				}
			}

			format := output.Format(outputFormat)

			if dryRun {
				return printBootstrapResult(result, format, "")
			}

			if err := os.MkdirAll(outFlag, 0o755); err != nil {
				return fmt.Errorf("cannot create output dir: %w", err)
			}

			seedPath, err := writeArtifacts(result, outFlag)
			if err != nil {
				return fmt.Errorf("write artifacts: %w", err)
			}

			return printBootstrapResult(result, format, seedPath)
		},
	}

	cmd.Flags().StringVar(&traitsFlag, "traits", "", "Comma-separated list of trait names")
	cmd.Flags().StringVar(&outFlag, "out", ".", "Output directory for generated artifacts")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would be generated without writing files")
	cmd.Flags().BoolVar(&noPicker, "no-picker", false, "Disable the interactive trait picker (TUI)")
	// --no-tui is a deprecated alias for --no-picker; kept hidden for one release.
	cmd.Flags().BoolVar(&noTUI, "no-tui", false, "Deprecated: use --no-picker")
	_ = cmd.Flags().MarkHidden("no-tui")
	cmd.Flags().StringVar(&onConflict, "on-conflict", "first", "Conflict resolution strategy: first|last|all|error")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")

	return cmd
}

// selectTraits resolves the trait list either from the --traits flag, or
// interactively via the TUI when running in a terminal.
func selectTraits(catalog *trait.Catalog, traitsFlag string, noTUI bool) ([]string, error) {
	if traitsFlag == "" {
		if !term.IsTerminal(int(os.Stdin.Fd())) || noTUI {
			return nil, errors.New("--traits flag is required when not running interactively")
		}
		cfg, err := tui.RunBootstrap(catalog)
		if err != nil {
			return nil, fmt.Errorf("TUI error: %w", err)
		}
		return cfg.Traits, nil
	}
	var selected []string
	for t := range strings.SplitSeq(traitsFlag, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			selected = append(selected, t)
		}
	}
	return selected, nil
}

type bootstrapOutput struct {
	Status         string           `json:"status"          yaml:"status"`
	TraitsResolved []string         `json:"traits_resolved" yaml:"traits_resolved"` //nolint:tagliatelle // public CLI output format uses snake_case for backwards compatibility
	Conflicts      []conflictOutput `json:"conflicts"       yaml:"conflicts"`
	Merged         []string         `json:"merged"          yaml:"merged"`
	Artifacts      artifactsOutput  `json:"artifacts"       yaml:"artifacts"`
	Seed           string           `json:"seed"            yaml:"seed"`
}

type conflictOutput struct {
	Category string   `json:"category" yaml:"category"`
	Owners   []string `json:"owners"   yaml:"owners"`
}

type artifactEntry struct {
	ID   string `json:"id"   yaml:"id"`
	File string `json:"file" yaml:"file"`
}

type artifactsOutput struct {
	ADRs     []artifactEntry `json:"adrs"     yaml:"adrs"` //nolint:tagliatelle // "adrs" is the correct domain key; camelCase "adRs" would be confusing
	Patterns []artifactEntry `json:"patterns" yaml:"patterns"`
}

func printBootstrapResult(result *trait.ResolveResult, format output.Format, seedPath string) error {
	conflicts := make([]conflictOutput, 0, len(result.Conflicts))
	for _, c := range result.Conflicts {
		conflicts = append(conflicts, conflictOutput{
			Category: c.Category,
			Owners:   c.Owners,
		})
	}

	adrs := make([]artifactEntry, 0, len(result.ADRs))
	for _, a := range result.ADRs {
		adrs = append(adrs, artifactEntry{ID: a.ID, File: a.File})
	}

	patterns := make([]artifactEntry, 0, len(result.Patterns))
	for _, p := range result.Patterns {
		patterns = append(patterns, artifactEntry{ID: p.ID, File: p.File})
	}

	out := bootstrapOutput{
		Status:         "success",
		TraitsResolved: result.Traits,
		Conflicts:      conflicts,
		Merged:         []string{},
		Artifacts: artifactsOutput{
			ADRs:     adrs,
			Patterns: patterns,
		},
		Seed: seedPath,
	}

	switch format {
	case output.FormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	case output.FormatYAML:
		return output.Print(os.Stdout, output.FormatYAML, out)
	default:
		fmt.Printf("status:          %s\n", out.Status)
		fmt.Printf("traits resolved: %s\n", strings.Join(out.TraitsResolved, ", "))
		if len(out.Conflicts) > 0 {
			fmt.Println("conflicts:")
			for _, c := range out.Conflicts {
				fmt.Printf("  %s: %s\n", c.Category, strings.Join(c.Owners, ", "))
			}
		}
		fmt.Printf("\nartifacts:\n")
		fmt.Printf("  ADRs (%d):\n", len(out.Artifacts.ADRs))
		for _, a := range out.Artifacts.ADRs {
			fmt.Printf("    %s -> %s\n", a.ID, a.File)
		}
		fmt.Printf("  patterns (%d):\n", len(out.Artifacts.Patterns))
		for _, p := range out.Artifacts.Patterns {
			fmt.Printf("    %s -> %s\n", p.ID, p.File)
		}
		if out.Seed != "" {
			fmt.Printf("\nseed: %s\n", out.Seed)
		}
		return nil
	}
}

func writeArtifacts(result *trait.ResolveResult, outDir string) (string, error) {
	govDir := filepath.Join(outDir, "governance")
	adrDir := filepath.Join(govDir, "adrs")
	patDir := filepath.Join(govDir, "patterns")

	for _, d := range []string{adrDir, patDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
	}

	catalogBase := trait.CatalogBaseDir()

	for _, a := range result.ADRs {
		src := filepath.Join(catalogBase, a.File)
		dst := filepath.Join(outDir, a.File)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		data, err := os.ReadFile(src)
		if err != nil {
			data = fmt.Appendf(nil, "# %s\n\nSource: %s\n", a.ID, a.File)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil { //nolint:gosec // governance artifacts are documentation, world-readable is intentional
			return "", err
		}
	}

	for _, p := range result.Patterns {
		src := filepath.Join(catalogBase, p.File)
		dst := filepath.Join(outDir, p.File)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		data, err := os.ReadFile(src)
		if err != nil {
			data = fmt.Appendf(nil, "# %s\n\nSource: %s\n", p.ID, p.File)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil { //nolint:gosec // governance artifacts are documentation, world-readable is intentional
			return "", err
		}
	}

	seedPath := filepath.Join(govDir, ".governance-seed.yaml")
	seedContent := buildSeedContent(result)
	if err := os.WriteFile(seedPath, []byte(seedContent), 0o644); err != nil { //nolint:gosec // seed file is configuration, world-readable is intentional
		return "", err
	}

	readmePath := filepath.Join(govDir, "README.md")
	readmeContent := buildReadmeContent(result)
	if err := os.WriteFile(readmePath, []byte(readmeContent), 0o644); err != nil { //nolint:gosec // README is documentation, world-readable is intentional
		return "", err
	}

	return strings.TrimPrefix(seedPath, outDir+"/"), nil
}

func buildSeedContent(result *trait.ResolveResult) string {
	var sb strings.Builder
	sb.WriteString("# Generated by ape bootstrap\n")
	sb.WriteString("traits:\n")
	for _, t := range result.Traits {
		fmt.Fprintf(&sb, "  - %s\n", t)
	}
	sb.WriteString("\nadrs:\n")
	for _, a := range result.ADRs {
		fmt.Fprintf(&sb, "  - id: %s\n    file: %s\n", a.ID, a.File)
	}
	sb.WriteString("\npatterns:\n")
	for _, p := range result.Patterns {
		fmt.Fprintf(&sb, "  - id: %s\n    file: %s\n", p.ID, p.File)
	}
	return sb.String()
}

func buildReadmeContent(result *trait.ResolveResult) string {
	var sb strings.Builder
	sb.WriteString("# Governance\n\n")
	sb.WriteString("This directory contains governance artifacts generated by `ape bootstrap`.\n\n")
	sb.WriteString("## Traits\n\n")
	for _, t := range result.Traits {
		fmt.Fprintf(&sb, "- %s\n", t)
	}
	sb.WriteString("\n## ADRs\n\n")
	for _, a := range result.ADRs {
		fmt.Fprintf(&sb, "- [%s](%s)\n", a.ID, a.File)
	}
	sb.WriteString("\n## Patterns\n\n")
	for _, p := range result.Patterns {
		fmt.Fprintf(&sb, "- [%s](%s)\n", p.ID, p.File)
	}
	return sb.String()
}
