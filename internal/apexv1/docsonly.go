package apexv1

import (
	"context"
	"errors"
	"path"
	"strings"
)

// Docs-only verify (Q87, D168 (3)): a change whose diff touches nothing
// but the user-facing docs (Config.DocsDefaultScope: release.docs,
// docs.changelog, docs.user_docs) and the development folder skips the
// regression command at verify — only that: its task and scenario checks
// still run, and the milestone gate still runs everything. The touched
// paths are the diff from base_sha to HEAD (verify runs on the change's
// branch, so HEAD is its tip); uncommitted ones need no reading, as verify
// refuses any outside the development folder and ape's state (tree.dirty).
//
// It runs anyway, the change otherwise docs-only, when
//   - _apex/config.yaml's release or docs block — which bound the scope —
//     is not a person's (refuseUnacceptedConfig, as for the docs delegate);
//   - a touched path is under docs.tested (docs the project executes);
//   - the regression command names a touched docs path, or a folder
//     holding one, as a word (`markdownlint docs/`, `test -f README.md`)
//     (D170). The change's own checks are not consulted: they run anyway,
//     and a docs change's task check names the doc it edits (`grep -q wave
//     README.md`), which would end every skip.
//
// There is no skip when the change has no base_sha or its diff cannot be
// read.

// RegressionSkippedDocsOnly is RegressionVerdict.Skipped for a docs-only
// change.
const RegressionSkippedDocsOnly = "docs-only"

// regressionPathsShown is how many touched docs paths a skipped verdict
// carries (it is committed in state.json); paths_total counts them all.
const regressionPathsShown = 50

// docsOnlyRegression decides the docs-only skip for ci, whose verify runs
// the regression command cmd. skip: the change is docs-only and
// nothing stops the skip; paths are then its touched docs paths (outside
// the development folder and ape's state). notSkipped: the change is
// docs-only, yet the command runs — why. Both empty: the change is not
// docs-only.
func (p *Project) docsOnlyRegression(ctx context.Context, ci ChangeInfo, cmd string) (skip bool, paths []string, notSkipped string) {
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return false, nil, ""
	}
	touched, err := p.changeTouched(ctx, ci.ID, ci.State)
	if err != nil || len(p.outsideDocs(cfg.DocsDefaultScope(), touched)) > 0 {
		return false, nil, ""
	}
	for _, f := range touched {
		if !p.notCode(f) {
			paths = append(paths, f)
		}
	}
	if err := p.refuseUnacceptedConfig(ctx, "release", "docs"); err != nil {
		why := err.Error()
		if fe, ok := errors.AsType[*FailError](err); ok {
			why, _, _ = strings.Cut(fe.Msg, " — ")
		}
		return false, paths, why + ", so the docs-only skip does not apply"
	}
	for _, f := range touched {
		if inReleaseDocs(cfg.Docs.Tested, f) {
			return false, paths, "it touches " + f + ", under docs.tested (docs the project executes)"
		}
	}
	if w := namedPath(cmd, paths); w != "" {
		return false, paths, "the regression command names " + w
	}
	return true, paths, ""
}

// namedPath returns the first word of cmd that names one of paths or a
// folder holding one (README.md; docs or docs/ for docs/guide.md; a glob
// under it, docs/**/*.md), "" when none does. A word is cmd split at
// whitespace and shell punctuation; a leading ./ and a trailing / do not
// count.
func namedPath(cmd string, paths []string) string {
	named := map[string]bool{}
	for _, f := range paths {
		for c := path.Clean(f); c != "." && c != "/" && c != ""; c = path.Dir(c) {
			named[c] = true
		}
	}
	words := strings.FieldsFunc(cmd, func(r rune) bool {
		return strings.ContainsRune(" \t\n\r'\"`;|&()<>=,{}$", r)
	})
	for _, w := range words {
		c := strings.TrimPrefix(w, "./")
		c = strings.TrimRight(c, "/")
		if c == "" || c == "." {
			continue
		}
		if named[c] {
			return w
		}
		for d := path.Dir(c); d != "." && d != "/"; d = path.Dir(d) {
			if named[d] {
				return w
			}
		}
	}
	return ""
}
