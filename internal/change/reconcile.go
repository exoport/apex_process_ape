package change

import (
	"fmt"
	"sort"
	"strings"
)

// Reconciliation is ape's account of the working tree: which goal each
// changed path belongs to, and what does not fit.
//
// This is the step that makes the contract safe to act on. Everything
// before it is the skill's own report; this compares that report against
// what git says actually changed. A path no goal claims is the failure
// it exists to catch — an edit nobody declared, riding into a commit
// under a subject that does not describe it.
type Reconciliation struct {
	// ClaimedBy maps a changed path to the 1-based goal claiming it.
	ClaimedBy map[string]int
	// EvidenceFiles holds, per goal, the changed files under its
	// evidence path. This is what the evidence commit carries, and it
	// being empty is why a goal can have an evidence path and no
	// evidence commit.
	EvidenceFiles map[int][]string
	// Unclaimed is every changed path no goal accounts for. Non-empty
	// means the run is refused.
	Unclaimed []string
	// Missing is every path a goal claims that git does not report as
	// changed. Recorded, not refused — see Reconcile.
	Missing []string
}

// Reconcile matches the changed set against the goals' claims.
//
// A claim covers the path itself and anything beneath it, because that
// is what the pathspec ape commits with does. Matching more narrowly
// here would refuse runs whose commits would have been exactly right.
//
// The two directions are NOT symmetrical, and deliberately so:
//
//   - a changed path no goal claims is a refusal. It is the one that
//     lets unreviewed work ride along inside someone else's commit;
//   - a claimed path that did not change is recorded and reported, not
//     refused. It means the skill over-declared — it listed a file it
//     then reverted, or named one it never reached — and the commit ape
//     would make is still exactly the set of real changes. Refusing
//     there would throw away an hour of finished work over a list that
//     was too long.
func (l Layout) Reconcile(changed []string, goals []Goal) *Reconciliation {
	r := &Reconciliation{
		ClaimedBy:     map[string]int{},
		EvidenceFiles: map[int][]string{},
	}
	type claim struct {
		path string
		goal int
	}
	var claims []claim
	for i := range goals {
		g := &goals[i]
		n := i + 1
		if g.Status == GoalNotStarted {
			continue
		}
		for _, p := range g.Paths {
			claims = append(claims, claim{path: p, goal: n})
		}
		if !None(g.Evidence) {
			claims = append(claims, claim{path: g.Evidence, goal: n})
		}
	}

	for _, p := range changed {
		matched := false
		for _, c := range claims {
			if !within(c.path, p) {
				continue
			}
			matched = true
			if _, already := r.ClaimedBy[p]; !already {
				r.ClaimedBy[p] = c.goal
			}
			if isEvidence(goals, c.goal, p) {
				r.EvidenceFiles[c.goal] = append(r.EvidenceFiles[c.goal], p)
			}
		}
		if !matched {
			r.Unclaimed = append(r.Unclaimed, p)
		}
	}

	for _, c := range claims {
		found := false
		for _, p := range changed {
			if within(c.path, p) {
				found = true
				break
			}
		}
		if !found {
			r.Missing = append(r.Missing, c.path)
		}
	}
	sort.Strings(r.Unclaimed)
	sort.Strings(r.Missing)
	for g := range r.EvidenceFiles {
		sort.Strings(r.EvidenceFiles[g])
	}
	return r
}

// isEvidence reports whether p lies under goal n's evidence path.
func isEvidence(goals []Goal, n int, p string) bool {
	if n < 1 || n > len(goals) {
		return false
	}
	g := &goals[n-1]
	return !None(g.Evidence) && within(g.Evidence, p)
}

// RefusalError renders the unclaimed set as the refusal it is, with both
// sides listed: what changed and what was claimed. One side alone is not
// diagnosable — the operator needs to see which of the two is wrong.
func (r *Reconciliation) RefusalError(goals []Goal) error {
	if len(r.Unclaimed) == 0 {
		return nil
	}
	var claimed []string
	for i := range goals {
		g := &goals[i]
		if g.Status == GoalNotStarted {
			continue
		}
		claimed = append(claimed, g.Paths...)
		if !None(g.Evidence) {
			claimed = append(claimed, g.Evidence+"/")
		}
	}
	sort.Strings(claimed)
	return fmt.Errorf("%w: %d changed %s no goal claims:\n  %s\nthe contract claims:\n  %s",
		ErrRefused, len(r.Unclaimed), plural(len(r.Unclaimed), "path", "paths"),
		strings.Join(r.Unclaimed, "\n  "), strings.Join(claimed, "\n  "))
}
