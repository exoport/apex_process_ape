package apecmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/diegosz/apex_process_ape/internal/output"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	Version   = "dev"
	BuildDate = "unknown"
	GitCommit = "unknown"
)

type versionResult struct {
	Version   string `json:"version"   yaml:"version"`
	BuildDate string `json:"buildDate" yaml:"buildDate"`
	GitCommit string `json:"gitCommit" yaml:"gitCommit"`
}

func newVersionCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Long:  "Print the version, build date, and git commit of the ape binary.",
		RunE: func(_ *cobra.Command, _ []string) error {
			res := versionResult{
				Version:   Version,
				BuildDate: BuildDate,
				GitCommit: GitCommit,
			}
			return printVersionResult(res, output.Format(outputFormat))
		},
	}

	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

func printVersionResult(res versionResult, format output.Format) error {
	switch format {
	case output.FormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	case output.FormatYAML:
		return output.Print(os.Stdout, output.FormatYAML, res)
	default:
		if term.IsTerminal(int(os.Stdout.Fd())) {
			fmt.Println(apeMascot)
		}
		fmt.Printf("ape %s\n", res.Version)
		fmt.Printf("  build date: %s\n", res.BuildDate)
		fmt.Printf("  git commit: %s\n", res.GitCommit)
		return nil
	}
}

// apeMascot is the ASCII-art ape printed by `ape version` on
// interactive terminals. Pipes / redirects / non-human output
// formats skip it so machine-readable output stays clean.
//
//nolint:gosmopolitan // intentional: Han glyph "三" is part of the mascot art, not a locale concern
const apeMascot = ` ／三ヽ
(6(･･|)
|　( ┴)
/ ~~~ \`
