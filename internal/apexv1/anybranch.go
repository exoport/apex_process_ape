package apexv1

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Records from any branch (D51 Q17). Every command that acts on a change
// `ape status` lists finds its records wherever they live, and writes them
// there:
//
//   - a started change's on its branch change/<id> (D38) — the working
//     tree when HEAD is on it, else that branch's tip;
//   - a change with no branch of its own (a draft, an approved fix not
//     started yet) on the base branch — the working tree when HEAD is on
//     the base, else, with HEAD on another change's branch, the base's
//     tip: that branch's copy of the folder is the one it was cut with,
//     and the base has moved on since (a fix planned and approved there).
//
// A write to a branch HEAD is not on is a commit there without a checkout
// (commitOnBranch, editOnBranch): the working tree, its index and HEAD
// stay as they were. start, verify, review and archive keep their branch
// rules (D38): they run the change's code, so HEAD must be where it is.

// baseView is the base branch's changes, as seen from another change's
// branch: the base's name, and the ids under its changes/ and archive/
// (archived maps them as ArchivedIDs does).
type baseView struct {
	branch   string
	changes  map[string]bool
	archived map[string]string
}

// baseViewOf is the base view when HEAD is on a change's branch whose
// base branch exists, else nil.
func (p *Project) baseViewOf(ctx context.Context) *baseView {
	if p.HeadChange(ctx) == "" {
		return nil
	}
	base, err := p.baseBranch(ctx)
	if err != nil || !p.branchExists(ctx, base) {
		return nil
	}
	v := &baseView{branch: base, changes: map[string]bool{}}
	if out, err := git(ctx, p.Root, "ls-tree", "--name-only", "refs/heads/"+base+":./"+p.Rel(p.ChangesDir())); err == nil {
		for name := range strings.SplitSeq(out, "\n") {
			if name != "" && name != "archive" && !strings.HasPrefix(name, ".") {
				v.changes[name] = true
			}
		}
	}
	v.archived = p.archivedOn(ctx, base)
	return v
}

// stateBytes is st as SaveState writes it.
func stateBytes(st *State) ([]byte, error) {
	if st.Accepted == nil {
		st.Accepted = []Accept{}
	}
	if st.Baseline == nil {
		st.Baseline = []Baseline{}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// saveChangeState commits st as ci's state.json with subject (trailer
// "Change: <id>") where ci was read: the working tree (saveAndCommitSubject)
// or, when ci.Branch is set, that branch without a checkout — refused
// (git.ref-moved) when the state there is no longer the one ci was read
// with, so a write made meanwhile is never overwritten.
func (p *Project) saveChangeState(ctx context.Context, ci ChangeInfo, st *State, subject string) error {
	if ci.Branch == "" {
		return p.saveAndCommitSubject(ctx, ci.ID, st, subject)
	}
	rel := p.Rel(p.StateFile(ci.ID))
	want := ci.State.Status
	_, err := p.editOnBranch(ctx, ci.Branch, subject+"\n\nChange: "+ci.ID, []string{rel}, func(cur map[string][]byte) (map[string][]byte, error) {
		if cur[rel] == nil {
			return nil, &FailError{Code: "change.no-state", Msg: "change " + ci.ID + " has no state.json on " + ci.Branch}
		}
		if s, err := parseState(rel+" on "+ci.Branch, cur[rel]); err != nil || s.Status != want {
			return nil, &FailError{Code: CodeRefMoved, Msg: "change " + ci.ID + "'s state on " + ci.Branch + " changed while ape wrote it — nothing was written; run the command again"}
		}
		data, err := stateBytes(st)
		if err != nil {
			return nil, err
		}
		return map[string][]byte{rel: data}, nil
	})
	return err
}

// viewAt is the project as branch holds it, for a read-only command on a
// change whose records are not in the working tree (check, diff, approve's
// check): the development folder and _apex/ at the branch's tip, extracted
// to a temporary directory that done removes. Outside git, or for "", the
// project itself.
func (p *Project) viewAt(ctx context.Context, branch string) (view *Project, done func(), err error) {
	if branch == "" {
		return p, func() {}, nil
	}
	tree := "refs/heads/" + branch + ":./"
	var paths []string
	for _, rel := range []string{p.DevRel, "_apex"} {
		if _, err := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", tree+rel); err == nil {
			paths = append(paths, rel)
		}
	}
	tmp, err := os.MkdirTemp("", "ape-view-")
	if err != nil {
		return nil, nil, err
	}
	done = func() { _ = os.RemoveAll(tmp) }
	out, err := gitRaw(ctx, p.Root, append([]string{"archive", "--format=tar", tree}, paths...)...)
	if err == nil {
		err = untar(tmp, []byte(out))
	}
	if err == nil {
		view, err = Open(tmp)
	}
	if err != nil {
		done()
		return nil, nil, &FailError{Code: CodeBranchFailed, Msg: "cannot read the project as " + branch + " holds it: " + err.Error() + " — run the command on " + branch}
	}
	return view, done, nil
}

// untar extracts the regular files and directories of a tar stream under
// dir.
func untar(dir string, data []byte) error {
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if !filepath.IsLocal(h.Name) {
			continue
		}
		path := filepath.Join(dir, filepath.FromSlash(h.Name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			content, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, content, 0o600); err != nil {
				return err
			}
		}
	}
}

// changeBranchOf is the branch holding change id's records when they are
// not in the working tree (requireChange's ChangeInfo.Branch), "" when
// they are or the change is unknown.
func (p *Project) changeBranchOf(ctx context.Context, id string) string {
	ci, err := p.requireChange(ctx, id)
	if err != nil {
		return ""
	}
	return ci.Branch
}

// CheckChangeAnywhere is CheckChange of the change as the branch holding
// its records has it (D51 Q17).
func (p *Project) CheckChangeAnywhere(ctx context.Context, id string) []Finding {
	view, done, err := p.viewAt(ctx, p.changeBranchOf(ctx, id))
	if err != nil {
		return p.CheckChange(id)
	}
	defer done()
	return view.CheckChange(id)
}
