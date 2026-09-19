package change

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Everything the contract has to satisfy before ape composes a single
// commit from it.
//
// The contract is model-written, and every field here becomes either a
// commit message or a git pathspec. Those are the two places where a
// string stops being a description and starts being an instruction:
//
//   - a newline in `subject` forges a commit body, and a line that looks
//     like `Request: …` forges a trailer ape's own readers believe;
//   - a path outside the lane's ownership is a commit over files nobody
//     agreed this lane may touch;
//   - an `evidence` path pointing at `src/` turns the evidence commit —
//     which is deliberately exempt from the deny-list, since evidence
//     lives under {development_folder} — into a door around it.
//
// None of this is a judgement about the model. It is that ape cannot
// tell a mistake from a forgery by reading the string, so it checks the
// shape instead.

// ErrRefused is what exit 6 reports: the run happened and ape will not
// turn it into commits.
var ErrRefused = errors.New("the commit was refused")

// subjectRe is the conventional-commit shape the lane's commits take.
// Anchored, and the type list is closed: a lane commit is maintenance,
// so `feat(x):` is as far as it goes.
var subjectRe = regexp.MustCompile(`^(fix|docs|build|chore|style|feat)\([^)]+\): .+$`)

// maxDeferredBody caps a deferred finding's body. It may span lines —
// it is a finding, not a commit field — so it takes a size limit where
// the others take a line-shape check. 16 KiB is far more than a finding
// needs and far less than a file someone pasted by accident.
const maxDeferredBody = 16 << 10

// ValidateGoals checks every goal that will be committed or reconciled.
//
// Ordering matters: this runs before the FIRST commit, not per goal as
// ape goes. A contract whose third goal is malformed must not leave the
// first two committed and the run refused half way, because that is a
// state the operator has to untangle by hand.
func (l Layout) ValidateGoals(goals []Goal) error {
	evidenceOf := map[int]string{}
	for i := range goals {
		g := &goals[i]
		n := i + 1
		if g.Status == GoalNotStarted {
			// A goal that never started claims nothing and commits
			// nothing. Its fields are the template's placeholders.
			continue
		}
		paths, err := l.checkGoalPaths(g, n)
		if err != nil {
			return err
		}
		g.Paths = paths

		ev, err := l.checkEvidence(g, n)
		if err != nil {
			return err
		}
		if ev != "" {
			evidenceOf[n] = ev
		}
		if err := checkDeferred(g, n); err != nil {
			return err
		}
		if g.Status != GoalLanded {
			// The rest of the fields become a commit message, and a
			// halted goal has no commit.
			continue
		}
		if err := checkCommitFields(g, n); err != nil {
			return err
		}
	}
	return checkEvidenceDisjoint(evidenceOf)
}

// checkGoalPaths cleans the goal's paths and holds them to the lane's
// ownership: not under the four roots, and never an evidence path.
//
// The evidence carve-out is deliberately NOT applied here. Evidence is
// committed separately, by its own commit, so a goal listing an evidence
// path among the files it changed would sweep it into the product commit
// and out of the release record's reach.
func (l Layout) checkGoalPaths(g *Goal, n int) ([]string, error) {
	out := make([]string, 0, len(g.Paths))
	for _, raw := range g.Paths {
		p, err := cleanRelPath(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: goal %d: %w", ErrRefused, n, err)
		}
		if err := singleLine(p, fmt.Sprintf("goal %d path %q", n, raw)); err != nil {
			return nil, err
		}
		if within(l.Evidence, p) {
			return nil, fmt.Errorf("%w: goal %d claims %s as a changed path, and that is inside "+
				"the evidence folder: evidence takes its own commit, and sweeping it into the "+
				"goal's would hide it from the release record", ErrRefused, n, p)
		}
		if root, denied := l.Denied(p); denied {
			return nil, fmt.Errorf("%w: goal %d claims %s, which is under %s — a folder this lane "+
				"does not own", ErrRefused, n, p, root)
		}
		out = append(out, p)
	}
	return out, nil
}

// checkEvidence holds the goal's evidence path strictly inside the
// evidence folder, and its triage note inside that.
func (l Layout) checkEvidence(g *Goal, n int) (string, error) {
	if None(g.Evidence) {
		if !None(g.Triage) {
			return "", fmt.Errorf("%w: goal %d has a triage note (%s) and no evidence path to "+
				"hold it", ErrRefused, n, g.Triage)
		}
		return "", nil
	}
	ev, err := cleanRelPath(g.Evidence)
	if err != nil {
		return "", fmt.Errorf("%w: goal %d evidence: %w", ErrRefused, n, err)
	}
	if err := singleLine(ev, fmt.Sprintf("goal %d evidence", n)); err != nil {
		return "", err
	}
	if !within(l.Evidence, ev) || ev == l.Evidence {
		return "", fmt.Errorf("%w: goal %d's evidence path %s is not inside %s. The evidence "+
			"commit is exempt from the deny-list, so a path outside the evidence folder would "+
			"be a door around it", ErrRefused, n, ev, l.Evidence)
	}
	if !None(g.Triage) {
		tr, trErr := cleanRelPath(g.Triage)
		if trErr != nil {
			return "", fmt.Errorf("%w: goal %d triage: %w", ErrRefused, n, trErr)
		}
		if err := singleLine(tr, fmt.Sprintf("goal %d triage", n)); err != nil {
			return "", err
		}
		if !within(ev, tr) || tr == ev {
			return "", fmt.Errorf("%w: goal %d's triage note %s is not inside its own evidence "+
				"path %s", ErrRefused, n, tr, ev)
		}
		g.Triage = tr
	}
	g.Evidence = ev
	return ev, nil
}

// checkEvidenceDisjoint refuses two goals sharing an evidence path, or
// one nesting inside another's.
//
// Sharing would make the first goal's evidence commit carry the second
// goal's files, so the second would find nothing left to commit and the
// record of which gate proved which goal would be gone.
func checkEvidenceDisjoint(evidenceOf map[int]string) error {
	for a, pa := range evidenceOf {
		for b, pb := range evidenceOf {
			if a >= b {
				continue
			}
			switch {
			case pa == pb:
				return fmt.Errorf("%w: goals %d and %d share the evidence path %s: one commit "+
					"would carry both goals' gate output", ErrRefused, a, b, pa)
			case within(pa, pb) || within(pb, pa):
				return fmt.Errorf("%w: goal %d's evidence path %s nests with goal %d's %s",
					ErrRefused, a, pa, b, pb)
			}
		}
	}
	return nil
}

// checkCommitFields holds the strings that become the commit message.
func checkCommitFields(g *Goal, n int) error {
	for _, f := range []struct{ name, value string }{
		{"subject", g.Subject},
		{"gates", g.Gates},
		{"governance", g.Governance},
	} {
		if err := singleLine(f.value, fmt.Sprintf("goal %d %s", n, f.name)); err != nil {
			return err
		}
	}
	if !subjectRe.MatchString(g.Subject) {
		return fmt.Errorf("%w: goal %d's subject %q is not `type(area): summary` with a type of "+
			"fix, docs, build, chore, style or feat", ErrRefused, n, g.Subject)
	}
	return nil
}

// checkDeferred holds the structured fields ape turns into records.
func checkDeferred(g *Goal, n int) error {
	for i := range g.Deferred {
		d := &g.Deferred[i]
		what := fmt.Sprintf("goal %d deferred finding %d", n, i+1)
		for _, f := range []struct{ name, value string }{
			{"title", d.Title},
			{"owner", d.Owner},
			{"trigger", d.Trigger},
		} {
			if err := singleLine(f.value, what+" "+f.name); err != nil {
				return err
			}
		}
		for _, a := range d.Anchors {
			if err := singleLine(a, what+" anchor"); err != nil {
				return err
			}
		}
		if strings.TrimSpace(d.Title) == "" {
			return fmt.Errorf("%w: %s has no title", ErrRefused, what)
		}
		// The body is the finding and may span lines, so it takes a size
		// cap where the others take a shape check.
		if len(d.Body) > maxDeferredBody {
			return fmt.Errorf("%w: %s has a %d-byte body, over the %d-byte cap: a finding, not a file",
				ErrRefused, what, len(d.Body), maxDeferredBody)
		}
	}
	return nil
}

// singleLine refuses a newline or a control character in a field that
// becomes a commit message line or a pathspec.
//
// The newline is the one that matters: a `subject` holding
// "fix(x): y\n\nRequest: something else" writes a commit whose trailers
// ape's own readers — and the release record's — would believe.
func singleLine(v, what string) error {
	if strings.ContainsAny(v, "\n\r") {
		return fmt.Errorf("%w: %s holds a line break, and it becomes a commit message line: "+
			"a second line there forges a trailer", ErrRefused, what)
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %s holds a control character (%#U)", ErrRefused, what, r)
		}
	}
	return nil
}
