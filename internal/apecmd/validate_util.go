package apecmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/diegosz/apex_process_ape/internal/output"
)

// mdValidateResult is the structured payload shared by the
// `ape pattern validate` and `ape adr validate` commands: the directory
// scanned and the Markdown files found in it.
type mdValidateResult struct {
	Dir   string   `json:"dir"   yaml:"dir"`
	Count int      `json:"count" yaml:"count"`
	Files []string `json:"files" yaml:"files"`
}

// runMarkdownDirValidate lists the `.md` files in dir and reports them in
// the requested output format. noun labels the human rendering
// ("pattern" / "ADR"). A missing directory is a soft no-op (message to
// stderr, nil error) so `validate` on a fresh project doesn't fail.
func runMarkdownDirValidate(dir, noun, outputFormat string) error {
	if dir == "" {
		fmt.Fprintf(os.Stderr, "no %s directory found\n", noun)
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("cannot read %s dir: %w", noun, err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".md" {
			files = append(files, e.Name())
		}
	}
	res := mdValidateResult{Dir: dir, Count: len(files), Files: files}
	switch format := output.Format(outputFormat); format {
	case output.FormatJSON, output.FormatYAML:
		return output.Print(os.Stdout, format, res)
	default:
		fmt.Printf("Validating %ss in %s\n", noun, dir)
		for _, f := range files {
			fmt.Printf("  OK: %s\n", f)
		}
		fmt.Printf("Validated %d %s file(s).\n", len(files), noun)
		return nil
	}
}
