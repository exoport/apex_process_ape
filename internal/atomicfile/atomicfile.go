// Package atomicfile writes a file via a temp file and a rename, so a
// crash mid-write cannot leave a truncated document behind.
//
// This matters wherever ape rewrites something a person authored: a story
// header, a record's frontmatter. `os.WriteFile` truncates first and then
// writes, so an interrupted call destroys the original and leaves nothing
// in its place — on exactly the files whose loss is least recoverable.
//
// WHAT "ATOMIC" MEANS HERE, precisely, because the word is often claimed
// for less. A reader sees either the whole old file or the whole new one,
// never a partial write — that is the rename. And after a crash the file
// on disk is one of those two, never a short one — that is the fsync,
// which the rename alone does not give you: a rename can reach the disk
// before the data it points at, leaving a file that is present, correct in
// name, and empty. Both are needed and both are here.
//
// The temp file is synced before the rename, and the directory after it,
// so the new name is durable too. That is three syscalls on a path that
// runs a few dozen times per repair, against a failure mode whose cost is
// a document nobody can reconstruct.
//
// `internal/deferred` carries its own copy predating this package. It is
// tested and identical in behaviour except for these syncs; migrating it
// is a separate change with its own risk, and duplicating twenty lines is
// the cheaper of the two mistakes available here.
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
	// Before the rename, not after: the rename publishes the name, and a
	// name published ahead of its bytes is the empty-file case.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync %s: %w", name, err)
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
	return syncDir(dir)
}

// syncDir flushes the directory entry the rename created, so the new name
// survives a crash rather than only the bytes behind it.
//
// Best-effort by design: some filesystems and platforms refuse to open a
// directory for sync, and failing the whole write there would turn a
// durability nicety into an outage on a file that is already correct on
// disk. A real error on the write path has been returned before this runs.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer func() { _ = d.Close() }()
	_ = d.Sync()
	return nil
}
