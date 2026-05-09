package apecmd

import (
	"fmt"
	"os"
	"path/filepath"

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
	return &cobra.Command{
		Use:   cmdUseList,
		Short: "List all governance patterns",
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
					ID    string `yaml:"id"`
					Title string `yaml:"title"`
					File  string `yaml:"file"`
				} `yaml:"patterns"`
			}
			if err := yaml.Unmarshal(data, &index); err != nil {
				return fmt.Errorf("cannot parse patterns index: %w", err)
			}

			for _, p := range index.Patterns {
				fmt.Printf("%-20s %s\n", p.ID, p.Title)
			}
			return nil
		},
	}
}

func newPatternValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate governance patterns",
		RunE: func(_ *cobra.Command, _ []string) error {
			patternsDir := findPatternsDir()
			if patternsDir == "" {
				fmt.Fprintln(os.Stderr, "no patterns directory found")
				return nil
			}
			fmt.Printf("Validating patterns in %s\n", patternsDir)
			entries, err := os.ReadDir(patternsDir)
			if err != nil {
				return fmt.Errorf("cannot read patterns dir: %w", err)
			}
			count := 0
			for _, e := range entries {
				if !e.IsDir() && filepath.Ext(e.Name()) == ".md" {
					count++
					fmt.Printf("  OK: %s\n", e.Name())
				}
			}
			fmt.Printf("Validated %d pattern file(s).\n", count)
			return nil
		},
	}
}

func newPatternSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Sync patterns (not yet implemented)",
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
