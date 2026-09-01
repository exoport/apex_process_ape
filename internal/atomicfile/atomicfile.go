// Package atomicfile writes a file via a temp file and a rename, so a
// crash mid-write cannot leave a truncated document behind.
//
// This matters wherever ape rewrites something a person authored: a story
// header, a record's frontmatter. `os.WriteFile` truncates first and then
// writes, so an interrupted call destroys the original and leaves nothing
// in its place — on exactly the files whose loss is least recoverable.
//
// `internal/deferred` carries its own copy predating this package. It is
// tested and identical in behaviour; migrating it is a separate change
// with its own risk, and duplicating twenty lines is the cheaper of the
// two mistakes available here.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Mode is the permission every document ape writes lands with. Named so
// the value is stated once rather than repeated at each call site.
const Mode os.FileMode = 0o644

// Write writes data to path atomically, preserving nothing about the
// previous file but its location.
func Write(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".ape-"+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create temp beside %s: %w", path, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	if err := os.Chmod(name, Mode); err != nil {
		return fmt.Errorf("chmod %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", name, path, err)
	}
	return nil
}
