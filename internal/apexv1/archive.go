package apexv1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aymanbagabas/go-udiff"
)

// SpecUpdate is one capability's spec after a delta merge.
type SpecUpdate struct {
	Capability string `json:"capability"`
	Path       string `json:"path"`
	Created    bool   `json:"created"`
	// Retired: the change removed the capability's last requirement and
	// lists it under retire:; archive deletes the spec.
	Retired  bool `json:"retired,omitempty"`
	Added    int  `json:"added"`
	Modified int  `json:"modified"`
	Removed  int  `json:"removed"`
	content  string
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
	retire := map[string]bool{}
	for _, r := range c.Meta.Retire {
		retire[r] = true
	}
	for _, capName := range order {
		spec, _, exists := p.LoadSpec(capName)
		u := SpecUpdate{Capability: capName, Path: p.Rel(p.SpecFile(capName)), Created: !exists}
		if !exists {
			spec = Spec{Preamble: "# " + titleFromSlug(capName) + "\nPurpose: " + capabilityPurpose(c, capName)}
		}
		merged, cs := applyDeltas(&spec, capName, byCap[capName], &u)
		if len(cs) == 0 && exists && len(spec.Requirements) == 0 {
			if retire[capName] {
				u.Retired = true
			} else {
				cs = append(cs, fmt.Sprintf("the delta leaves [%s] with no requirement — add `retire: [%s]` to delete the capability", capName, capName))
			}
		}
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
				conflicts = append(conflicts, fmt.Sprintf("MODIFIED [%s] %q is not in the spec (%s)", capName, d.Name, nearMissFix(*spec, d.Name)))
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
				conflicts = append(conflicts, fmt.Sprintf("REMOVED [%s] %q is not in the spec (%s)", capName, d.Name, nearMissFix(*spec, d.Name)))
				continue
			}
			spec.Requirements = append(spec.Requirements[:idx], spec.Requirements[idx+1:]...)
			u.Removed++
		}
	}
	return spec.Render(), conflicts
}

// capabilityPurpose is a new capability's Purpose: the `Purpose:` line of
// one of the change's ADDED blocks for it (item 11), else purposeFrom.
func capabilityPurpose(c *Change, capName string) string {
	for i := range c.Deltas {
		if d := &c.Deltas[i]; d.Op == OpAdded && d.Capability == capName && d.Purpose != "" {
			return d.Purpose
		}
	}
	return purposeFrom(c)
}

// purposeFrom is a new capability's Purpose line when the change gives
// none: the first paragraph of the introducing change's `## Why`,
// flattened to one line.
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
	ID         string       `json:"id"`
	ArchivedTo string       `json:"archived_to"`
	Specs      []SpecUpdate `json:"specs"`
	// Commit is the one commit on the base branch: the squashed change,
	// the delta merged into specs/ and the archive move (D38).
	Commit  string `json:"commit"`
	Subject string `json:"subject,omitempty"`
	// Hooks: the project's git hooks ran on the squash (commit.hooks).
	Hooks      bool   `json:"hooks"`
	BaseBranch string `json:"base_branch,omitempty"`
	// BranchTip is change/<id>'s last commit, squashed into Commit, and
	// kept reachable as TipRef, refs/apex/archive/<id> (H16).
	BranchTip string `json:"branch_tip,omitempty"`
	TipRef    string `json:"tip_ref,omitempty"`
	// Rebased: the base branch had moved, and archive rebased the change
	// branch onto it.
	Rebased bool `json:"rebased"`
	// CatchUp is the catch-up with the moved base (D40), when there was one.
	CatchUp           *CatchUp      `json:"catch_up,omitempty"`
	Reverified        bool          `json:"reverified"`
	Verify            *VerifyResult `json:"verify,omitempty"`
	AllowPendingHuman bool          `json:"allow_pending_human,omitempty"`
	PendingHuman      []string      `json:"pending_human,omitempty"`
}

// Archive lands a reviewed change on its base branch (D38): it catches
// change/<id> up with the base branch when that moved (catchUp, D40: a
// rebase conflict is left on the branch, exit 3 — H8), re-verifies when that moved
// the code, then squash-merges the branch into
// the base branch as ONE commit — subject the change's title, trailer
// `Change: <id>` — holding the change's code, its delta merged into
// specs/, and its folder moved to archive/<date>-<id>/; then deletes the
// branch. Run from the change's branch or from any other non-change
// branch (it switches); the tree must be clean.
func (p *Project) Archive(ctx context.Context, id string, allowPendingHuman bool, opt RunOptions) (*ArchiveResult, error) {
	// Archiving past a person's review is a person's call (D45).
	if allowPendingHuman {
		if err := requirePerson("--allow-pending-human"); err != nil {
			return nil, err
		}
	}
	if err := p.recoverArchive(ctx); err != nil {
		return nil, err
	}
	ci, err := p.requireChange(ctx, id)
	if err != nil {
		return nil, err
	}
	pending, err := p.archivable(ctx, ci, allowPendingHuman)
	if err != nil {
		return nil, err
	}
	// Archive ends on the base branch: refused up front, before any
	// rebase or re-verify, when another worktree holds it (D45).
	if err := p.requireBranchFree(ctx, ci.State.BaseBranch, "archive "+id); err != nil {
		return nil, err
	}
	if err := p.refuseBehindUpstream(ctx, ci.State.BaseBranch, "archive "+id); err != nil {
		return nil, err
	}
	if ci, err = p.enterChangeBranch(ctx, ci); err != nil {
		return nil, err
	}
	base := ci.State.BaseBranch
	res := &ArchiveResult{ID: id, BaseBranch: base}
	// The journal (H28): what a crash from here on is rolled back to. Any
	// return — success, a refusal, a conflict laid out — removes it.
	baseSHA, _ := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+base)
	j := &ArchiveJournal{ID: id, Base: base, BaseSHA: baseSHA, Branch: ChangeBranch(id), BranchTip: HeadSHA(ctx, p.Root), Step: JournalRebase}
	if err := p.writeJournal(j); err != nil {
		return res, err
	}
	defer p.removeJournal()

	// The review judged the change's own code: refused when that moved
	// since, judged before the rebase brings the base's code in.
	if stale, files := p.reviewStale(ctx, ci.State); stale {
		return res, &FailError{Code: "review.stale", Msg: "code changed since the review (" + strings.Join(limit(files, 5), ", ") + ") — review again (apex-review; `ape change review " + id + " --run` re-verifies it first), then archive"}
	}
	// The one catch-up path (D40): rebased onto the base when it moved,
	// re-baselined at its tip, the verify marked stale; reviewed stays
	// reviewed here, and archiveFresh re-verifies.
	ci, cu, err := p.catchUp(ctx, ci, "archive", opt, true)
	res.CatchUp = cu
	if err != nil {
		return res, err
	}
	res.Rebased = cu != nil && cu.Rebased
	if err := p.archiveFresh(ctx, ci, res, opt); err != nil {
		return res, err
	}
	updates, dest, err := p.planArchive(ci)
	if err != nil {
		return res, err
	}
	if len(pending) > 0 {
		res.AllowPendingHuman, res.PendingHuman = true, pending
	}
	res.ArchivedTo, res.Specs = p.Rel(dest), updates
	cc := p.commitConfig()
	res.Subject = archiveSubject(cc.SubjectTemplate(), ci.Doc, id)
	res.Hooks = cc.RunHooks()
	return res, p.squashOntoBase(ctx, ci, updates, dest, pending, res, j)
}

// archivable checks what archive needs before it touches git: a review
// pass, a frozen plan, accepted human-review tasks (or allowPendingHuman),
// a git repository and the change's branch with its base. It returns the
// human-review tasks archived pending.
func (p *Project) archivable(ctx context.Context, ci ChangeInfo, allowPendingHuman bool) ([]string, error) {
	id, st := ci.ID, ci.State
	if st.Status != StatusReviewed || st.Review == nil || st.Review.Verdict != "pass" {
		return nil, &FailError{Code: CodeWrongStatus, Msg: "change " + id + " is " + st.Status + " — archive needs a green verify and a review pass (`ape change review " + id + " --verdict pass`)"}
	}
	if !p.isWorkTree(ctx) {
		return nil, &FailError{Code: "git.not-a-repo", Msg: "not a git repository — archive commits its result"}
	}
	if err := checkPlanFrozen(ci); err != nil {
		return nil, err
	}
	pending := st.PendingHuman(ci.Doc.HumanTasks())
	if len(pending) > 0 && !allowPendingHuman {
		return nil, &FailError{Code: "archive.pending-human", Msg: "human-review tasks not accepted: " + strings.Join(pending, ", ") +
			" — a person runs `ape change accept " + id + " --task <T> --by <who>` (on " + ChangeBranch(id) + "), or archives it with --allow-pending-human (recorded; a person only, from a terminal)"}
	}
	if !p.branchExists(ctx, ChangeBranch(id)) || st.BaseBranch == "" {
		return nil, &FailError{Code: "change.no-branch", Msg: "change " + id + " has no branch " + ChangeBranch(id) + " with a recorded base_branch — it was started before changes got their own branch (D38); " +
			"create it from where its work is and record base_branch, or abandon it"}
	}
	if !p.branchExists(ctx, st.BaseBranch) {
		return nil, &FailError{Code: CodeBranchFailed, Msg: "the base branch " + st.BaseBranch + " of " + id + " does not exist — recreate it, or abandon the change"}
	}
	return pending, nil
}

// enterChangeBranch makes the tree clean (committing what the change's own
// folder holds, on its branch) and checks out the change's branch, from
// any branch but another change's. It returns the change as read there.
func (p *Project) enterChangeBranch(ctx context.Context, ci ChangeInfo) (ChangeInfo, error) {
	id := ci.ID
	if err := p.commitChangeFolder(ctx, id, "archive"); err != nil {
		return ci, err
	}
	cur := CurrentBranch(ctx, p.Root)
	if cur == ChangeBranch(id) {
		return ci, nil
	}
	if ChangeOfBranch(cur) != "" {
		return ci, p.otherChangeBranch(cur, "archive "+id)
	}
	if err := p.switchBranch(ctx, ChangeBranch(id)); err != nil {
		return ci, err
	}
	return p.requireChange(ctx, id)
}

// planArchive decides everything archive writes, writing nothing: the
// merged specs and the archive folder.
func (p *Project) planArchive(ci ChangeInfo) (updates []SpecUpdate, dest string, err error) {
	id, st := ci.ID, ci.State
	if fc := p.fingerprintConflicts(st.BaseFingerprints, ci.Doc); len(fc) > 0 {
		return nil, "", &ConflictError{Code: "archive.base-changed", Msg: "requirements this change modifies or removes were changed by another change since it started — nothing was written; " +
			"re-plan against the current spec and re-start (`ape change start " + id + "`)", Conflicts: fc}
	}
	// The subject template may have come to use {type} since start.
	if fs := typeFindings(p.Rel(p.ChangeMD(id)), ci.Doc, p.commitConfig().SubjectTemplate()); len(fs) > 0 {
		return nil, "", &FailError{Code: CodeCheckFailed, Msg: "change " + id + " cannot be archived with the configured commit subject — fix change.md's type: (a plan change: re-start), then archive", Findings: fs}
	}
	updates, conflicts := p.MergeDelta(ci.Doc)
	if len(conflicts) > 0 {
		return nil, "", &ConflictError{Code: CodeDeltaConflict, Msg: "the delta no longer applies to specs/ — nothing was written; re-plan the change against the current specs", Conflicts: conflicts}
	}
	dest = filepath.Join(p.ArchiveDir(), Today()+"-"+id)
	if fileOrDirExists(dest) {
		return nil, "", &ConflictError{Code: CodeFolderExists, Msg: "archive folder " + p.Rel(dest) + " already exists — nothing was written"}
	}
	for _, u := range updates {
		if u.Retired {
			continue
		}
		if fs := ValidateSpec(u.Path, u.content); len(fs) > 0 {
			return nil, "", &FailError{Code: "archive.spec-invalid", Msg: "merged spec " + u.Path + " would not pass `ape project check` — nothing was written", Findings: fs}
		}
	}
	return updates, dest, nil
}

// squashOntoBase switches to the base branch, squash-merges the change's
// branch, writes the specs and the archive move, commits it all as ONE
// commit and deletes the branch. Anything failing before the commit
// leaves the base branch where it was and the change's branch checked out.
func (p *Project) squashOntoBase(ctx context.Context, ci ChangeInfo, updates []SpecUpdate, dest string, pending []string, res *ArchiveResult, j *ArchiveJournal) error {
	id, base, branch := ci.ID, ci.State.BaseBranch, ChangeBranch(ci.ID)
	tip := HeadSHA(ctx, p.Root)
	// What the squash writes beside what git merges: all a rollback may
	// clean (H28) — never the whole development folder, where a person's
	// untracked files may be.
	written := []string{p.Rel(dest)}
	for _, u := range updates {
		if u.Created {
			written = append(written, u.Path)
		}
	}
	j.Step, j.BranchTip, j.Written = JournalSquash, tip, written
	if err := p.writeJournal(j); err != nil {
		return err
	}
	if err := p.switchBranch(ctx, base); err != nil {
		return err
	}
	baseSHA := HeadSHA(ctx, p.Root)
	undo := func(cause error) error {
		_, _ = git(ctx, p.Root, "reset", "-q", "--hard", baseSHA)
		p.cleanWritten(ctx, written)
		_, _ = git(ctx, p.Root, "switch", "-q", branch)
		if fe, ok := errors.AsType[*FailError](cause); ok {
			fe.Msg += " (nothing was committed: " + base + " is unchanged and " + branch + " is checked out again)"
			return fe
		}
		return &FailError{Code: CodeCommitFailed, Msg: "archive failed on " + base + ": " + cause.Error() + " — nothing was committed: " + base + " is unchanged and " + branch + " is checked out again; fix git and archive again"}
	}
	if _, err := git(ctx, p.Root, "merge", "--squash", "-q", branch); err != nil {
		return undo(err)
	}
	if err := p.writeArchive(ctx, id, ci.State, updates, dest, tip, pending); err != nil {
		return undo(err)
	}
	// An archived change ends its milestone's run of blocks (H19).
	if err := p.resetBlocks(ctx, ci.Doc); err != nil {
		return undo(err)
	}
	// What the review noticed outside the change goes to Next (H26).
	if err := p.appendNoticed(ctx, id, dest); err != nil {
		return undo(err)
	}
	res.BranchTip = tip
	msg := p.squashMessage(ctx, res.Subject, base, branch, id)
	if out, err := p.commitSquash(ctx, msg, res.Hooks); err != nil {
		if res.Hooks && p.hasCommitHooks(ctx) {
			return undo(&FailError{Code: CodeHookFailed, Msg: "a git hook refused the squash commit on " + base + ": " + strings.Join(lastLines(out, 15), " | ") +
				" — fix what it reports on " + branch + " (commit there, verify), then archive again; or set commit.hooks: skip in _apex/config.yaml"})
		}
		return undo(fmt.Errorf("%w: %s", err, strings.TrimSpace(out)))
	}
	res.Commit = HeadSHA(ctx, p.Root)
	if err := p.keepTip(ctx, archiveRef(id), tip, "archived as "+short(res.Commit)+" on "+base, branch); err != nil {
		return err
	}
	res.TipRef = archiveRef(id)
	if _, err := git(ctx, p.Root, "branch", "-q", "-D", branch); err != nil {
		return &FailError{Code: CodeBranchFailed, Msg: "archived as " + short(res.Commit) + " on " + base + ", but deleting " + branch + " failed: " + err.Error() + " — `git branch -D " + branch + "`"}
	}
	return nil
}

// writeArchive applies the delta to specs/, moves the change folder to
// dest with its state archived, and stages both.
func (p *Project) writeArchive(ctx context.Context, id string, st *State, updates []SpecUpdate, dest, tip string, pending []string) error {
	for _, u := range updates {
		path := filepath.Join(p.Root, filepath.FromSlash(u.Path))
		if u.Retired {
			if err := os.RemoveAll(filepath.Dir(path)); err != nil {
				return err
			}
			continue
		}
		if err := writeFileAtomic(path, u.content); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(p.ArchiveDir(), 0o755); err != nil {
		return err
	}
	if err := os.Rename(p.ChangeDir(id), dest); err != nil {
		return err
	}
	st.Status = StatusArchived
	st.ArchivedAt = Stamp()
	st.ArchivedTo = p.Rel(dest)
	st.BranchTip = tip
	st.TipRef = archiveRef(id)
	if len(pending) > 0 {
		st.AllowPendingHuman = true
	}
	if err := SaveState(filepath.Join(dest, "state.json"), st); err != nil {
		return err
	}
	if p.evidenceOn() && st.Verify != nil {
		if err := p.writeEvidence(filepath.Join(dest, "evidence"), st.Verify.Results, nil); err != nil {
			return err
		}
	}
	_, err := git(ctx, p.Root, "add", "-A", "--", p.DevRel)
	return err
}

// AbandonResult is what `ape change abandon` reports.
type AbandonResult struct {
	ID         string `json:"id"`
	ArchivedTo string `json:"archived_to"`
	Reason     string `json:"reason"`
	Commit     string `json:"commit"`
	// BranchTip is the deleted change/<id>'s last commit (D38), kept
	// reachable as TipRef (H16).
	BranchTip string `json:"branch_tip,omitempty"`
	TipRef    string `json:"tip_ref,omitempty"`
}

// Abandon moves a change to the archive as abandoned, merging nothing,
// and commits the move as `apex: <id> abandoned` (trial decision D6) on
// the base branch; a started change's branch is deleted, its tip recorded
// as branch_tip (D38). The archived folder holds the change's files as
// its branch last had them.
func (p *Project) Abandon(ctx context.Context, id, reason string) (*AbandonResult, error) {
	if err := p.recoverArchive(ctx); err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, &UsageError{Code: "usage.reason", Msg: "--reason is required: why the change is dropped, and what (if anything) replaces it"}
	}
	ci, err := p.requireChange(ctx, id)
	if err != nil {
		return nil, err
	}
	if cur := p.HeadChange(ctx); cur != "" && cur != id && ci.Branch != "" {
		return p.abandonElsewhere(ctx, ci, reason)
	}
	folder := Today() + "-" + id
	dest := filepath.Join(p.ArchiveDir(), folder)
	if fileOrDirExists(dest) {
		return nil, &ConflictError{Code: CodeFolderExists, Msg: "archive folder " + p.Rel(dest) + " already exists — nothing was written"}
	}
	inGit := p.isWorkTree(ctx)
	branch, tip := ChangeBranch(id), ""
	if inGit {
		if ci, tip, err = p.leaveChangeBranch(ctx, ci); err != nil {
			return nil, err
		}
	}
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
	st.BranchTip = tip
	if tip != "" {
		st.TipRef = abandonedRef(id)
	}
	if err := SaveState(filepath.Join(dest, "state.json"), st); err != nil {
		return nil, err
	}
	res := &AbandonResult{ID: id, ArchivedTo: p.Rel(dest), Reason: reason, BranchTip: tip, TipRef: st.TipRef}
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
	if tip != "" {
		if err := p.keepTip(ctx, abandonedRef(id), tip, "abandoned", branch); err != nil {
			return res, err
		}
		if _, err := git(ctx, p.Root, "branch", "-q", "-D", branch); err != nil {
			return res, &FailError{Code: CodeBranchFailed, Msg: "abandoned, but deleting " + branch + " failed: " + err.Error() + " — `git branch -D " + branch + "`"}
		}
	}
	return res, nil
}

// abandonElsewhere is Abandon with HEAD on another change's branch (D51
// Q17): the change's folder, as the branch holding its records has it (its
// own branch, or the base for a change with none), is moved into the
// archive on the base branch in one commit there, without a checkout —
// the base's copy of the folder deleted, the archived state.json
// abandoned — and a started change's branch is deleted, its tip kept as
// refs/apex/abandoned/<id>. The working tree, its index and HEAD are not
// touched.
func (p *Project) abandonElsewhere(ctx context.Context, ci ChangeInfo, reason string) (*AbandonResult, error) {
	id, src := ci.ID, ci.Branch
	base := ci.State.BaseBranch
	if base == "" || !p.branchExists(ctx, base) {
		b, err := p.baseBranch(ctx)
		if err != nil {
			return nil, err
		}
		base = b
	}
	branch, tip := ChangeBranch(id), ""
	if src == branch {
		if wt := p.worktreeOf(ctx, branch); wt != "" {
			return nil, &FailError{Code: CodeBaseCheckedOut, Msg: branch + " is checked out in another worktree, " + wt + ", and abandon deletes it — nothing was written; switch that worktree off it and run this again"}
		}
		tip, _ = git(ctx, p.Root, "rev-parse", "--verify", "refs/heads/"+branch)
	}
	folder := Today() + "-" + id
	destRel := p.Rel(filepath.Join(p.ArchiveDir(), folder))
	if _, err := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+base+":./"+destRel); err == nil {
		return nil, &ConflictError{Code: CodeFolderExists, Msg: "archive folder " + destRel + " already exists on " + base + " — nothing was written"}
	}
	dirRel := p.Rel(p.ChangeDir(id))
	files := map[string][]byte{}
	for _, name := range p.filesAt(ctx, base, dirRel) {
		files[dirRel+"/"+name] = nil
	}
	for _, name := range p.filesAt(ctx, src, dirRel) {
		data, err := p.showAt(ctx, "refs/heads/"+src, dirRel+"/"+name)
		if err != nil {
			return nil, &FailError{Code: CodeBranchFailed, Msg: "cannot read " + id + "'s folder from " + src + ": " + err.Error()}
		}
		files[destRel+"/"+name] = []byte(data)
	}
	st := *ci.State
	st.Status, st.AbandonedReason, st.ArchivedAt, st.ArchivedTo, st.BranchTip = StatusAbandoned, reason, Stamp(), destRel, tip
	if tip != "" {
		st.TipRef = abandonedRef(id)
	}
	data, err := stateBytes(&st)
	if err != nil {
		return nil, err
	}
	files[destRel+"/state.json"] = data
	res := &AbandonResult{ID: id, ArchivedTo: destRel, Reason: reason, BranchTip: tip, TipRef: st.TipRef}
	if res.Commit, err = p.commitOnBranch(ctx, base, files, "apex: "+id+" "+StatusAbandoned+"\n\nChange: "+id); err != nil {
		return nil, err
	}
	if tip != "" {
		if err := p.keepTip(ctx, abandonedRef(id), tip, "abandoned", branch); err != nil {
			return res, err
		}
		if _, err := git(ctx, p.Root, "branch", "-q", "-D", branch); err != nil {
			return res, &FailError{Code: CodeBranchFailed, Msg: "abandoned, but deleting " + branch + " failed: " + err.Error() + " — `git branch -D " + branch + "`"}
		}
	}
	return res, nil
}

// filesAt lists the files under dir (project-relative) on branch, relative
// to dir; none when branch does not hold dir.
func (p *Project) filesAt(ctx context.Context, branch, dir string) []string {
	out, err := git(ctx, p.Root, "ls-tree", "-r", "--name-only", "refs/heads/"+branch+":./"+dir)
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

// leaveChangeBranch prepares abandoning a change (D38): refused on another
// change's branch; a started change's branch is left for its base branch
// (from the change's branch) and its folder taken as the branch last had
// it. It returns the change as read then, and the branch's tip ("" when
// the change has no branch).
func (p *Project) leaveChangeBranch(ctx context.Context, ci ChangeInfo) (ChangeInfo, string, error) {
	id, branch := ci.ID, ChangeBranch(ci.ID)
	cur := CurrentBranch(ctx, p.Root)
	if ChangeOfBranch(cur) != "" && cur != branch {
		return ci, "", p.otherChangeBranch(cur, "abandon "+id)
	}
	if !p.branchExists(ctx, branch) {
		return ci, "", nil
	}
	if base := ci.State.BaseBranch; cur == branch && base != "" {
		if err := p.requireBranchFree(ctx, base, "abandon "+id); err != nil {
			return ci, "", err
		}
	}
	if err := p.commitChangeFolder(ctx, id, "abandon"); err != nil {
		return ci, "", err
	}
	if cur == branch {
		base := ci.State.BaseBranch
		if base == "" || !p.branchExists(ctx, base) {
			return ci, "", &FailError{Code: CodeBranchFailed, Msg: "the base branch of " + id + " (" + base + ") is not recorded or does not exist — `git switch <base>` and abandon from there"}
		}
		if err := p.switchBranch(ctx, base); err != nil {
			return ci, "", err
		}
	}
	tip, _ := git(ctx, p.Root, "rev-parse", "--verify", "refs/heads/"+branch)
	// The archive keeps the change as its branch last had it.
	if _, err := git(ctx, p.Root, "checkout", branch, "--", p.Rel(p.ChangeDir(id))); err != nil {
		return ci, "", &FailError{Code: CodeBranchFailed, Msg: "cannot read " + id + "'s folder from " + branch + ": " + err.Error()}
	}
	ci, err := checkLoaded(p.loadChange(id))
	return ci, tip, err
}

// archiveRef and abandonedRef keep an archived or abandoned change's
// branch tip reachable after its branch is deleted (H16): the archived
// state.json's branch_tip, verify and review shas would otherwise be
// pruned by git gc. Local refs, outside refs/heads and refs/tags.
func archiveRef(id string) string   { return "refs/apex/archive/" + id }
func abandonedRef(id string) string { return "refs/apex/abandoned/" + id }

// keepTip points ref at tip before branch is deleted; done says what was
// already done, for the message when it fails (the branch is then kept).
func (p *Project) keepTip(ctx context.Context, ref, tip, done, branch string) error {
	if _, err := git(ctx, p.Root, "update-ref", ref, tip); err != nil {
		return &FailError{Code: CodeBranchFailed, Msg: done + ", but keeping " + branch + "'s tip as " + ref + " failed: " + err.Error() +
			" — `git update-ref " + ref + " " + tip + "`, then `git branch -D " + branch + "`"}
	}
	return nil
}

func failingNames(v *VerifyResult) string {
	failing := v.Failing()
	names := make([]string, 0, len(failing))
	for i := range failing {
		names = append(names, failing[i].Task+" "+failing[i].Outcome())
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

// archiveFresh re-runs the verify when the code moved since it — after
// the rebase onto a moved base branch, or a commit since the verify that
// touched no code the review judged. ape's own state commits (D5) move
// HEAD on every status change, so "HEAD moved" means "something outside
// the development folder changed". A green re-verify keeps the change
// reviewed: the review was of the change's own code, judged before the
// rebase.
func (p *Project) archiveFresh(ctx context.Context, ci ChangeInfo, res *ArchiveResult, opt RunOptions) error {
	st := ci.State
	codeMoved := st.Verify == nil || st.Verify.Stale
	if !codeMoved {
		codeMoved, _, _ = p.codeMovedSince(ctx, st.Verify.SHA)
	}
	if !codeMoved {
		return nil
	}
	v, err := p.verify(ctx, ci, opt, true)
	res.Reverified, res.Verify = true, v
	if err != nil {
		return err
	}
	if !v.Green {
		return &FailError{Code: "verify.red", Msg: "re-verify at HEAD is red (" + failingNames(v) + ") — the change is back in-progress on " + ChangeBranch(ci.ID) + "; fix it there"}
	}
	return nil
}

// SpecDiff is one capability's spec before and after a change's delta.
type SpecDiff struct {
	SpecUpdate
	Diff string `json:"diff"`
}

// SpecPreview is `ape change diff`: the specs as archive would leave them
// (backlog item 10) — for review and for handoff.
type SpecPreview struct {
	ID        string     `json:"id"`
	Specs     []SpecDiff `json:"specs"`
	DeltaNone string     `json:"delta_none,omitempty"`
}

// PreviewDeltaAnywhere is PreviewDelta of the change as the branch
// holding its records has it, against that branch's specs (D51 Q17).
func (p *Project) PreviewDeltaAnywhere(ctx context.Context, id string) (*SpecPreview, error) {
	if _, err := p.requireChange(ctx, id); err != nil {
		return nil, err
	}
	view, done, err := p.viewAt(ctx, p.changeBranchOf(ctx, id))
	if err != nil {
		return nil, err
	}
	defer done()
	return view.previewDelta(ctx, id)
}

// PreviewDelta merges the change's delta in memory and diffs each touched
// spec. A delta that no longer applies is a ConflictError, as at archive.
func (p *Project) PreviewDelta(id string) (*SpecPreview, error) {
	return p.previewDelta(context.Background(), id)
}

func (p *Project) previewDelta(ctx context.Context, id string) (*SpecPreview, error) {
	ci, err := p.requireChange(ctx, id)
	if err != nil {
		return nil, err
	}
	out := &SpecPreview{ID: id, Specs: []SpecDiff{}, DeltaNone: ci.Doc.DeltaNone}
	updates, conflicts := p.MergeDelta(ci.Doc)
	if len(conflicts) > 0 {
		return out, &ConflictError{Code: CodeDeltaConflict, Msg: "the delta does not apply to the current specs — re-plan the change against them", Conflicts: conflicts}
	}
	for _, u := range updates {
		before := ""
		if !u.Created {
			before, _ = readFileString(filepath.Join(p.Root, filepath.FromSlash(u.Path)))
		}
		from, to, after := "a/"+u.Path, "b/"+u.Path, u.content
		if u.Created {
			from = "/dev/null"
		}
		if u.Retired {
			to, after = "/dev/null", ""
		}
		out.Specs = append(out.Specs, SpecDiff{SpecUpdate: u, Diff: udiff.Unified(from, to, before, after)})
	}
	return out, nil
}
