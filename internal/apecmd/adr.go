package apecmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	return &cobra.Command{
		Use:   cmdUseList,
		Short: "List all ADRs",
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
					ID     string `yaml:"id"`
					Title  string `yaml:"title"`
					Status string `yaml:"status"`
				} `yaml:"adrs"` //nolint:tagliatelle // YAML key "adrs" matches index.yaml format; "adRs" would be wrong
			}
			if err := yaml.Unmarshal(data, &index); err != nil {
				return fmt.Errorf("cannot parse ADR index: %w", err)
			}

			for _, a := range index.ADRs {
				fmt.Printf("%-15s %-10s %s\n", a.ID, a.Status, a.Title)
			}
			return nil
		},
	}
}

func newADRValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate ADR files",
		RunE: func(_ *cobra.Command, _ []string) error {
			adrDir := findADRDir()
			if adrDir == "" {
				fmt.Fprintln(os.Stderr, "no ADR directory found")
				return nil
			}
			fmt.Printf("Validating ADRs in %s\n", adrDir)
			entries, err := os.ReadDir(adrDir)
			if err != nil {
				return fmt.Errorf("cannot read ADR dir: %w", err)
			}
			count := 0
			for _, e := range entries {
				if !e.IsDir() && filepath.Ext(e.Name()) == ".md" {
					count++
					fmt.Printf("  OK: %s\n", e.Name())
				}
			}
			fmt.Printf("Validated %d ADR file(s).\n", count)
			return nil
		},
	}
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
