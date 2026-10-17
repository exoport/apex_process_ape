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
	"unicode"
)

// Approvals are records, not text (H2). A milestone's `Status: approved
// (by <who> <ts>)` line is what a person reads; what ape trusts is the
// record `ape milestone approve` commits beside it in milestones.json:
// who approved, when, and the gate as it was approved — its command and
// the content of the files that command names. change start, milestone
// gate and milestone close refuse an approved milestone with no record
// (milestone.status-unrecorded: approved by hand, or before ape recorded
// approvals — a person approves it again), and gate and close refuse one
// whose gate changed since (milestone.gate-changed: a person reviews the
// gate and approves it again). `ape project check` reports both. An ADR
// is the same: its acceptance is the decisions.json record `ape decision
// accept` writes (decision.accepted-unrecorded without one).

// Codes of the approval records.
const (
	CodeStatusUnrecorded = "milestone.status-unrecorded"
	CodeGateChanged      = "milestone.gate-changed"
)

// MilestoneApproval is ape's record of a person approving a milestone, in
// milestones.json under the milestone's "approval".
type MilestoneApproval struct {
	By string `json:"by"`
	At string `json:"at"`
	// GateCmd is the Gate: command approved.
	GateCmd string `json:"gate_cmd"`
	// GateFiles maps each gate file — a word of the gate command that
	// names a regular file under the project root, e.g. ./scripts/e2e.sh
	// (gateFiles) — to its content's SHA-256 at approval;
	// GateFilesSHA256 digests the whole map (sorted "path sha" lines).
	GateFiles       map[string]string `json:"gate_files,omitempty"`
	GateFilesSHA256 string            `json:"gate_files_sha256"`
}

// gateFiles are the gate files of a gate command: its words, split at
// whitespace and the shell's ; | & < > ( ) and backquote, unquoted, the
// value of a KEY=value or --flag=value word, that name a regular file
// under the project root as it is now — absolute paths, paths leaving the
// root, options and words with $ or a glob character are not looked up.
// What the gate runs beyond the files it names (a script's own includes,
// package.json behind `npm run e2e`) is not covered.
func (p *Project) gateFiles(gate string) map[string]string {
	out := map[string]string{}
	words := strings.FieldsFunc(gate, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(";|&<>()`", r) })
	for _, w := range words {
		w = strings.Trim(w, `"'`)
		if _, v, ok := strings.Cut(w, "="); ok {
			w = strings.Trim(v, `"'`)
		}
		if w == "" || strings.HasPrefix(w, "-") || strings.ContainsAny(w, "$*?[~") || filepath.IsAbs(w) {
			continue
		}
		rel := filepath.Clean(filepath.FromSlash(w))
		if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		path := filepath.Join(p.Root, rel)
		if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
	}
	return out
}

func gateFilesDigest(files map[string]string) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n + " " + files[n] + "\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// newApproval records a person approving gate now.
func (p *Project) newApproval(gate, by string) *MilestoneApproval {
	files := p.gateFiles(gate)
	a := &MilestoneApproval{By: by, At: Stamp(), GateCmd: gate, GateFilesSHA256: gateFilesDigest(files)}
	if len(files) > 0 {
		a.GateFiles = files
	}
	return a
}

// gateDrift lists how m's gate differs from the approved one: its
// command, and each gate file added, removed or changed. Empty when it
// does not.
func (p *Project) gateDrift(m *Milestone, a *MilestoneApproval) []string {
	var out []string
	if strings.TrimSpace(m.Gate) != strings.TrimSpace(a.GateCmd) {
		out = append(out, "the command was `"+a.GateCmd+"`, is `"+m.Gate+"`")
	}
	now := p.gateFiles(m.Gate)
	if gateFilesDigest(now) == a.GateFilesSHA256 {
		return out
	}
	for _, n := range sortedKeys(keysOf(now, a.GateFiles)) {
		switch was, is := a.GateFiles[n], now[n]; {
		case was == "":
			out = append(out, n+" is new")
		case is == "":
			out = append(out, n+" is gone")
		case was != is:
			out = append(out, n+" changed")
		}
	}
	if len(out) == 0 {
		out = append(out, "the gate files changed")
	}
	return out
}

func keysOf(ms ...map[string]string) map[string]bool {
	out := map[string]bool{}
	for _, m := range ms {
		for k := range m {
			out[k] = true
		}
	}
	return out
}

// requireApprovalRecord refuses an approved milestone ape holds no
// approval record of (milestone.status-unrecorded) and, with checkGate,
// one whose gate changed since it was approved (milestone.gate-changed).
func (p *Project) requireApprovalRecord(m *Milestone, checkGate bool) error {
	recs, err := p.LoadMilestoneRecords()
	if err != nil {
		return err
	}
	rec := recs[m.ID]
	if rec == nil || rec.Approval == nil {
		return &FailError{Code: CodeStatusUnrecorded, Msg: m.ID + " says " + m.StatusText + ", but ape has no record of a person approving it (approved by hand, or before ape recorded approvals)" +
			" — a person approves it: `ape milestone approve " + m.ID + " --by <who>`"}
	}
	if !checkGate {
		return nil
	}
	if drift := p.gateDrift(m, rec.Approval); len(drift) > 0 {
		return &FailError{Code: CodeGateChanged, Msg: m.ID + "'s gate changed since " + rec.Approval.By + " approved it: " + strings.Join(drift, "; ") +
			" — a person reviews the gate and approves it again: `ape milestone approve " + m.ID + " --by <who>`"}
	}
	return nil
}

// milestoneRecordFindings is `ape project check`'s side of H2: a Status
// line ape holds no record behind, and a gate changed since its approval.
func (p *Project) milestoneRecordFindings() []Finding {
	rm := p.roadmap()
	if rm == nil {
		return nil
	}
	recs, err := p.LoadMilestoneRecords()
	if err != nil {
		return nil
	}
	file := p.Rel(p.RoadmapMD())
	var out []Finding
	for i := range rm.Milestones {
		m := &rm.Milestones[i]
		rec := recs[m.ID]
		switch m.Status {
		case MilestoneApproved:
			if rec == nil || rec.Approval == nil {
				out = append(out, Finding{File: file, Line: m.StatusLine, Message: m.ID + " says approved, but ape has no record of a person approving it" +
					" — a person approves it: `ape milestone approve " + m.ID + " --by <who>`"})
				continue
			}
			if drift := p.gateDrift(m, rec.Approval); len(drift) > 0 {
				out = append(out, Finding{File: file, Line: m.StatusLine, Message: m.ID + " gate changed since it was approved (" + strings.Join(drift, "; ") + ")" +
					" — a person reviews the gate and approves it again: `ape milestone approve " + m.ID + " --by <who>`"})
			}
		case MilestoneDone:
			if rec == nil || rec.DoneAt == "" && rec.Result != GateGreen {
				out = append(out, Finding{File: file, Line: m.StatusLine, Message: m.ID + " says done, but ape recorded no close of it (a green gate, or a red one a person accepted)" +
					" — restore its Status: line; `ape milestone close " + m.ID + "` writes it"})
			}
		}
	}
	return out
}

// CodeDecisionUnrecorded: an ADR that says accepted with no record.
const CodeDecisionUnrecorded = "decision.accepted-unrecorded"

// LiftBy is the "by" of a decisions.json record the lift migration
// writes (RecordLiftedDecisions) for an ADR it converted from a v0
// project, which no person accepted through ape.
const LiftBy = "lift"

// decisionRecordFinding reports an ADR whose Status says accepted with no
// decisions.json record behind it.
func (p *Project) decisionRecordFinding(id, file, doc string) []Finding {
	if adrStatus(doc) != ADRAccepted {
		return nil
	}
	if _, ok := p.loadDecisionRecords()[id]; ok {
		return nil
	}
	return []Finding{{File: file, Message: id + " is accepted, but ape has no record of a person accepting it" +
		" — a person records it: `ape decision accept " + id + " --by <who>`"}}
}

// RecordLiftedDecisions records each ADR in ids — ADR-NNNN, an accepted
// ADR the lift migration converted — in decisions.json as accepted by
// LiftBy now, with its body as it is: from then on `ape project check`
// holds it to that text and does not report it unrecorded. It writes
// decisions.json and commits nothing: the lift commits it with the rest
// of what it writes. An id with no ADR file, or whose ADR is not
// accepted, is an error and nothing is written.
func (p *Project) RecordLiftedDecisions(ids []string) error {
	recs := p.loadDecisionRecords()
	for _, id := range ids {
		path, ok := p.adrFile(id)
		if !ok {
			return &FailError{Code: CodeDecisionNotFound, Msg: "no decision " + id + " in " + p.Rel(p.DecisionsDir())}
		}
		doc, err := readFileString(path)
		if err != nil {
			return err
		}
		if st := adrStatus(doc); st != ADRAccepted {
			return fmt.Errorf("%s is %s, not accepted: the lift records only accepted decisions", id, st)
		}
		recs[id] = DecisionRecord{AcceptedAt: Stamp(), By: LiftBy, BodySHA256: adrBodyDigest(doc)}
	}
	return p.saveDecisionRecords(recs)
}

// recordAcceptedDecision is `ape decision accept` on an ADR whose Status
// already says accepted with no record (accepted by hand, or before ape
// recorded acceptances): it records it as it is, unless its text changed
// since git first committed it accepted.
func (p *Project) recordAcceptedDecision(ctx context.Context, id, path, doc, by string) (*DecisionResult, error) {
	if p.acceptedBodyChanged(ctx, id, path, doc) {
		return nil, &FailError{Code: "decision.accepted-edited", Msg: id + " was accepted and its text has changed since — restore it, then record it; " +
			"a different decision is a new ADR: `ape decision new <slug> --supersedes " + id + "`"}
	}
	recs := p.loadDecisionRecords()
	recs[id] = DecisionRecord{AcceptedAt: Stamp(), By: by, BodySHA256: adrBodyDigest(doc)}
	if err := p.saveDecisionRecords(recs); err != nil {
		return nil, err
	}
	res := &DecisionResult{ID: id, Path: p.Rel(path), Status: ADRAccepted}
	sha, err := p.commitRecord(ctx, "apex: "+id+" accepted (recorded)", "", p.Rel(p.decisionsJSON()))
	if err != nil {
		return res, &FailError{Code: CodeCommitFailed, Msg: "decision recorded but its commit failed: " + err.Error()}
	}
	res.Commit = sha
	return res, nil
}
