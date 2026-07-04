package apecmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diegosz/apex_process_ape/internal/output"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newADRCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "adr",
		Short: "Manage Architecture Decision Records",
	}

	cmd.AddCommand(
		newADRListCmd(),
		newADRValidateCmd(),
		newADRNewCmd(),
	)

	return cmd
}

func newADRListCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:     cmdUseList,
		Short:   "List all ADRs",
		Example: "  ape adr list --output-format json",
		RunE: func(_ *cobra.Command, _ []string) error {
			adrDir := findADRDir()
			if adrDir == "" {
				fmt.Fprintln(os.Stderr, "no ADR directory found (looking for development/adrs/)")
				return nil
			}

			indexFile := filepath.Join(adrDir, "index.yaml")
			data, err := os.ReadFile(indexFile)
			if err != nil {
				return fmt.Errorf("cannot read ADR index: %w", err)
			}

			var index struct {
				ADRs []struct {
					ID     string `json:"id"     yaml:"id"`
					Title  string `json:"title"  yaml:"title"`
					Status string `json:"status" yaml:"status"`
				} `json:"adrs" yaml:"adrs"` //nolint:tagliatelle // YAML key "adrs" matches index.yaml format; "adRs" would be wrong
			}
			if err := yaml.Unmarshal(data, &index); err != nil {
				return fmt.Errorf("cannot parse ADR index: %w", err)
			}

			format := output.Format(outputFormat)
			switch format {
			case output.FormatJSON, output.FormatYAML:
				return output.Print(os.Stdout, format, index.ADRs)
			default:
				for _, a := range index.ADRs {
					fmt.Printf("%-15s %-10s %s\n", a.ID, a.Status, a.Title)
				}
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

func newADRValidateCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:     "validate",
		Short:   "Validate ADR files",
		Example: "  ape adr validate --output-format json",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runMarkdownDirValidate(findADRDir(), "ADR", outputFormat)
		},
	}

	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

func newADRNewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "new <title>",
		Short: "Scaffold a new ADR file",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			title := strings.Join(args, " ")
			adrDir := findADRDir()
			if adrDir == "" {
				adrDir = "development/adrs"
				if err := os.MkdirAll(adrDir, 0o755); err != nil {
					return fmt.Errorf("cannot create ADR directory: %w", err)
				}
			}

			slug := strings.ToLower(strings.ReplaceAll(title, " ", "-"))
			slug = strings.Map(func(r rune) rune {
				if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
					return r
				}
				return -1
			}, slug)

			filename := fmt.Sprintf("ADR-%s.md", slug)
			filePath := filepath.Join(adrDir, filename)

			content := fmt.Sprintf(`---
id: ADR-%s
title: %s
status: proposed
date: %s
---

## Context

[Describe the context and problem that motivates this decision.]

## Decision

[Describe the decision that was made.]

## Consequences

[Describe the resulting context, positive and negative outcomes.]
`, strings.ToUpper(slug), title, time.Now().Format("2006-01-02"))

			if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil { //nolint:gosec // ADR files are documentation and intentionally world-readable
				return fmt.Errorf("cannot write ADR file: %w", err)
			}

			fmt.Printf("Created: %s\n", filePath)
			return nil
		},
	}
}

func findADRDir() string {
	candidates := []string{
		"development/adrs",
		filepath.Join(os.Getenv("APE_PROCESS_REPO"), "development", "adrs"),
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
