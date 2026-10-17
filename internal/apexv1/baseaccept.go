package apexv1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"path"
	"path/filepath"
	"strings"
)

// Accepting a reviewed change from the base branch (D52 Q18). Evidence a
// person commits on the base does not exist on the change's branch, and
// evidence committed there makes the review stale (code moved since it).
// So `ape change accept` of a reviewed change with HEAD off its branch
// records the acceptance on the base: in its own file beside the draft,
// development/changes/<id>/accepted.json ({"accepted": [{task, by, at,
// evidence}]}), committed there as "apex: <id> accepted <T>" — never in
// the base's state.json, which the change's branch rewrites and the
// archive's squash would conflict on. Its --evidence paths are resolved
// at the base's tip: committed there, recorded with that content's
// SHA-256.
//
// Every reader of a change's acceptances merges the file (withBaseAccepted):
// the archive's pending-human check, awaiting_human and pending_human in
// `ape status`, a milestone's close, verify's pending list. The archive
// folds it into the archived state.json and drops it with the folder. It
// is development/, so it never makes a verify or a review stale, and
// recordsOnlyMoved counts it as ape's record: a base moved only by it does
// not make the change catch up.

// acceptedFile is the base-side acceptance record's name, in a change's
// folder.
const acceptedFile = "accepted.json"

// baseAccepted is accepted.json.
type baseAccepted struct {
	Accepted []Accept `json:"accepted"`
}

// acceptedRel is change id's accepted.json, project-relative.
func (p *Project) acceptedRel(id string) string {
	return p.Rel(filepath.Join(p.ChangeDir(id), acceptedFile))
}

// isAcceptedRecord reports a project-relative path that is some change's
// accepted.json.
func (p *Project) isAcceptedRecord(rel string) bool {
	dir, file := path.Split(rel)
	parent, _ := path.Split(strings.TrimSuffix(dir, "/"))
	return file == acceptedFile && parent == p.Rel(p.ChangesDir())+"/"
}

// parseBaseAccepted reads accepted.json's content; none when absent or
// unreadable.
func parseBaseAccepted(data []byte) []Accept {
	var b baseAccepted
	if len(data) == 0 || json.Unmarshal(data, &b) != nil {
		return nil
	}
	return b.Accepted
}

// mergeAccepted is own with extra folded in: per task, the later
// acceptance wins (RFC 3339 UTC stamps compare as strings).
func mergeAccepted(own, extra []Accept) []Accept {
	out := append([]Accept{}, own...)
	for _, e := range extra {
		i := -1
		for j := range out {
			if out[j].Task == e.Task {
				i = j
			}
		}
		switch {
		case i < 0:
			out = append(out, e)
		case e.At >= out[i].At:
			out[i] = e
		}
	}
	return out
}

// withBaseAccepted is st with the acceptances its base branch records in
// accepted.json folded in — a copy, st itself when there are none. A
// change with no base branch recorded has none.
func (p *Project) withBaseAccepted(ctx context.Context, id string, st *State) *State {
	if st == nil || st.BaseBranch == "" || !p.isWorkTree(ctx) {
		return st
	}
	data, ok := p.readOnBranch(ctx, st.BaseBranch, p.acceptedRel(id))
	extra := parseBaseAccepted(data)
	if !ok || len(extra) == 0 {
		return st
	}
	merged := *st
	merged.Accepted = mergeAccepted(st.Accepted, extra)
	return &merged
}

// acceptOnBase records a person accepting task of reviewed change ci from
// off its branch: accepted.json on the change's base branch, from any
// branch, without a checkout when HEAD is elsewhere (editOnBranch). It
// returns the change's state with the base's acceptances folded in.
func (p *Project) acceptOnBase(ctx context.Context, ci ChangeInfo, a acceptance) (*State, error) {
	id, base := ci.ID, ci.State.BaseBranch
	if base == "" || !p.branchExists(ctx, base) {
		return nil, &FailError{Code: CodeBranchFailed, Msg: "the base branch of " + id + " (" + base + ") is not recorded or does not exist — `git switch " + ChangeBranch(id) + "` and accept there"}
	}
	ev, err := a.evidence(func(items []string) ([]Evidence, error) { return p.resolveEvidenceAt(ctx, base, items) })
	if err != nil {
		return nil, err
	}
	notes, err := p.readNotes(a.notes)
	if err != nil {
		return nil, err
	}
	rel := p.acceptedRel(id)
	var recorded []Accept
	_, err = p.editOnBranch(ctx, base, "apex: "+id+" accepted "+a.task+"\n\n"+a.trailers(id), []string{rel}, func(cur map[string][]byte) (map[string][]byte, error) {
		recorded = mergeAccepted(parseBaseAccepted(cur[rel]), []Accept{a.record(ev)})
		data, err := json.MarshalIndent(baseAccepted{Accepted: recorded}, "", "  ")
		if err != nil {
			return nil, err
		}
		files := map[string][]byte{rel: append(data, '\n')}
		maps.Copy(files, notes)
		return files, nil
	})
	if err != nil {
		return nil, err
	}
	p.dropCommittedNotes(ctx, base, notes)
	st := *ci.State
	st.Accepted = mergeAccepted(st.Accepted, recorded)
	refreshPending(&st, ci.Doc)
	return &st, nil
}

// resolveEvidenceAt is resolveEvidence at branch's tip: a URL as given; a
// path — relative to the project root, or absolute inside the repository
// — must be a file committed on branch, recorded with that content's
// SHA-256.
func (p *Project) resolveEvidenceAt(ctx context.Context, branch string, items []string) ([]Evidence, error) {
	var out []Evidence
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if urlRe.MatchString(item) {
			out = append(out, Evidence{URL: item})
			continue
		}
		abs := item
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(p.Root, filepath.FromSlash(item))
		}
		rel := p.Rel(filepath.Clean(abs))
		rev := "refs/heads/" + branch + ":./" + rel
		if kind, err := git(ctx, p.Root, "cat-file", "-t", rev); err != nil || kind != "blob" {
			return nil, &UsageError{Code: "usage.evidence", Msg: "--evidence " + item + " is neither a URL (scheme://…) nor a file committed on " + branch +
				" — commit it on " + branch + " first (a path relative to the project root), or give a URL"}
		}
		data, err := gitRaw(ctx, p.Root, "cat-file", "blob", rev)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256([]byte(data))
		out = append(out, Evidence{Path: rel, SHA256: hex.EncodeToString(sum[:])})
	}
	return out, nil
}
