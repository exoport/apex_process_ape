package apexv1

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// A crash mid-archive (H28). Archive's rollback is in-process: a SIGKILL
// (the kernel's OOM killer, say) between `git merge --squash` and the
// commit leaves a half squash staged on the base branch, the branch still
// present and HEAD on the base — and the next command only says
// tree.dirty. So archive writes a journal, _apex/state/archive-journal.json
// (ignored), before it catches the change up, updates it before it
// switches to the base branch, and removes it when it returns, whatever it
// returns. A journal that outlives its process is a crash: the next v1
// command that writes (start, verify, review, accept, block, archive,
// abandon, change/milestone/decision new, approve, gate, close, decision
// accept) rolls the checkout back to what the journal recorded — the base
// branch at its pre-archive sha (its squash commit undone, its half squash
// dropped, only the paths archive writes cleaned), change/<id> at its
// recorded tip, checked out — and fails with archive.interrupted (retry:
// archive again). `ape status` reports the journal, and changes nothing.

// CodeArchiveInterrupted: a crashed archive was rolled back.
const CodeArchiveInterrupted = "archive.interrupted"

// Archive journal steps.
const (
	// JournalRebase: on change/<id>, catching it up and re-verifying; the
	// base branch is untouched.
	JournalRebase = "rebase"
	// JournalSquash: switching to the base branch and committing the
	// squash there.
	JournalSquash = "squash"
)

// ArchiveJournal is an archive in flight.
type ArchiveJournal struct {
	ID string `json:"id"`
	// Base and BaseSHA: the base branch and its tip before the archive.
	Base    string `json:"base"`
	BaseSHA string `json:"base_sha"`
	// Branch and BranchTip: change/<id> and the tip to restore it to.
	Branch    string `json:"branch"`
	BranchTip string `json:"branch_tip"`
	Step      string `json:"step"`
	// Written are the project-relative paths the squash writes beside what
	// git merges (the archive folder, new specs): what a rollback cleans.
	Written []string `json:"written,omitempty"`
	PID     int      `json:"pid"`
	At      string   `json:"at"`
}

func (p *Project) journalPath() string { return filepath.Join(p.StateDir(), "archive-journal.json") }

func (p *Project) writeJournal(j *ArchiveJournal) error {
	j.PID, j.At = os.Getpid(), Stamp()
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	p.ensureState()
	return writeFileAtomic(p.journalPath(), string(data)+"\n")
}

func (p *Project) removeJournal() { _ = os.Remove(p.journalPath()) }

// LoadArchiveJournal reads the journal; nil when there is none (or it
// does not parse — a journal ape cannot read cannot be acted on).
func (p *Project) LoadArchiveJournal() *ArchiveJournal {
	data, err := os.ReadFile(p.journalPath())
	if err != nil {
		return nil
	}
	var j ArchiveJournal
	if json.Unmarshal(data, &j) != nil || j.ID == "" {
		return nil
	}
	return &j
}

// Running reports an archive whose process is still alive: not a crash.
func (j *ArchiveJournal) Running() bool {
	return j.PID > 0 && j.PID != os.Getpid() && processAlive(j.PID)
}

// recoverArchive rolls back an archive that crashed (see the comment
// above) and returns archive.interrupted saying what it did; nil when
// there is no journal, or its archive is still running.
func (p *Project) recoverArchive(ctx context.Context) error {
	j := p.LoadArchiveJournal()
	if j == nil || j.Running() {
		return nil
	}
	if !p.isWorkTree(ctx) {
		p.removeJournal()
		return nil
	}
	did := p.rollBack(ctx, j)
	p.removeJournal()
	return &FailError{Code: CodeArchiveInterrupted, Msg: "the archive of " + j.ID + " was interrupted (at its " + j.Step + " step) and is rolled back: " + strings.Join(did, "; ") +
		" — run `ape change archive " + j.ID + "` again, then this command"}
}

// rollBack restores what the journal recorded and says what it did.
func (p *Project) rollBack(ctx context.Context, j *ArchiveJournal) []string {
	var did []string
	gitDir, _ := git(ctx, p.Root, "rev-parse", "--absolute-git-dir")
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if dirExists(filepath.Join(gitDir, d)) {
			_, _ = git(ctx, p.Root, "rebase", "--abort")
			did = append(did, "aborted the rebase in progress")
			break
		}
	}
	_, _ = git(ctx, p.Root, "cherry-pick", "--quit")
	cur := CurrentBranch(ctx, p.Root)

	// The base branch: its squash commit undone, its half squash dropped.
	baseTip, _ := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+j.Base)
	switch {
	case baseTip == "" || j.BaseSHA == "" || baseTip == j.BaseSHA:
	case p.isSquashOf(ctx, baseTip, j):
		if cur == j.Base {
			_, _ = git(ctx, p.Root, "reset", "-q", "--hard", j.BaseSHA)
		} else {
			_, _ = git(ctx, p.Root, "update-ref", "refs/heads/"+j.Base, j.BaseSHA, baseTip)
		}
		did = append(did, "undid the squash commit "+short(baseTip)+" on "+j.Base+" (back at "+short(j.BaseSHA)+")")
	default:
		did = append(did, j.Base+" moved past "+short(j.BaseSHA)+" by other commits: left as it is")
	}
	if cur == j.Base {
		if dirty, _ := gitRaw(ctx, p.Root, "status", "--porcelain=v1", "--untracked-files=normal"); strings.TrimSpace(dirty) != "" {
			_, _ = git(ctx, p.Root, "reset", "-q", "--hard")
			p.cleanWritten(ctx, j.Written)
			did = append(did, "dropped the half-made squash on "+j.Base)
		}
	}

	// The change's branch: back at its recorded tip, checked out.
	tip, _ := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+j.Branch)
	switch {
	case tip == "":
		_, _ = git(ctx, p.Root, "branch", j.Branch, j.BranchTip)
		did = append(did, "restored "+j.Branch+" at "+short(j.BranchTip))
	case tip != j.BranchTip:
		if cur == j.Branch {
			_, _ = git(ctx, p.Root, "reset", "-q", "--hard", j.BranchTip)
		} else {
			_, _ = git(ctx, p.Root, "update-ref", "refs/heads/"+j.Branch, j.BranchTip, tip)
		}
		did = append(did, "reset "+j.Branch+" to "+short(j.BranchTip))
	}
	if j.Step == JournalSquash {
		_, _ = git(ctx, p.Root, "update-ref", "-d", archiveRef(j.ID))
	}
	if CurrentBranch(ctx, p.Root) != j.Branch {
		if _, err := git(ctx, p.Root, "switch", "-q", j.Branch); err != nil {
			did = append(did, "could not check out "+j.Branch+" ("+err.Error()+"): `git switch "+j.Branch+"`")
		} else {
			did = append(did, "checked out "+j.Branch)
		}
	}
	if len(did) == 0 {
		did = append(did, "nothing had changed")
	}
	return did
}

// isSquashOf reports whether sha is the archive's squash commit: one
// commit on the recorded base sha, trailer `Change: <id>`.
func (p *Project) isSquashOf(ctx context.Context, sha string, j *ArchiveJournal) bool {
	parent, err := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", sha+"^")
	if err != nil || parent != j.BaseSHA {
		return false
	}
	change, _ := git(ctx, p.Root, "log", "-1", "--format=%(trailers:key=Change,valueonly)", sha)
	return strings.TrimSpace(change) == j.ID
}

// cleanWritten removes the untracked files archive wrote — and only those,
// never a person's files elsewhere in the development folder — and the
// folders they leave empty.
func (p *Project) cleanWritten(ctx context.Context, written []string) {
	if len(written) == 0 {
		return
	}
	_, _ = git(ctx, p.Root, append([]string{"clean", "-fdq", "--"}, written...)...)
	for _, w := range written {
		// A new capability's folder, left empty; never the archive folder.
		if strings.HasSuffix(w, "/spec.md") {
			_ = os.Remove(filepath.Dir(filepath.Join(p.Root, filepath.FromSlash(w))))
		}
	}
}
