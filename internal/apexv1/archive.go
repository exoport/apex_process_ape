package apexv1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SpecUpdate is one capability's spec after a delta merge.
type SpecUpdate struct {
	Capability string `json:"capability"`
	Path       string `json:"path"`
	Created    bool   `json:"created"`
	Added      int    `json:"added"`
	Modified   int    `json:"modified"`
	Removed    int    `json:"removed"`
	content    string
}

// MergeDelta applies a change's delta to the current specs in memory.
// Any conflict — ADDED naming an existing requirement, MODIFIED or
// REMOVED naming a missing one — is collected, and nothing is returned
// for writing when there is one.
func (p *Project) MergeDelta(c *Change) (updates []SpecUpdate, conflicts []string) {
	byCap := map[string][]Delta{}
	var order []string
	for i := range c.Deltas {
		d := &c.Deltas[i]
		if _, ok := byCap[d.Capability]; !ok {
			order = append(order, d.Capability)
		}
		byCap[d.Capability] = append(byCap[d.Capability], *d)
	}
	sort.Strings(order)
	for _, capName := range order {
		spec, _, exists := p.LoadSpec(capName)
		u := SpecUpdate{Capability: capName, Path: p.Rel(p.SpecFile(capName)), Created: !exists}
		if !exists {
			spec = Spec{Preamble: "# " + titleFromSlug(capName) + "\nPurpose: " + purposeFrom(c)}
		}
		merged, cs := applyDeltas(&spec, capName, byCap[capName], &u)
		conflicts = append(conflicts, cs...)
		if len(cs) == 0 {
			u.content = merged
			updates = append(updates, u)
		}
	}
	if len(conflicts) > 0 {
		return nil, conflicts
	}
	return updates, nil
}

// applyDeltas merges deltas into spec and returns the rendered result.
func applyDeltas(spec *Spec, capName string, deltas []Delta, u *SpecUpdate) (merged string, conflicts []string) {
	for i := range deltas {
		d := &deltas[i]
		idx := -1
		for i := range spec.Requirements {
			if spec.Requirements[i].Name == d.Name {
				idx = i
				break
			}
		}
		switch d.Op {
		case OpAdded:
			if idx >= 0 {
				conflicts = append(conflicts, fmt.Sprintf("ADDED [%s] %q already exists in the spec", capName, d.Name))
				continue
			}
			spec.Requirements = append(spec.Requirements, d.Req)
			u.Added++
		case OpModified:
			if idx < 0 {
				conflicts = append(conflicts, fmt.Sprintf("MODIFIED [%s] %q is not in the spec", capName, d.Name))
				continue
			}
			if loss := scenarioLoss(&spec.Requirements[idx], &d.Req); len(loss) > 0 {
				conflicts = append(conflicts, fmt.Sprintf("MODIFIED [%s] %q %s of the current spec", capName, d.Name, strings.Join(loss, "; ")))
				continue
			}
			spec.Requirements[idx] = d.Req
			u.Modified++
		case OpRemoved:
			if idx < 0 {
				conflicts = append(conflicts, fmt.Sprintf("REMOVED [%s] %q is not in the spec", capName, d.Name))
				continue
			}
			spec.Requirements = append(spec.Requirements[:idx], spec.Requirements[idx+1:]...)
			u.Removed++
		}
	}
	return spec.Render(), conflicts
}

// purposeFrom is a new capability's Purpose line: the first paragraph of
// the introducing change's `## Why`, flattened to one line.
func purposeFrom(c *Change) string {
	if s := c.section("Why"); s != nil {
		if para := firstParagraph(s.Body); para != "" {
			return para
		}
	}
	return "Introduced by change " + c.Meta.ID + "."
}

// firstParagraph is the first run of non-blank lines outside HTML
// comments, joined with spaces.
func firstParagraph(body []mdLine) string {
	var para []string
	inComment := false
	for _, l := range body {
		t := strings.TrimSpace(l.Text)
		if strings.HasPrefix(t, "<!--") || inComment {
			inComment = !strings.Contains(t, "-->")
			continue
		}
		if t == "" {
			if len(para) > 0 {
				break
			}
			continue
		}
		para = append(para, t)
	}
	return strings.Join(para, " ")
}

// ArchiveResult is what `ape change archive` reports.
type ArchiveResult struct {
	ID                string        `json:"id"`
	ArchivedTo        string        `json:"archived_to"`
	Specs             []SpecUpdate  `json:"specs"`
	Commit            string        `json:"commit"`
	Reverified        bool          `json:"reverified"`
	Verify            *VerifyResult `json:"verify,omitempty"`
	AllowPendingHuman bool          `json:"allow_pending_human,omitempty"`
	PendingHuman      []string      `json:"pending_human,omitempty"`
}

// Archive applies a reviewed change's delta to specs/, moves the change
// to changes/archive/<date>-<id>/ and commits both as `archive: <id>`.
func (p *Project) Archive(ctx context.Context, id string, allowPendingHuman bool, opt RunOptions) (*ArchiveResult, error) {
	ci, err := p.requireChange(id)
	if err != nil {
		return nil, err
	}
	st := ci.State
	if st.Status != StatusReviewed || st.Review == nil || st.Review.Verdict != "pass" {
		return nil, &FailError{Code: CodeWrongStatus, Msg: "change " + id + " is " + st.Status + " — archive needs a green verify and a review pass (`ape change review " + id + " --verdict pass`)"}
	}
	if _, err := git(ctx, p.Root, "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil, &FailError{Code: "git.not-a-repo", Msg: "not a git repository — archive commits its result"}
	}
	if err := checkPlanFrozen(ci); err != nil {
		return nil, err
	}
	res := &ArchiveResult{ID: id}

	// Verified at HEAD: re-run when code has moved since the verify. ape's
	// own state commits (D5) move HEAD on every status change, so "HEAD
	// moved" means "something outside the development folder changed".
	codeMoved := st.Verify == nil
	if !codeMoved {
		codeMoved, _, _ = p.codeMovedSince(ctx, st.Verify.SHA)
	}
	if codeMoved {
		v, err := p.verify(ctx, ci, opt)
		if err != nil {
			return nil, err
		}
		res.Reverified, res.Verify = true, v
		if !v.Green {
			return res, &FailError{Code: "verify.red", Msg: "re-verify at HEAD is red (" + failingNames(v) + ") — the change is back in-progress"}
		}
		st = ci.State
	} else if dirty, _ := p.dirtyCode(ctx); len(dirty) > 0 {
		return nil, &FailError{Code: "tree.dirty", Msg: "uncommitted changes outside " + p.DevRel + "/: " + strings.Join(limit(dirty, 8), ", ") + " — commit or discard them first"}
	}
	if moved, files, _ := p.codeMovedSince(ctx, st.Review.SHA); moved {
		return res, &FailError{Code: "review.stale", Msg: "code changed since the review (" + strings.Join(limit(files, 5), ", ") + ") — review again (apex-review), then archive"}
	}

	pending := st.PendingHuman(ci.Doc.HumanTasks())
	if len(pending) > 0 && !allowPendingHuman {
		return res, &FailError{Code: "archive.pending-human", Msg: "human-review tasks not accepted: " + strings.Join(pending, ", ") +
			" — a person runs `ape change accept " + id + " --task <T> --by <who>`, or archive with --allow-pending-human (recorded)"}
	}

	if fc := p.fingerprintConflicts(st.BaseFingerprints, ci.Doc); len(fc) > 0 {
		return res, &ConflictError{Code: "archive.base-changed", Msg: "requirements this change modifies or removes were changed by another change since it started — nothing was written; " +
			"re-plan against the current spec and re-start (`ape change start " + id + "`)", Conflicts: fc}
	}
	updates, conflicts := p.MergeDelta(ci.Doc)
	if len(conflicts) > 0 {
		return res, &ConflictError{Code: "archive.delta-conflict", Msg: "the delta no longer applies to specs/ — nothing was written; re-plan the change against the current specs", Conflicts: conflicts}
	}
	folder := Today() + "-" + id
	dest := filepath.Join(p.ArchiveDir(), folder)
	if fileOrDirExists(dest) {
		return res, &ConflictError{Code: CodeFolderExists, Msg: "archive folder " + p.Rel(dest) + " already exists — nothing was written"}
	}
	for _, u := range updates {
		if fs := ValidateSpec(u.Path, u.content); len(fs) > 0 {
			return res, &FailError{Code: "archive.spec-invalid", Msg: "merged spec " + u.Path + " would not pass `ape project check` — nothing was written", Findings: fs}
		}
	}

	// Everything is decided; write.
	for _, u := range updates {
		if err := writeFileAtomic(filepath.Join(p.Root, filepath.FromSlash(u.Path)), u.content); err != nil {
			return res, err
		}
	}
	trackedBefore, _ := git(ctx, p.Root, "ls-files", "--", p.Rel(p.ChangeDir(id)))
	if err := os.MkdirAll(p.ArchiveDir(), 0o755); err != nil {
		return res, err
	}
	if err := os.Rename(p.ChangeDir(id), dest); err != nil {
		return res, err
	}
	st.Status = StatusArchived
	st.ArchivedAt = Stamp()
	st.ArchivedTo = p.Rel(dest)
	if len(pending) > 0 {
		st.AllowPendingHuman = true
		res.AllowPendingHuman = true
		res.PendingHuman = pending
	}
	if err := SaveState(filepath.Join(dest, "state.json"), st); err != nil {
		return res, err
	}
	res.ArchivedTo = p.Rel(dest)
	res.Specs = updates

	paths := []string{p.Rel(dest)}
	if dirExists(p.SpecsDir()) {
		paths = append(paths, p.Rel(p.SpecsDir()))
	}
	if trackedBefore != "" {
		paths = append(paths, p.Rel(p.ChangeDir(id)))
	}
	sha, err := p.commitPaths(ctx, "archive: "+id, id, paths...)
	if err != nil {
		return res, &FailError{Code: CodeCommitFailed, Msg: "archived on disk but the commit failed: " + err.Error() + " — commit " + strings.Join(paths, " ") + " yourself"}
	}
	res.Commit = sha
	return res, nil
}

// AbandonResult is what `ape change abandon` reports.
type AbandonResult struct {
	ID         string `json:"id"`
	ArchivedTo string `json:"archived_to"`
	Reason     string `json:"reason"`
	Commit     string `json:"commit"`
}

// Abandon moves a change to the archive as abandoned, merging nothing,
// and commits the move as `apex: <id> abandoned` (trial decision D6).
func (p *Project) Abandon(ctx context.Context, id, reason string) (*AbandonResult, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, &UsageError{Code: "usage.reason", Msg: "--reason is required: why the change is dropped, and what (if anything) replaces it"}
	}
	ci, err := p.requireChange(id)
	if err != nil {
		return nil, err
	}
	folder := Today() + "-" + id
	dest := filepath.Join(p.ArchiveDir(), folder)
	if fileOrDirExists(dest) {
		return nil, &ConflictError{Code: CodeFolderExists, Msg: "archive folder " + p.Rel(dest) + " already exists — nothing was written"}
	}
	inGit := p.isWorkTree(ctx)
	trackedBefore := ""
	if inGit {
		trackedBefore, _ = git(ctx, p.Root, "ls-files", "--", p.Rel(p.ChangeDir(id)))
	}
	if err := os.MkdirAll(p.ArchiveDir(), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(p.ChangeDir(id), dest); err != nil {
		return nil, err
	}
	st := ci.State
	st.Status = StatusAbandoned
	st.AbandonedReason = reason
	st.ArchivedAt = Stamp()
	st.ArchivedTo = p.Rel(dest)
	if err := SaveState(filepath.Join(dest, "state.json"), st); err != nil {
		return nil, err
	}
	res := &AbandonResult{ID: id, ArchivedTo: p.Rel(dest), Reason: reason}
	if !inGit {
		return res, nil
	}
	paths := []string{p.Rel(dest)}
	if trackedBefore != "" {
		paths = append(paths, p.Rel(p.ChangeDir(id)))
	}
	sha, err := p.commitPaths(ctx, "apex: "+id+" "+StatusAbandoned, id, paths...)
	if err != nil {
		return res, &FailError{Code: CodeCommitFailed, Msg: "abandoned on disk but the commit failed: " + err.Error() + " — commit " + strings.Join(paths, " ") + " yourself"}
	}
	res.Commit = sha
	return res, nil
}

func failingNames(v *VerifyResult) string {
	failing := v.Failing()
	names := make([]string, 0, len(failing))
	for i := range failing {
		names = append(names, fmt.Sprintf("%s exit %d", failing[i].Task, failing[i].Exit))
	}
	return strings.Join(names, ", ")
}

// requirementFingerprint is the SHA-256 of a requirement block, trailing
// whitespace trimmed per line.
func requirementFingerprint(r *Requirement) string {
	lines := strings.Split(r.Text, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	sum := sha256.Sum256([]byte(strings.TrimRight(strings.Join(lines, "\n"), "\n")))
	return hex.EncodeToString(sum[:])
}

// baseFingerprints fingerprints the current spec block of every requirement
// c MODIFIES or REMOVES.
func (p *Project) baseFingerprints(c *Change) map[string]string {
	out := map[string]string{}
	for i := range c.Deltas {
		d := &c.Deltas[i]
		if d.Op == OpAdded {
			continue
		}
		spec, _, ok := p.LoadSpec(d.Capability)
		if !ok {
			continue
		}
		if r := spec.Find(d.Name); r != nil {
			out[d.Capability+"/"+d.Name] = requirementFingerprint(r)
		}
	}
	return out
}

// fingerprintConflicts lists the requirements whose live block no longer
// matches the fingerprint recorded at start.
func (p *Project) fingerprintConflicts(recorded map[string]string, c *Change) []string {
	if len(recorded) == 0 {
		return nil
	}
	live := p.baseFingerprints(c)
	var out []string
	for key, sha := range recorded {
		if now, ok := live[key]; ok && now != sha {
			out = append(out, "["+strings.Replace(key, "/", "] ", 1)+" changed in the spec since this change started")
		}
	}
	sort.Strings(out)
	return out
}
