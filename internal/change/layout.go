package change

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
)

// Layout is the project's folder vocabulary, as the four deny-list roots
// and the two carve-outs from them.
//
// Every path is project-relative and slash-separated, which is the shape
// `git status` speaks and therefore the shape everything here compares
// in. Resolving them through apexcfg rather than hardcoding `development`
// or `_output` is what makes the deny-list mean the same thing on a
// project that renamed its folders.
type Layout struct {
	// The four the lane's ownership condition names. The skill may not
	// edit anything under these.
	Development string
	Apex        string
	Output      string
	Claude      string
	// Evidence is where a goal's gate output goes. It sits UNDER
	// Development on a default project, which is exactly why it is a
	// carve-out and not a fifth root.
	Evidence string
	// Deferred is the store ape writes itself, also under Development.
	Deferred string
}

// LayoutFrom reads the project's resolved config.
func LayoutFrom(cfg *apexcfg.Resolved) (Layout, error) {
	rel := func(abs string) string {
		if abs == "" {
			return ""
		}
		r, err := filepath.Rel(cfg.Root, abs)
		if err != nil {
			return ""
		}
		return path.Clean(filepath.ToSlash(r))
	}
	l := Layout{
		Development: rel(cfg.Paths.Development),
		Apex:        rel(cfg.Paths.Apex),
		Output:      rel(cfg.Paths.Output),
		Claude:      ".claude",
		Deferred:    rel(cfg.Paths.Deferred),
	}
	ev := strings.TrimSpace(cfg.EvidenceFolder)
	if ev == "" {
		// The preflight refuses this earlier. Repeated here because this
		// package is also called from the framework check, where the
		// config comes from a fixture rather than from a real project.
		return Layout{}, errors.New("evidence_folder is not set")
	}
	if filepath.IsAbs(ev) {
		l.Evidence = rel(ev)
	} else {
		l.Evidence = path.Clean(filepath.ToSlash(ev))
	}
	if l.Evidence == "" || l.Evidence == "." || strings.HasPrefix(l.Evidence, "../") {
		return Layout{}, fmt.Errorf("evidence_folder %q does not resolve inside the project", ev)
	}
	return l, nil
}

// Denied reports whether p lies under one of the four roots the skill
// may not touch, ignoring the carve-outs. Callers apply those
// themselves, because which carve-out applies depends on WHICH field of
// the contract the path came from.
func (l Layout) Denied(p string) (root string, denied bool) {
	for _, r := range []string{l.Development, l.Apex, l.Output, l.Claude} {
		if r != "" && within(r, p) {
			return r, true
		}
	}
	return "", false
}

// within reports whether p is root itself or lies beneath it. Both are
// clean, slash-separated, project-relative paths, so this is a prefix
// test with a boundary — `dev` must not match `development/x`.
func within(root, p string) bool {
	if root == "" || p == "" {
		return false
	}
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+"/")
}

// cleanRelPath normalises a path from the contract into the shape git
// reports, or says why it cannot be one.
//
// An absolute path, a `..` escape or a Windows separator all mean the
// same thing here: ape is about to pass this to `git add` as a literal
// pathspec, and a path that does not denote something inside this
// repository is not a path this run may commit.
func cleanRelPath(p string) (string, error) {
	t := strings.TrimSpace(p)
	if t == "" {
		return "", errors.New("empty path")
	}
	if filepath.IsAbs(t) || strings.HasPrefix(t, "/") {
		return "", fmt.Errorf("%q is absolute, and the contract's paths are project-relative", p)
	}
	if strings.Contains(t, `\`) {
		return "", fmt.Errorf("%q holds a backslash: paths are slash-separated, as git reports them", p)
	}
	c := path.Clean(filepath.ToSlash(t))
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("%q climbs out of the project", p)
	}
	return c, nil
}
