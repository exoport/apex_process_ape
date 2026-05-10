package framework

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"golang.org/x/mod/modfile"
)

// DefaultProjectName returns the best-effort default for the
// project_name field during config bootstrap, in priority order:
//
//  1. The last path segment of the module path declared in
//     <projectRoot>/go.mod, if present and parseable.
//  2. The base name of projectRoot.
//  3. "my-project" — the framework template default.
func DefaultProjectName(projectRoot string) string {
	if name, ok := projectNameFromGoMod(projectRoot); ok {
		return name
	}
	if base := filepath.Base(projectRoot); base != "" && base != "." && base != string(filepath.Separator) {
		return base
	}
	return "my-project"
}

func projectNameFromGoMod(projectRoot string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(projectRoot, "go.mod"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			// Best-effort: any non-ErrNotExist read failure is treated
			// as "no go.mod for our purposes" and we fall through.
			return "", false
		}
		return "", false
	}
	f, err := modfile.Parse("go.mod", data, nil)
	if err != nil || f.Module == nil || f.Module.Mod.Path == "" {
		return "", false
	}
	// modfile parses module paths as forward-slash-separated regardless
	// of OS, so use path.Base (not filepath.Base) for the last segment.
	return path.Base(f.Module.Mod.Path), true
}
