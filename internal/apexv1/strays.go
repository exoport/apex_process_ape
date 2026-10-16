package apexv1

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Item 19 (F3): a check command that leaves files behind — a coverage
// report, a scratch binary, a temp dir that git does not ignore — makes the
// tree dirty, and verify and archive then refuse it as uncommitted code,
// though nobody wrote code. The checks are ape's to run, so their litter is
// ape's to clean: each check runs between two snapshots of git's view of
// the tree, and what the check alone created or changed outside the
// development folder is undone and reported.
//
// Snapshots rather than a disposable worktree at the verified sha: a
// check may rely on ignored state in the working tree (node_modules, a
// virtualenv, a build cache) that a fresh worktree would not have.

// treeSnapshot is git's view of the tree outside the development folder:
// every untracked, not-ignored file, and every tracked path that differs
// from HEAD.
type treeSnapshot struct {
	untracked map[string]bool
	modified  map[string]bool
	ok        bool
}

func (p *Project) snapshot(ctx context.Context) treeSnapshot {
	s := treeSnapshot{untracked: map[string]bool{}, modified: map[string]bool{}}
	out, err := gitRaw(ctx, p.Root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return s
	}
	// Porcelain v1: two status columns, a space, then the path.
	const pathAt = 3
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) <= pathAt {
			continue
		}
		code, path := e[:2], e[pathAt:]
		if code[0] == 'R' || code[0] == 'C' {
			i++ // -z puts a rename's source in the next entry
		}
		if p.notCode(path) {
			continue
		}
		if code == "??" {
			s.untracked[path] = true
		} else {
			s.modified[path] = true
		}
	}
	s.ok = true
	return s
}

// undoStrays removes what a check left: untracked files new since before,
// and tracked files it changed that were clean before (restored from the
// index/HEAD). Returns the paths, sorted. A path dirty before the check is
// never touched: it is not the check's.
func (p *Project) undoStrays(ctx context.Context, before treeSnapshot) []string {
	if !before.ok {
		return nil
	}
	after := p.snapshot(ctx)
	if !after.ok {
		return nil
	}
	var strays []string
	var restore []string
	for path := range after.untracked {
		if before.untracked[path] {
			continue
		}
		abs := filepath.Join(p.Root, filepath.FromSlash(path))
		if err := os.Remove(abs); err == nil || os.IsNotExist(err) {
			strays = append(strays, path)
			p.pruneEmptyParents(filepath.Dir(abs))
		}
	}
	for path := range after.modified {
		if before.modified[path] {
			continue
		}
		restore = append(restore, path)
	}
	if len(restore) > 0 {
		sort.Strings(restore)
		if _, err := git(ctx, p.Root, append([]string{"checkout", "HEAD", "--"}, restore...)...); err == nil {
			strays = append(strays, restore...)
		}
	}
	sort.Strings(strays)
	return strays
}

// pruneEmptyParents removes empty directories from dir up to (not
// including) the project root. git tracks no empty directory, so none of
// these can be anyone's work.
func (p *Project) pruneEmptyParents(dir string) {
	root := filepath.Clean(p.Root)
	for d := filepath.Clean(dir); d != root && strings.HasPrefix(d, root+string(filepath.Separator)); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			return
		}
	}
}
