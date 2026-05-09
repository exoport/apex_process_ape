package apecmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/diegosz/apex_process_ape/internal/output"
	"github.com/spf13/cobra"
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
		fmt.Printf("ape %s\n", res.Version)
		fmt.Printf("  build date: %s\n", res.BuildDate)
		fmt.Printf("  git commit: %s\n", res.GitCommit)
		return nil
	}
}
