package apecmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/diegosz/apex_process_ape/internal/output"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newPatternCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pattern",
		Short: "Manage governance patterns",
	}

	cmd.AddCommand(
		newPatternListCmd(),
		newPatternValidateCmd(),
		newPatternSyncCmd(),
	)

	return cmd
}

func newPatternListCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:     cmdUseList,
		Short:   "List all governance patterns",
		Example: "  ape pattern list --output-format json",
		RunE: func(_ *cobra.Command, _ []string) error {
			patternsDir := findPatternsDir()
			if patternsDir == "" {
				fmt.Fprintln(os.Stderr, "no patterns directory found (looking for development/patterns/)")
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

			format := output.Format(outputFormat)
			switch format {
			case output.FormatJSON, output.FormatYAML:
				return output.Print(os.Stdout, format, index.Patterns)
			default:
				for _, p := range index.Patterns {
					fmt.Printf("%-20s %s\n", p.ID, p.Title)
				}
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

func newPatternValidateCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:     "validate",
		Short:   "Validate governance patterns",
		Example: "  ape pattern validate --output-format json",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runMarkdownDirValidate(findPatternsDir(), "pattern", outputFormat)
		},
	}

	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

func newPatternSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "sync",
		Short:  "Sync patterns (not yet implemented)",
		Hidden: true,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Println("sync not yet implemented")
		},
	}
}

func findPatternsDir() string {
	candidates := []string{
		"development/patterns",
		filepath.Join(os.Getenv("APE_PROCESS_REPO"), "development", "patterns"),
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}
