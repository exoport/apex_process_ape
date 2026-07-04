package apecmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/diegosz/apex_process_ape/internal/output"
	"github.com/diegosz/apex_process_ape/internal/trait"
	"github.com/spf13/cobra"
)

func newTraitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trait",
		Short: "Manage and inspect traits",
	}

	cmd.AddCommand(
		newTraitListCmd(),
		newTraitShowCmd(),
		newTraitValidateCmd(),
		newTraitConflictsCmd(),
	)

	return cmd
}

func newTraitListCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:   cmdUseList,
		Short: "List all available traits",
		RunE: func(_ *cobra.Command, _ []string) error {
			catalog, err := trait.LoadCatalog()
			if err != nil {
				return fmt.Errorf("cannot load catalog: %w", err)
			}

			format := output.Format(outputFormat)
			switch format {
			case output.FormatJSON, output.FormatYAML:
				return output.Print(os.Stdout, format, catalog.Traits)
			default:
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tVERSION\tDESCRIPTION\tTAGS")
				for _, t := range catalog.Traits {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", t.Name, t.Version, t.Description, strings.Join(t.Tags, ","))
				}
				return w.Flush()
			}
		},
	}

	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

func newTraitShowCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show details of a trait",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			t, err := trait.LoadTrait(name)
			if err != nil {
				return fmt.Errorf("cannot load trait %q: %w", name, err)
			}

			format := output.Format(outputFormat)
			switch format {
			case output.FormatJSON, output.FormatYAML:
				return output.Print(os.Stdout, format, t)
			default:
				fmt.Printf("Name:        %s\n", t.Name)
				fmt.Printf("Version:     %s\n", t.Version)
				fmt.Printf("Description: %s\n", t.Description)
				if len(t.Uses) > 0 {
					fmt.Printf("Uses:        %s\n", strings.Join(t.Uses, ", "))
				}
				fmt.Printf("\nOwns Categories:\n")
				for _, c := range t.OwnsCategories {
					fmt.Printf("  - %s\n", c)
				}
				fmt.Printf("\nADRs (%d):\n", len(t.ADRs))
				for _, a := range t.ADRs {
					fmt.Printf("  %s: %s\n", a.ID, a.Title)
				}
				fmt.Printf("\nPatterns (%d):\n", len(t.Patterns))
				for _, p := range t.Patterns {
					fmt.Printf("  %s: %s\n", p.ID, p.Title)
				}
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

func newTraitValidateCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:     "validate <file>",
		Short:   "Validate a trait YAML file",
		Example: "  ape trait validate ./mytrait.yaml --output-format json",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			file := args[0]
			t, err := trait.LoadTraitFromFile(file)
			if err != nil {
				return fmt.Errorf("validation failed: %w", err)
			}

			var errs []string
			if t.Name == "" {
				errs = append(errs, "missing required field: name")
			}
			if t.Version == "" {
				errs = append(errs, "missing required field: version")
			}
			if t.Description == "" {
				errs = append(errs, "missing required field: description")
			}

			type validateResult struct {
				File   string   `json:"file"   yaml:"file"`
				Valid  bool     `json:"valid"  yaml:"valid"`
				Errors []string `json:"errors" yaml:"errors"`
			}
			res := validateResult{File: file, Valid: len(errs) == 0, Errors: errs}

			format := output.Format(outputFormat)
			switch format {
			case output.FormatJSON, output.FormatYAML:
				if err := output.Print(os.Stdout, format, res); err != nil {
					return err
				}
				if !res.Valid {
					return fmt.Errorf("validation failed with %d error(s)", len(errs))
				}
				return nil
			default:
				if len(errs) > 0 {
					fmt.Fprintf(os.Stderr, "validation errors in %s:\n", file)
					for _, e := range errs {
						fmt.Fprintf(os.Stderr, "  - %s\n", e)
					}
					return fmt.Errorf("validation failed with %d error(s)", len(errs))
				}

				fmt.Printf("OK: %s is valid\n", file)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

func newTraitConflictsCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "conflicts <trait1> <trait2> [...]",
		Short: "Check for conflicts between traits",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			catalog, err := trait.LoadCatalog()
			if err != nil {
				return fmt.Errorf("cannot load catalog: %w", err)
			}

			resolver := trait.NewResolver(catalog)
			result, err := resolver.Resolve(args)
			if err != nil {
				return fmt.Errorf("resolve error: %w", err)
			}

			format := output.Format(outputFormat)
			if len(result.Conflicts) == 0 {
				switch format {
				case output.FormatJSON:
					return output.Print(os.Stdout, format, map[string]any{keyConflicts: []any{}})
				case output.FormatYAML:
					return output.Print(os.Stdout, format, map[string]any{keyConflicts: []any{}})
				default:
					fmt.Println("No conflicts detected.")
					return nil
				}
			}

			type conflictItem struct {
				Category string   `json:"category" yaml:"category"`
				Owners   []string `json:"owners"   yaml:"owners"`
			}
			var items []conflictItem
			for _, c := range result.Conflicts {
				items = append(items, conflictItem{Category: c.Category, Owners: c.Owners})
			}

			switch format {
			case output.FormatJSON, output.FormatYAML:
				return output.Print(os.Stdout, format, map[string]any{keyConflicts: items})
			default:
				fmt.Printf("Conflicts detected (%d):\n", len(items))
				for _, c := range items {
					fmt.Printf("  category %q owned by: %s\n", c.Category, strings.Join(c.Owners, ", "))
				}
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}
