package apexv1

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The archive's two layouts (D155). archive and abandon put a change's
// folder in archive/changes/<YYYY-MM-DD>-<id>/; an older ape put it in
// archive/<YYYY-MM-DD>-<id>/, beside milestones/. Every reader of an
// archived change goes through archivedLayout, which reads both — always,
// not only while seq-06 is pending, so a project half moved by hand, or
// a branch cut before the move, reads the same (Q75). `ape project
// archive-layout` moves the older layout into the newer one (Q76).

// archivedChangesSub is ArchivedChangesDir's name under archive/.
const archivedChangesSub = "changes"

// ArchiveLayoutSubject is the commit `ape project archive-layout` makes:
// the framework's v1.0.0_seq-06 migration.
const ArchiveLayoutSubject = "apex: archive layout (seq-06)"

// datedFolderRe is an archived change's folder name, <YYYY-MM-DD>-<id>:
// milestones/ and changes/ never match, so they are never a change.
var datedFolderRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-(.+)$`)

// datedFolders maps the change ids of the dated folder names among names
// to their names; of two folders of one id, the later name wins.
func datedFolders(names []string) map[string]string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	out := map[string]string{}
	for _, name := range sorted {
		if m := datedFolderRe.FindStringSubmatch(name); m != nil {
			out[m[1]] = name
		}
	}
	return out
}

// archivedLayout is the one reader of the archive's layouts (Q75): given
// the folder names directly in archive/ (legacy) and in archive/changes/
// (nested), it maps every archived change id to its folder relative to
// archive/, slash-separated: "changes/<date>-<id>", or "<date>-<id>" for
// one still in the older layout. An id in both is read from the newer.
func archivedLayout(legacy, nested []string) map[string]string {
	out := datedFolders(legacy)
	for id, name := range datedFolders(nested) {
		out[id] = archivedChangesSub + "/" + name
	}
	return out
}

// ArchivedIDs maps every archived change id to its folder relative to
// ArchiveDir, in either layout (archivedLayout); archivedPath makes it a
// path.
func (p *Project) ArchivedIDs() map[string]string {
	return archivedLayout(dirNames(p.ArchiveDir()), dirNames(p.ArchivedChangesDir()))
}

// archivedOn is ArchivedIDs as branch's tree holds it.
func (p *Project) archivedOn(ctx context.Context, branch string) map[string]string {
	rel := p.Rel(p.ArchiveDir())
	return archivedLayout(p.treeDirs(ctx, branch, rel), p.treeDirs(ctx, branch, rel+"/"+archivedChangesSub))
}

// archivedPath is the path of an archived change's folder, as ArchivedIDs
// names it.
func (p *Project) archivedPath(folder string) string {
	return filepath.Join(p.ArchiveDir(), filepath.FromSlash(folder))
}

// dirNames lists the folders directly in dir; none when it cannot be read.
func dirNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// treeDirs lists the folders directly in rel (project-relative) on
// branch; none when branch does not hold it.
func (p *Project) treeDirs(ctx context.Context, branch, rel string) []string {
	out, err := git(ctx, p.Root, "ls-tree", "-d", "--name-only", "refs/heads/"+branch+":./"+rel)
	if err != nil {
		return nil
	}
	var names []string
	for name := range strings.SplitSeq(out, "\n") {
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// ArchiveMove is one folder `ape project archive-layout` moves (or, under
// --check, would move), project-relative.
type ArchiveMove struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
}

// ArchiveLayoutResult is what `ape project archive-layout` reports.
type ArchiveLayoutResult struct {
	// Moved are the folders moved, or under --check the ones left to move;
	// empty when the archive is in the newer layout.
	Moved []ArchiveMove `json:"moved"`
	// Rewritten are the moved state.json files whose recorded paths
	// (archived_to, an acceptance's evidence) named the old folder.
	Rewritten []string `json:"rewritten,omitempty"`
	// Commit is the one commit, ArchiveLayoutSubject; "" when nothing
	// moved.
	Commit string `json:"commit,omitempty"`
}

// legacyArchiveMoves is every archived change folder still in the older
// layout, with where it goes.
func (p *Project) legacyArchiveMoves() []ArchiveMove {
	var out []ArchiveMove
	for _, name := range dirNames(p.ArchiveDir()) {
		if m := datedFolderRe.FindStringSubmatch(name); m != nil {
			out = append(out, ArchiveMove{
				ID:   m[1],
				From: p.Rel(filepath.Join(p.ArchiveDir(), name)),
				To:   p.Rel(filepath.Join(p.ArchivedChangesDir(), name)),
			})
		}
	}
	return out
}

// ArchiveLayoutCheck is `ape project archive-layout --check`, the
// migration's check: nil when no archived change is left in the older
// layout, else archive.legacy-layout naming them (exit 1). It writes
// nothing and needs no git.
func (p *Project) ArchiveLayoutCheck() (*ArchiveLayoutResult, error) {
	moves := p.legacyArchiveMoves()
	res := &ArchiveLayoutResult{Moved: moves}
	if len(moves) == 0 {
		return res, nil
	}
	return res, &FailError{Code: "archive.legacy-layout", Msg: strconv.Itoa(len(moves)) + " archived change(s) in the older layout " + p.Rel(p.ArchiveDir()) + "/<date>-<id>/: " +
		strings.Join(limit(movesFrom(moves), 5), ", ") + " — `ape project archive-layout` moves them to " + p.Rel(p.ArchivedChangesDir()) + "/"}
}

func movesFrom(moves []ArchiveMove) []string {
	out := make([]string, len(moves))
	for i, m := range moves {
		out[i] = m.From
	}
	return out
}

// ArchiveLayout is `ape project archive-layout` (Q76, the framework's
// v1.0.0_seq-06): every archived change folder in the older layout is
// moved (git mv) into archive/changes/, each moved state.json's recorded
// paths rewritten to the new folder, in ONE commit, ArchiveLayoutSubject.
// Idempotent: with nothing to move it writes nothing. Refused on a change
// branch, on a dirty tree, and when a folder's new place is taken (exit
// 3, nothing written). The refs (refs/apex/archive/<id>) and tip_ref are
// untouched: they name commits, not folders.
func (p *Project) ArchiveLayout(ctx context.Context) (*ArchiveLayoutResult, error) {
	moves := p.legacyArchiveMoves()
	res := &ArchiveLayoutResult{Moved: moves}
	if len(moves) == 0 {
		return res, nil
	}
	if !p.isWorkTree(ctx) {
		return res, &FailError{Code: "git.not-a-repo", Msg: "not a git repository — the archive layout move is one commit"}
	}
	if err := p.recoverArchive(ctx); err != nil {
		return res, err
	}
	if err := p.refuseOnChangeBranch(ctx, "the archive layout move"); err != nil {
		return res, err
	}
	if dirty, err := gitRaw(ctx, p.Root, "status", "--porcelain=v1", "--untracked-files=normal"); err != nil {
		return res, &FailError{Code: CodeGitStatusFailed, Msg: "cannot read git status: " + err.Error()}
	} else if strings.TrimSpace(dirty) != "" {
		return res, &FailError{Code: "tree.dirty", Msg: "the archive layout move is one commit, and the tree has uncommitted changes — commit or discard them first, then run it again"}
	}
	for _, m := range moves {
		if fileOrDirExists(filepath.Join(p.Root, filepath.FromSlash(m.To))) {
			return res, &ConflictError{Code: CodeFolderExists, Msg: "archive folder " + m.To + " already exists, so " + m.From + " cannot move there — nothing was written; a person merges the two folders by hand, then runs this again"}
		}
	}
	if err := os.MkdirAll(p.ArchivedChangesDir(), 0o755); err != nil {
		return res, err
	}
	archiveRel := p.Rel(p.ArchiveDir())
	// The tree was clean: undoing is putting the folders back and the
	// archive as HEAD has it.
	var done []ArchiveMove
	undo := func(cause string) error {
		for _, m := range slices.Backward(done) {
			_ = os.Rename(filepath.Join(p.Root, filepath.FromSlash(m.To)), filepath.Join(p.Root, filepath.FromSlash(m.From)))
		}
		_, _ = git(ctx, p.Root, "reset", "-q", "--", archiveRel)
		_, _ = git(ctx, p.Root, "checkout", "-q", "--", archiveRel)
		_ = os.Remove(p.ArchivedChangesDir())
		return &FailError{Code: CodeCommitFailed, Msg: "the archive layout move failed: " + cause + " — nothing was committed and the archive is as it was; fix git and run it again"}
	}
	for _, m := range moves {
		if p.tracked(ctx, m.From) {
			if _, err := git(ctx, p.Root, "mv", "--", m.From, m.To); err != nil {
				return res, undo("git mv " + m.From + ": " + err.Error())
			}
		} else if err := os.Rename(filepath.Join(p.Root, filepath.FromSlash(m.From)), filepath.Join(p.Root, filepath.FromSlash(m.To))); err != nil {
			// A folder git does not track (one ignored) moves as it is.
			return res, undo(err.Error())
		}
		done = append(done, m)
		rewritten, err := rewriteArchivedPaths(filepath.Join(p.Root, filepath.FromSlash(m.To), "state.json"), m.From, m.To)
		if err != nil {
			return res, undo(err.Error())
		}
		if rewritten {
			res.Rewritten = append(res.Rewritten, path.Join(m.To, "state.json"))
		}
	}
	sha, err := p.commitRecord(ctx, ArchiveLayoutSubject, strconv.Itoa(len(moves))+" archived change folder(s) moved from "+archiveRel+"/ to "+archiveRel+"/"+archivedChangesSub+"/.", archiveRel)
	if err != nil {
		return res, undo(err.Error())
	}
	res.Commit = sha
	return res, nil
}

// tracked reports whether git tracks anything under rel.
func (p *Project) tracked(ctx context.Context, rel string) bool {
	out, err := git(ctx, p.Root, "ls-files", "--", rel)
	return err == nil && out != ""
}

// rewriteArchivedPaths rewrites, in a moved state.json, every recorded
// path naming the old folder — archived_to, and an acceptance's evidence
// inside the folder — to the new one: the JSON string "<from>" and every
// one starting "<from>/". The rest of the file is kept byte for byte. A
// missing state.json is nothing to rewrite.
func rewriteArchivedPaths(file, from, to string) (bool, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s := strings.NewReplacer(`"`+from+`"`, `"`+to+`"`, `"`+from+`/`, `"`+to+`/`).Replace(string(data))
	if s == string(data) {
		return false, nil
	}
	if _, err := parseState(file, []byte(s)); err != nil {
		return false, err
	}
	return true, writeFileAtomic(file, s)
}
