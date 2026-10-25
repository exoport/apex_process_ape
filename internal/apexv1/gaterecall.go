package apexv1

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// VerbMilestoneGate is the long verb whose finished run records the tree
// it left (F1, D432/D434, narrowed by D455): a gate run takes up to hours,
// and the call that saw only exit 75 must get its result rather than run
// it again — but only once; a later call runs the gate again.
const VerbMilestoneGate = "milestone gate"

// WorkTreeDigest is the git tree id of the project's working tree as it
// is now: tracked files as they are on disk and untracked files git does
// not ignore, ape's own _apex/state/ left out. It is written through a
// copy of the index (its stat cache keeps unchanged files from being
// hashed again), so the real index and HEAD are untouched; the objects it
// writes are unreferenced, for git gc. Equal digests mean the same content
// whatever was committed in between: a commit of the same files keeps it,
// an untracked file, an edit or a deletion changes it.
func (p *Project) WorkTreeDigest(ctx context.Context) (string, error) {
	return p.workTreeDigest(ctx, false)
}

// ContentDigest is WorkTreeDigest with ape's state left out altogether,
// its tracked records too (milestones.json, decisions.json): what a full
// run is current against (v1.4), so the gate's own record commit, which
// writes only milestones.json, does not change it.
func (p *Project) ContentDigest(ctx context.Context) (string, error) {
	return p.workTreeDigest(ctx, true)
}

func (p *Project) workTreeDigest(ctx context.Context, dropState bool) (string, error) {
	idx, err := git(ctx, p.Root, "rev-parse", "--git-path", "index")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(idx) {
		idx = filepath.Join(p.Root, idx)
	}
	dir, err := os.MkdirTemp("", "ape-tree-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	tmp := filepath.Join(dir, "index")
	switch data, err := os.ReadFile(idx); {
	case err == nil:
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return "", err
		}
	case !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	env := []string{"GIT_INDEX_FILE=" + tmp}
	if _, err := gitWithEnv(ctx, p.Root, env, "add", "-A", "--", ".", ":(exclude)"+p.StateRel()); err != nil {
		return "", err
	}
	if dropState {
		if _, err := gitWithEnv(ctx, p.Root, env, "rm", "-r", "-q", "--cached", "--ignore-unmatch", "--", p.StateRel()); err != nil {
			return "", err
		}
	}
	return gitWithEnv(ctx, p.Root, env, "write-tree")
}

// GatePreflight is what a gate run refuses before it runs anything: HEAD
// on a change branch, a done milestone, an approved one ape holds no
// approval record of or whose gate changed since (milestone.gate-changed).
func (p *Project) GatePreflight(ctx context.Context, id string) error {
	if err := p.refuseOnChangeBranch(ctx, "a milestone gate run"); err != nil {
		return err
	}
	if _, rm, err := p.loadRoadmapDoc(); err == nil {
		if m := rm.Find(id); m != nil {
			switch m.Status {
			case MilestoneDone:
				// A done milestone's gate was recorded at its close, and its
				// gate folder archived (D155): run now, the Gate: line names
				// a script that is gone, and a red would be recorded and
				// committed against a closed milestone (Q162, D350).
				// Refused before anything runs or is written.
				return doneMilestoneGate(m)
			case MilestoneApproved:
				// An approved milestone's gate is the one a person approved (H2).
				if err := p.requireApprovalRecord(m, true); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// GateRunCurrent is the scope's last long run when it was a finished
// `ape milestone gate <M>` that still describes the project (F1): it
// recorded the tree it left, HEAD is still where it ended, the working
// tree (ape's state aside) is the same, and the gate would not be refused
// now — so a gate whose files changed since its approval is refused,
// never answered from the record. Nil when any of that fails. A result
// without a tree (written by ape v1.1) never matches. Being current is
// not enough for a call to return it: only a result no call printed yet
// is returned, once (D455); ape status reports it as gate_last.current.
func (p *Project) GateRunCurrent(ctx context.Context, id string) *RunResult {
	r := p.LoadRunResult(id)
	if r == nil || r.Verb != VerbMilestoneGate || r.Tree == "" || r.HeadAfter == "" || r.HeadAfter != HeadSHA(ctx, p.Root) {
		return nil
	}
	if p.GatePreflight(ctx, id) != nil {
		return nil
	}
	if tree, err := p.WorkTreeDigest(ctx); err != nil || tree != r.Tree {
		return nil
	}
	return r
}
