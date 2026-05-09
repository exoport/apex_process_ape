package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PreflightError reports a missing prerequisite for a pipeline run.
// The error names the missing path so the user can take corrective
// action (typically: run an upstream pipeline first).
type PreflightError struct {
	Pipeline string
	Missing  []string
}

func (e *PreflightError) Error() string {
	if len(e.Missing) == 1 {
		return fmt.Sprintf("pipeline %q: required file missing: %s", e.Pipeline, e.Missing[0])
	}
	return fmt.Sprintf("pipeline %q: %d required files missing:\n  - %s",
		e.Pipeline, len(e.Missing), strings.Join(e.Missing, "\n  - "))
}

// Preflight verifies the pipeline's prerequisites against the working
// directory. Returns nil if all required files are present, otherwise
// a *PreflightError listing the missing paths.
func Preflight(spec *Spec, projectRoot string) error {
	var missing []string
	for _, rel := range spec.Requires.Files {
		p := filepath.Join(projectRoot, rel)
		if _, err := os.Stat(p); err != nil {
			if os.IsNotExist(err) {
				missing = append(missing, rel)
				continue
			}
			return fmt.Errorf("preflight: stat %s: %w", rel, err)
		}
	}
	if len(missing) > 0 {
		return &PreflightError{Pipeline: spec.Name, Missing: missing}
	}
	return nil
}
