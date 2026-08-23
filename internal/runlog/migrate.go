package runlog

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// MigrationResult reports what a Migrate call did.
type MigrationResult struct {
	// Moved names each run directory relocated, as "<kind>/<group>/<runID>".
	Moved []string `json:"moved,omitempty" yaml:"moved,omitempty"`
	// Conflicts names run directories left in place because something
	// already occupied the destination. Never overwritten, never merged.
	Conflicts []string `json:"conflicts,omitempty" yaml:"conflicts,omitempty"`
}

// Pending reports whether any legacy run tree still holds runs.
//
// Cheap enough for a doctor check: it stops at the first run directory it
// finds rather than enumerating the whole tree.
func Pending(projectRoot string) bool {
	for _, rel := range LegacyRunRoots(projectRoot) {
		if !rel.Grouped {
			if hasRunDir(rel.From) {
				return true
			}
			continue
		}
		groups, err := os.ReadDir(rel.From)
		if err != nil {
			continue
		}
		for _, g := range groups {
			if g.IsDir() && hasRunDir(filepath.Join(rel.From, g.Name())) {
				return true
			}
		}
	}
	return false
}

// hasRunDir reports whether dir holds at least one subdirectory — one run.
// An empty legacy tree is not pending work: there is nothing in it to
// move, and reporting it would nag forever on a project whose runs were
// deleted by hand.
func hasRunDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			return true
		}
	}
	return false
}

// Migrate relocates run artifacts from the pre-`_output/ape` layout into
// it: `_output/pipelines/<name>/<runID>` becomes
// `_output/ape/pipelines/<name>/<runID>`, and likewise for tasks.
//
// This moves a user's history — the manifests `ape costs` reads and the
// run ids their reports quote — so it is deliberately conservative:
//
//   - it moves whole run directories, never individual files, so a run is
//     either fully at the old path or fully at the new one and never half
//     of each;
//   - a destination that already exists is a CONFLICT: the source is left
//     untouched and reported. Two runs sharing an id is not something to
//     resolve by guessing, and silently overwriting one would destroy the
//     record it exists to preserve;
//   - it is idempotent. A project already migrated has no legacy tree to
//     read and reports nothing;
//   - it removes a legacy directory only once it is empty, so anything it
//     could not move keeps its containing tree.
//
// A rename across filesystems fails with EXDEV; that falls back to
// copy-then-remove, which is why this does not simply call os.Rename on
// the whole root.
func Migrate(projectRoot string) (MigrationResult, error) {
	var res MigrationResult
	for _, rel := range LegacyRunRoots(projectRoot) {
		if err := migrateRoot(rel, &res); err != nil {
			return res, err
		}
	}
	pruneLegacyParents(projectRoot)
	return res, nil
}

// pruneLegacyParents removes the emptied `_output/ape` and `_output`
// husks a relocation can leave behind on a project that renamed
// output_folder.
//
// It never touches the LIVE output folder, even when empty: that
// directory is the framework's, and deleting it because ape happened to
// find it empty would be ape reaching outside its own subtree — the exact
// thing this whole change is about.
func pruneLegacyParents(projectRoot string) {
	legacy := filepath.Join(projectRoot, DefaultOutputDirName)
	if filepath.Clean(legacy) == filepath.Clean(OutputRoot(projectRoot)) {
		return // _output IS the output folder — not ours to remove
	}
	pruneIfEmpty(filepath.Join(legacy, ApeDirName))
	pruneIfEmpty(legacy)
}

func migrateRoot(rel Relocation, res *MigrationResult) error {
	// Ungrouped trees (prompts, chats) hold run dirs directly.
	if !rel.Grouped {
		if err := migrateGroup(rel, "", res); err != nil {
			return err
		}
		pruneIfEmpty(rel.From)
		return nil
	}
	groups, err := os.ReadDir(rel.From)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // nothing to migrate — the common case
		}
		return fmt.Errorf("read %s: %w", rel.From, err)
	}
	for _, g := range groups {
		if !g.IsDir() {
			// A stray file at the group level is not a run. Leave it be
			// rather than guessing where it belongs.
			continue
		}
		if err := migrateGroup(rel, g.Name(), res); err != nil {
			return err
		}
	}
	// Only succeeds once every run and group beneath it is gone.
	pruneIfEmpty(rel.From)
	return nil
}

// migrateGroup moves the runs in one group. group is "" for an ungrouped
// tree, where the runs sit directly under rel.From.
func migrateGroup(rel Relocation, group string, res *MigrationResult) error {
	srcGroup, dstGroup := rel.From, rel.To
	if group != "" {
		srcGroup = filepath.Join(rel.From, group)
		dstGroup = filepath.Join(rel.To, group)
	}
	runs, err := os.ReadDir(srcGroup)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", srcGroup, err)
	}
	for _, r := range runs {
		name := r.Name()
		src := filepath.Join(srcGroup, name)
		dst := filepath.Join(dstGroup, name)
		label := filepath.Join(rel.Kind, group, name)

		// `latest` is a symlink into a sibling run dir. Recreating it
		// after the runs have moved is simpler and safer than trying to
		// move a link whose target is also moving.
		if r.Type()&os.ModeSymlink != 0 {
			if err := relinkSymlink(src, dst, dstGroup); err != nil {
				return err
			}
			continue
		}
		if !r.IsDir() {
			continue
		}
		if _, err := os.Lstat(dst); err == nil {
			res.Conflicts = append(res.Conflicts, label)
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", dst, err)
		}
		if err := os.MkdirAll(dstGroup, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dstGroup, err)
		}
		if err := moveDir(src, dst); err != nil {
			return fmt.Errorf("move %s: %w", label, err)
		}
		res.Moved = append(res.Moved, label)
	}
	pruneIfEmpty(srcGroup)
	return nil
}

// relinkSymlink recreates src's link at dst with the same (relative)
// target and drops the original. A link whose target cannot be read is
// left alone: it was already broken, and inventing a destination for it
// would turn a visible stale link into an invisible one.
func relinkSymlink(src, dst, dstGroup string) error {
	target, err := os.Readlink(src)
	if err != nil {
		return nil //nolint:nilerr // an unreadable link is skipped, never fatal to the migration
	}
	if err := os.MkdirAll(dstGroup, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dstGroup, err)
	}
	_ = os.Remove(dst)
	if err := os.Symlink(target, dst); err != nil {
		return nil //nolint:nilerr // could not relink; leave the original in place
	}
	_ = os.Remove(src)
	return nil
}

// moveDir renames src to dst, falling back to a copy when the two are on
// different filesystems (os.Rename returns EXDEV, which a project whose
// _output is a mount or a symlinked volume can genuinely hit).
func moveDir(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyTree(src, dst); err != nil {
		// Leave the partial destination behind rather than deleting it:
		// the source is still intact, and a half-copy the user can inspect
		// beats a cleanup that races the error being reported.
		return err
	}
	return os.RemoveAll(src)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type()&os.ModeSymlink != 0:
			link, lerr := os.Readlink(path)
			if lerr != nil {
				return lerr
			}
			return os.Symlink(link, target)
		default:
			return copyFile(path, target)
		}
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// pruneIfEmpty removes dir when nothing is left in it. Best-effort: a
// directory that still holds something the migration declined to move
// stays, which is the point.
//
// "Empty" here means NO ENTRIES AT ALL — files included. That is deliberate
// and it is not the same test hasRunDir applies a few lines up, which counts
// only subdirectories. The two look unifiable and are not: hasRunDir answers
// "is there a run still to move?", where a loose file is not a run; this
// answers "may I delete this directory?", where a loose file is somebody's
// data. The framework writes loose files at the output-folder root —
// defer-*, retro-epic-*, data-architecture-ripple-* and friends — so a
// prune that ignored files would delete them. Do not make these agree.
func pruneIfEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 0 {
		return
	}
	_ = os.Remove(dir)
}
