package apecmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/registry"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newADRCmd() *cobra.Command {
	family, err := registry.FamilyByName("adrs")
	if err != nil {
		panic(err) // the family table is a compile-time constant
	}
	cmd := &cobra.Command{
		Use:     "adr",
		Aliases: []string{"adrs"},
		Short:   "Manage Architecture Decision Records",
	}

	cmd.AddCommand(
		newADRListCmd(),
		newADRNewCmd(),
		newLegacyValidateCmd(family),
	)
	cmd.AddCommand(registryVerbs(family)...)

	return cmd
}

// newLegacyValidateCmd keeps `ape <family> validate` working after the
// verb was renamed to `verify`. Hidden rather than a cobra alias, because
// an alias appears in help and this spelling is on its way out: one
// release as a hidden pointer, then deleted.
//
// It is NOT a behaviour-preserving shim. The old implementation
// (runMarkdownDirValidate) listed .md files and printed "OK:" for each
// without opening one; this runs the real checks. The exit code is
// unchanged — still 0 without --strict — but the JSON payload changes
// from {dir,count,files[]} to findings[].
func newLegacyValidateCmd(family registry.Family) *cobra.Command {
	cmd := newFamilyVerifyCmd(family)
	cmd.Use = "validate"
	cmd.Hidden = true
	cmd.Short = "Deprecated alias for `verify`"
	cmd.Long = "Deprecated: use `ape " + family.Singular + " verify`.\n\n" + verifyLong
	return cmd
}

func newADRListCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:     cmdUseList,
		Short:   "List all ADRs",
		Example: "  ape adr list --output-format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			adrDir := findADRDir()
			if adrDir == "" {
				fmt.Fprintln(os.Stderr, "no ADR directory found (no _apex/config.yaml governance folder, and no development/adrs/)")
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

			out := cmd.OutOrStdout()
			format := output.Format(outputFormat)
			switch format {
			case output.FormatJSON, output.FormatYAML:
				return output.Print(out, format, index.ADRs)
			default:
				for _, a := range index.ADRs {
					fmt.Fprintf(out, "%-15s %-10s %s\n", a.ID, a.Status, a.Title)
				}
				return nil
			}
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
		RunE: func(cmd *cobra.Command, args []string) error {
			title := strings.Join(args, " ")
			adrDir := adrDirForWrite("")
			if err := os.MkdirAll(adrDir, 0o755); err != nil {
				return fmt.Errorf("cannot create ADR directory: %w", err)
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

			fmt.Fprintf(cmd.OutOrStdout(), "Created: %s\n", filePath)
			return nil
		},
	}
}

// findADRDir resolves the project's ADR directory through the config
// (PLAN-25 D1) rather than probing hardcoded paths. The old two-candidate
// probe looked for "development/adrs" and $APE_PROCESS_REPO/development/adrs,
// neither of which exists on a project that sets
// `governance_folder: development/governance` — so `ape adr list` reported
// "no ADR directory found" against 64 ADRs on disk.
//
// The legacy candidates stay as a fall-back for a tree with no _apex/
// config at all, so nothing that worked before stops working.
func findADRDir() string { return findADRDirIn("") }

func findADRDirIn(cwdFlag string) string {
	if res := tryResolveProjectConfig(cwdFlag); res != nil && res.Paths.ADRs != "" {
		if _, err := os.Stat(res.Paths.ADRs); err == nil {
			return res.Paths.ADRs
		}
	}
	return firstExistingDir(
		// filepath.Join, not a "development/adrs" literal: this value is
		// returned to callers that open it and print it, so it has to be
		// the platform's own spelling. On Windows the literal resolved
		// fine but came back forward-slashed, unlike every sibling path.
		filepath.Join("development", "adrs"),
		filepath.Join(os.Getenv("APE_PROCESS_REPO"), "development", "adrs"),
	)
}

// adrDirForWrite is findADRDirIn plus the create-if-absent path `adr new`
// needs: the resolved governance location, so `new` and `list` cannot
// disagree about where an ADR lives.
func adrDirForWrite(cwdFlag string) string {
	if dir := findADRDirIn(cwdFlag); dir != "" {
		return dir
	}
	if res := tryResolveProjectConfig(cwdFlag); res != nil && res.Paths.ADRs != "" {
		return res.Paths.ADRs
	}
	return "development/adrs"
}
