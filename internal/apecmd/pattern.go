package apecmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/registry"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newPatternCmd() *cobra.Command {
	family, err := registry.FamilyByName("patterns")
	if err != nil {
		panic(err)
	}
	cmd := &cobra.Command{
		Use:     "pattern",
		Aliases: []string{"patterns"},
		Short:   "Manage governance patterns",
	}

	cmd.AddCommand(
		newPatternListCmd(),
		newLegacyValidateCmd(family),
	)
	// This is where the third stub went: `ape pattern sync` printed "not
	// yet implemented" alongside `ape sync patterns`, two paths to one
	// verb. registryVerbs supplies the real one.
	cmd.AddCommand(registryVerbs(family)...)

	return cmd
}

func newPatternListCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:     cmdUseList,
		Short:   "List all governance patterns",
		Example: "  ape pattern list --output-format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			patternsDir := findPatternsDir()
			if patternsDir == "" {
				fmt.Fprintln(os.Stderr, "no patterns directory found (no _apex/config.yaml governance folder, and no development/patterns/)")
				return nil
			}

			indexFile := filepath.Join(patternsDir, "index.yaml")
			data, err := os.ReadFile(indexFile)
			if err != nil {
				return fmt.Errorf("cannot read patterns index: %w", err)
			}

			var index struct {
				Patterns []struct {
					ID    string `json:"id"    yaml:"id"`
					Title string `json:"title" yaml:"title"`
					File  string `json:"file"  yaml:"file"`
				} `json:"patterns" yaml:"patterns"`
			}
			if err := yaml.Unmarshal(data, &index); err != nil {
				return fmt.Errorf("cannot parse patterns index: %w", err)
			}

			out := cmd.OutOrStdout()
			format := output.Format(outputFormat)
			switch format {
			case output.FormatJSON, output.FormatYAML:
				return output.Print(out, format, index.Patterns)
			default:
				for _, p := range index.Patterns {
					fmt.Fprintf(out, "%-20s %s\n", p.ID, p.Title)
				}
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

// findPatternsDir resolves through the project config (PLAN-25 D1); see
// findADRDir for why the hardcoded probe was wrong. The legacy candidates
// remain as a fall-back outside a configured project.
func findPatternsDir() string { return findPatternsDirIn("") }

func findPatternsDirIn(cwdFlag string) string {
	if res := tryResolveProjectConfig(cwdFlag); res != nil && res.Paths.Patterns != "" {
		if _, err := os.Stat(res.Paths.Patterns); err == nil {
			return res.Paths.Patterns
		}
	}
	return firstExistingDir(
		"development/patterns",
		filepath.Join(os.Getenv("APE_PROCESS_REPO"), "development", "patterns"),
	)
}

// firstExistingDir returns the first candidate that exists as a
// directory, or "". Empty candidates are skipped, so an unset
// $APE_PROCESS_REPO cannot resolve to the filesystem root.
func firstExistingDir(candidates ...string) string {
	for _, c := range candidates {
		if c == "" || c == string(filepath.Separator) {
			continue
		}
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c
		}
	}
	return ""
}
