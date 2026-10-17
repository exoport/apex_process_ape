package apexv1

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// H14 (D47 Q5): provenance. The guards stop the Edit tool, not Bash, so a
// spec or a state.json can still be written around ape. History says who
// did: since the v1 epoch, a spec changes only in an archive squash, and a
// change's state.json only in ape's own commits. Nothing before the epoch
// is judged — that history predates the rules.

func init() {
	findingRules = append(findingRules,
		rule("", `^edited outside an archive squash`, "spec.edited"),
		rule("", `^changed outside an apex: commit`, "state.forged"),
	)
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{"change.provenance", 1, ActionPerson, "start, verify, archive: since the v1 epoch a commit edited a spec outside an archive squash, or a state.json outside ape's commits (findings follow); a person restores them"},
		CodeDoc{"spec.edited", 1, ActionPerson, "a commit since the v1 epoch changed development/specs/** and is not an archive squash (a `Change: <id>` trailer whose change is archived): specs change only through ape change archive"},
		CodeDoc{"state.forged", 1, ActionPerson, "a commit since the v1 epoch changed a change's state.json and is neither ape's (subject `apex: …`) nor an archive squash: only ape writes state.json"},
	)
}

var stateJSONPathRe = regexp.MustCompile(`^(changes|archive)/[^/]+/state\.json$`)

// provenanceFindings walks the history since the epoch once — the commits
// touching specs/, changes/ or archive/, with their files — and reports
// each spec or state.json written by a commit that may not write it.
// An archive squash is identified by its `Change: <id>` trailer naming an
// archived change, never by its subject (the subject is configurable).
// A `git revert` of such a commit, and the commit it reverts, are not
// reported: reverting is how a person puts the history right.
func (p *Project) provenanceFindings(ctx context.Context) []Finding {
	epoch := p.v1Epoch(ctx, "HEAD")
	if epoch == "" {
		return nil
	}
	dev := p.DevRel + "/"
	out, err := gitRaw(ctx, p.Root, "log", "--no-renames", "--relative", "--name-only",
		"--format=%x1e%H%x1f%s%x1f%(trailers:key=Change,valueonly,separator=%x2c)%x1f%P%x1f%b%x1d",
		epoch+"..HEAD", "--", dev+"specs", dev+"changes", dev+"archive")
	if err != nil {
		return nil
	}
	type commit struct {
		sha, subject, parent string
		squash, apex         bool
		files                []string
	}
	archived := p.ArchivedIDs()
	reverted := map[string]bool{}
	var commits []commit
	for rec := range strings.SplitSeq(out, "\x1e") {
		head, files, ok := strings.Cut(rec, "\x1d")
		parts := strings.SplitN(head, "\x1f", 5)
		if !ok || len(parts) < 5 {
			continue
		}
		parent, _, _ := strings.Cut(parts[3], " ")
		c := commit{sha: parts[0], subject: parts[1], parent: parent, apex: strings.HasPrefix(parts[1], "apex:")}
		for id := range strings.SplitSeq(parts[2], ",") {
			if id = strings.TrimSpace(id); id != "" {
				if _, ok := archived[id]; ok {
					c.squash = true
				}
			}
		}
		if m := revertsRe.FindStringSubmatch(parts[4]); m != nil {
			reverted[m[1]], reverted[c.sha] = true, true
		}
		for f := range strings.SplitSeq(files, "\n") {
			if f = strings.TrimSpace(f); f != "" {
				c.files = append(c.files, f)
			}
		}
		commits = append(commits, c)
	}
	reported := map[string]bool{}
	var fs []Finding
	for _, c := range commits {
		if reverted[c.sha] {
			continue
		}
		short := c.sha[:min(7, len(c.sha))]
		by := fmt.Sprintf("commit %s (%q)", short, c.subject)
		for _, f := range c.files {
			rel, inDev := strings.CutPrefix(f, dev)
			if !inDev || reported[f] {
				continue
			}
			switch {
			case strings.HasPrefix(rel, "specs/") && !c.squash:
				reported[f] = true
				fs = append(fs, Finding{File: f, Message: "edited outside an archive squash by " + by +
					", which carries no `Change:` trailer of an archived change — specs change only through `ape change archive`: " +
					"a person reverts it (git revert " + short + ") and re-plans the edit as a change"})
			case stateJSONPathRe.MatchString(rel) && !c.apex && !c.squash && !p.resolvesConflict(ctx, c.parent, rel):
				reported[f] = true
				fs = append(fs, Finding{File: f, Message: "changed outside an apex: commit by " + by +
					" — only ape writes state.json: a person reverts it (git revert " + short + ") and lets the ape command record the status"})
			}
		}
	}
	return fs
}

// conflictedRe is the subject of ape's commit that leaves a rebase
// conflict on change/<id> (H8): the change's state, with its conflict list
// staged for the resolution commit.
var conflictedRe = regexp.MustCompile(`^apex: (\S+) conflicted rebasing onto `)

// resolvesConflict reports a commit whose first parent is ape's
// conflicted-rebase commit of the change whose state.json (rel, under the
// development folder) it writes: apex-apply's resolution commit, which
// carries the state ape staged and could not commit itself (D47 Q3).
func (p *Project) resolvesConflict(ctx context.Context, parent, rel string) bool {
	id, ok := strings.CutPrefix(rel, "changes/")
	if !ok || parent == "" {
		return false
	}
	id = strings.TrimSuffix(id, "/state.json")
	subject, err := git(ctx, p.Root, "log", "-1", "--format=%s", parent)
	if err != nil {
		return false
	}
	m := conflictedRe.FindStringSubmatch(subject)
	return m != nil && m[1] == id
}

// revertsRe is the line `git revert` writes into a revert's message.
var revertsRe = regexp.MustCompile(`(?m)^This reverts commit ([0-9a-f]{40})`)

// Provenance is provenanceFindings for the change commands: nil when the
// history since the epoch is clean, else the refusal start, verify and
// archive return.
func (p *Project) Provenance(ctx context.Context) error {
	fs := p.provenanceFindings(ctx)
	if len(fs) == 0 {
		return nil
	}
	return &FailError{Code: "change.provenance", Findings: fs, Msg: fmt.Sprintf(
		"%d spec or state.json write(s) since the v1 epoch did not come from ape — a person restores them (`ape project check` lists them)", len(fs))}
}
