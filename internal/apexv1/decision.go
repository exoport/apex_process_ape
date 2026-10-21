package apexv1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// DecisionResult is what `ape decision new|accept` reports.
type DecisionResult struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Status     string `json:"status"`
	Supersedes string `json:"supersedes,omitempty"`
	Commit     string `json:"commit,omitempty"`
}

// ADR statuses.
const (
	ADRProposed = "proposed"
	ADRAccepted = "accepted"
)

var adrStatusLineRe = regexp.MustCompile(`(?m)^Status:[ \t]*(.*)$`)

// adrFile finds the file of ADR-NNNN.
func (p *Project) adrFile(id string) (string, bool) {
	if !adrIDRe.MatchString(id) {
		return "", false
	}
	matches, _ := filepath.Glob(filepath.Join(p.DecisionsDir(), strings.TrimPrefix(id, "ADR-")+"-*.md"))
	if len(matches) != 1 {
		return "", false
	}
	return matches[0], true
}

// adrStatus is the text of an ADR's Status: line.
func adrStatus(doc string) string {
	if m := adrStatusLineRe.FindStringSubmatch(doc); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func withADRStatus(doc, status string) string {
	return adrStatusLineRe.ReplaceAllLiteralString(doc, "Status: "+status)
}

// adrBodyDigest is the SHA-256 of an ADR without its Status: line — what
// may not change once it is accepted. The Status line may: superseding an
// ADR rewrites it.
func adrBodyDigest(doc string) string {
	body := adrStatusLineRe.ReplaceAllLiteralString(strings.ReplaceAll(doc, "\r\n", "\n"), "Status:")
	sum := sha256.Sum256([]byte(strings.TrimRight(body, "\n")))
	return hex.EncodeToString(sum[:])
}

// DecisionRecord is ape's record of an accepted ADR, committed in
// _apex/state/decisions.json: the body it was accepted with.
type DecisionRecord struct {
	AcceptedAt string `json:"accepted_at"`
	By         string `json:"by"`
	BodySHA256 string `json:"body_sha256"`
	// Standard is set for ADR-0001, APEX's standard, when ape wrote it:
	// the shipped version it holds and the digest of its bytes (D206).
	Standard *StandardRecord `json:"standard,omitempty"`
}

func (p *Project) decisionsJSON() string { return filepath.Join(p.StateDir(), "decisions.json") }

func (p *Project) loadDecisionRecords() map[string]DecisionRecord {
	out := map[string]DecisionRecord{}
	if data, err := os.ReadFile(p.decisionsJSON()); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

func (p *Project) saveDecisionRecords(r map[string]DecisionRecord) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	p.ensureState()
	return writeFileAtomic(p.decisionsJSON(), string(data)+"\n")
}

// NewDecision allocates the next ADR number and writes the skeleton. With
// supersedes (ADR-NNNN, accepted or proposed), the new ADR records
// `Supersedes: <old>` and the old one's Status becomes `superseded by
// <new>`; both are committed together.
func (p *Project) NewDecision(ctx context.Context, slug, title, supersedes string) (*DecisionResult, error) {
	if err := p.recoverArchive(ctx); err != nil {
		return nil, err
	}
	if !slugRe.MatchString(slug) {
		return nil, &UsageError{Code: "usage.decision-slug", Msg: fmt.Sprintf("decision slug %q must be kebab-case (e.g. use-sqlite)", slug)}
	}
	var oldPath, oldDoc string
	if supersedes != "" {
		path, ok := p.adrFile(supersedes)
		if !ok {
			return nil, &FailError{Code: CodeDecisionNotFound, Msg: "no decision " + supersedes + " in " + p.Rel(p.DecisionsDir()) + " — `ape decision new` lists numbers as ADR-NNNN"}
		}
		doc, err := readFileString(path)
		if err != nil {
			return nil, err
		}
		if st := adrStatus(doc); st != ADRAccepted && st != ADRProposed {
			return nil, &FailError{Code: "decision.not-supersedable", Msg: supersedes + " is " + st + " — only a proposed or accepted decision can be superseded"}
		}
		oldPath, oldDoc = path, doc
	}
	maxN := 0
	entries, _ := os.ReadDir(p.DecisionsDir())
	for _, e := range entries {
		if m := adrFileRe.FindStringSubmatch(e.Name()); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > maxN {
				maxN = n
			}
			if m[2] == slug {
				return nil, &FailError{Code: "decision.slug-taken", Msg: "decision " + e.Name() + " already uses slug " + slug}
			}
		}
	}
	num := fmt.Sprintf("%04d", maxN+1)
	id := "ADR-" + num
	if title == "" {
		title = titleFromSlug(slug)
	}
	path := filepath.Join(p.DecisionsDir(), num+"-"+slug+".md")
	head := "# " + id + ": " + title + "\n" +
		"Status: " + ADRProposed + "\n" +
		"Date: " + Today() + "\n"
	if supersedes != "" {
		head += "Supersedes: " + supersedes + "\n"
	}
	body := head + "\n" +
		"## Context\n" + adrPlaceholders[0] + "\n\n" +
		"## Decision\n" + adrPlaceholders[1] + "\n\n" +
		"## Consequences\n" + adrPlaceholders[2] + "\n\n" +
		"Enforced by: review\n"
	ownRels := []string{p.Rel(path)}
	if oldPath != "" {
		ownRels = append(ownRels, p.Rel(oldPath))
	}
	own := p.beforeOwnEdit(ctx, ownRels...)
	if err := os.MkdirAll(p.DecisionsDir(), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, &FailError{Code: "decision.write-failed", Msg: "cannot create " + p.Rel(path) + ": " + err.Error()}
	}
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	res := &DecisionResult{ID: id, Path: p.Rel(path), Status: ADRProposed, Supersedes: supersedes}
	paths := []string{res.Path}
	subject := "apex: " + id + " proposed"
	if oldPath != "" {
		if err := writeFileAtomic(oldPath, withADRStatus(oldDoc, "superseded by "+id)); err != nil {
			return res, err
		}
		paths = append(paths, p.Rel(oldPath))
		subject += ", supersedes " + supersedes
	}
	sha, err := p.commitOwnRecord(ctx, own, subject, "", paths...)
	if err != nil {
		return res, commitFailed(err, "decision written but its commit failed: "+err.Error())
	}
	res.Commit = sha
	return res, nil
}

// adrPlaceholders are the comments `ape decision new` writes under each
// section of its skeleton, for a person to replace.
var adrPlaceholders = [...]string{"<!-- the forces at play -->", "<!-- what we decided -->", "<!-- what becomes easier or harder -->"}

// adrPlaceholdersIn are the skeleton's placeholders an ADR still holds,
// outside code blocks.
func adrPlaceholdersIn(doc string) []string {
	var out []string
	for _, l := range splitLines(doc) {
		if l.InCode {
			continue
		}
		for _, ph := range adrPlaceholders {
			if strings.Contains(l.Text, ph) && !slices.Contains(out, ph) {
				out = append(out, ph)
			}
		}
	}
	return out
}

// decisionPlaceholderNotices are project check's notices for an accepted
// ADR that still holds the skeleton's placeholders (Q155): accepted before
// decision accept refused one. Its accepted text is fixed, so the remedy
// is a new ADR, and the notice never fails the check.
func (p *Project) decisionPlaceholderNotices() []Diagnostic {
	entries, err := os.ReadDir(p.DecisionsDir())
	if err != nil {
		return nil
	}
	var out []Diagnostic
	for _, e := range entries {
		m := adrFileRe.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			continue
		}
		path := filepath.Join(p.DecisionsDir(), e.Name())
		doc, err := readFileString(path)
		if err != nil || adrStatus(doc) != ADRAccepted {
			continue
		}
		if ph := adrPlaceholdersIn(doc); len(ph) > 0 {
			id := "ADR-" + m[1]
			out = append(out, notice(Diagnostic{Code: CodeDecisionPlaceholderAccepted, File: p.Rel(path), Message: id +
				" was accepted with the skeleton's placeholder " + strings.Join(ph, ", ") + ", so it records no decision" +
				" — write the decision as a new ADR, `ape decision new <slug> --supersedes " + id + "`, and accept that"}))
		}
	}
	return out
}

// AcceptDecision sets a proposed ADR's Status to accepted and records the
// body it was accepted with; from then on `ape project check` refuses an
// edit to it (supersede it instead).
func (p *Project) AcceptDecision(ctx context.Context, id, by string) (*DecisionResult, error) {
	// A person's acceptance (OI-4): a model never accepts its own decision.
	if err := requirePerson(ctx, ActDecisionAccept, "ape decision accept"); err != nil {
		return nil, err
	}
	if err := p.recoverArchive(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(by) == "" {
		return nil, &UsageError{Code: "usage.by", Msg: "--by names who accepts the decision"}
	}
	// A person's acceptance is recorded on the base branch (D11, D38).
	if err := p.refuseOnChangeBranch(ctx, "accepting a decision"); err != nil {
		return nil, err
	}
	path, ok := p.adrFile(id)
	if !ok {
		return nil, &FailError{Code: CodeDecisionNotFound, Msg: "no decision " + id + " in " + p.Rel(p.DecisionsDir())}
	}
	doc, err := readFileString(path)
	if err != nil {
		return nil, err
	}
	if st := adrStatus(doc); st != ADRProposed {
		if _, recorded := p.loadDecisionRecords()[id]; st == ADRAccepted && !recorded {
			return p.recordAcceptedDecision(ctx, id, path, doc, strings.TrimSpace(by))
		}
		return nil, &FailError{Code: "decision.not-proposed", Msg: id + " is " + st + " — only a proposed decision is accepted"}
	}
	if fs := ValidateDecision(p.Rel(path), filepath.Base(path), doc); len(fs) > 0 {
		return nil, &FailError{Code: "decision.malformed", Msg: id + " is not well-formed — fix it before accepting", Findings: fs}
	}
	// Q155: the skeleton `ape decision new` wrote records no decision.
	if ph := adrPlaceholdersIn(doc); len(ph) > 0 {
		return nil, &FailError{Code: CodeDecisionPlaceholder, Msg: id + " still holds the skeleton's placeholder " + strings.Join(ph, ", ") +
			" — write its Context, Decision and Consequences, then accept it"}
	}
	accepted := withADRStatus(doc, ADRAccepted)
	own := p.beforeOwnEdit(ctx, p.Rel(path), p.Rel(p.decisionsJSON()))
	if err := writeFileAtomic(path, accepted); err != nil {
		return nil, err
	}
	recs := p.loadDecisionRecords()
	recs[id] = DecisionRecord{AcceptedAt: Stamp(), By: strings.TrimSpace(by), BodySHA256: adrBodyDigest(accepted)}
	if err := p.saveDecisionRecords(recs); err != nil {
		return nil, err
	}
	res := &DecisionResult{ID: id, Path: p.Rel(path), Status: ADRAccepted}
	sha, err := p.commitOwnRecord(ctx, own, "apex: "+id+" accepted", "", res.Path, p.Rel(p.decisionsJSON()))
	if err != nil {
		return res, commitFailed(err, "decision accepted but its commit failed: "+err.Error())
	}
	res.Commit = sha
	return res, nil
}

// acceptedBodyChanged reports whether an accepted (or since superseded) ADR's
// body differs from the one it was accepted with: ape's record when
// there is one, else the file as git first committed it accepted. Unknown
// (no record, no such commit) is not a change.
func (p *Project) acceptedBodyChanged(ctx context.Context, id, path, doc string) bool {
	status := adrStatus(doc)
	if status != ADRAccepted && !strings.HasPrefix(status, "superseded by ") {
		return false
	}
	if rec, ok := p.loadDecisionRecords()[id]; ok {
		return rec.BodySHA256 != adrBodyDigest(doc) && !standardADRUnchanged(id, doc, rec)
	}
	first, err := p.firstAcceptedVersion(ctx, path)
	if err != nil || first == "" {
		return false
	}
	return adrBodyDigest(first) != adrBodyDigest(doc)
}

// firstAcceptedVersion is the file's content at the oldest commit in which
// its Status was accepted.
func (p *Project) firstAcceptedVersion(ctx context.Context, path string) (string, error) {
	rel := p.Rel(path)
	out, err := git(ctx, p.Root, "log", "--reverse", "--format=%H", "--", rel)
	if err != nil || out == "" {
		return "", err
	}
	for sha := range strings.SplitSeq(out, "\n") {
		content, err := gitRaw(ctx, p.Root, "show", sha+":"+rel)
		if err != nil {
			continue
		}
		if adrStatus(content) == ADRAccepted {
			return content, nil
		}
	}
	return "", errors.New("never committed accepted")
}
