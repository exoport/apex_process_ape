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

// Approvals are records, not text (H2). A milestone's `Status: approved
// (by <who> <ts>)` line is what a person reads; what ape trusts is the
// record `ape milestone approve` commits beside it in milestones.json:
// who approved, when, and the gate as it was approved — its command and
// the content of the scripts that command runs (gateScripts). change start, milestone
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
	// GateFiles maps each gate file — a script of the gate command
	// (gateScripts) that was a regular file under the project root — to its
	// content's SHA-256 at approval; GateFilesSHA256 digests the whole map
	// (sorted "path sha" lines). Approvals recorded by rc.1 may hold more
	// files (every word that named one); gateDrift ignores the extra ones.
	GateFiles       map[string]string `json:"gate_files,omitempty"`
	GateFilesSHA256 string            `json:"gate_files_sha256"`
	// SelfCheck is the gate's run at approval (D70 B): red, its exit,
	// duration and log. Absent on approvals before rc.3.
	SelfCheck *GateSelfCheck `json:"self_check,omitempty"`
	// BaseSHA is the milestone's start (D130 Q60): HEAD at its first
	// approval, before ape's approval commit; a re-approval keeps it.
	// Absent on approvals before rc.5 (Base falls back to SelfCheck.SHA).
	BaseSHA string `json:"base_sha,omitempty"`
}

// Base is the milestone's start: BaseSHA, else the self-check's sha (the
// same HEAD, recorded since rc.3), else "".
func (a *MilestoneApproval) Base() string {
	switch {
	case a == nil:
		return ""
	case a.BaseSHA != "":
		return a.BaseSHA
	case a.SelfCheck != nil:
		return a.SelfCheck.SHA
	}
	return ""
}

// gateEnv is what every gate run gets on top of ape's environment (D130
// Q60): the milestone and its start, so a gate can test what changed
// since — `git diff --quiet "$APE_MILESTONE_BASE" HEAD -- docs/`. An empty
// base (an approval before rc.3) is passed empty: the gate must fail on it
// rather than diff against nothing.
func gateEnv(id, base string) []string {
	return []string{"APE_MILESTONE=" + id, "APE_MILESTONE_BASE=" + base}
}

// gateFiles are the gate files of a gate command: each of its scripts
// (gateScripts) that is a regular file under the project root as it is
// now, with its content's SHA-256. What a script runs in turn (its own
// includes, package.json behind `npm run e2e`) is not covered.
func (p *Project) gateFiles(gate string) map[string]string {
	out := map[string]string{}
	for _, rel := range gateScripts(gate) {
		if sum, ok := p.fileSHA256(rel); ok {
			out[rel] = sum
		}
	}
	return out
}

// fileSHA256 is the SHA-256 of rel's content, when rel is a regular file
// under the project root.
func (p *Project) fileSHA256(rel string) (string, bool) {
	path := filepath.Join(p.Root, filepath.FromSlash(rel))
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), true
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
// command, and each recorded gate file that is gone or changed. It
// compares file by file, only the recorded files the gate still runs as a
// script: a script that did not exist at approval is not drift (it was
// never approved, and the gate fails without it), nor is a file an older
// approval recorded that the gate does not run (rc.1 recorded data files
// too). Empty when it does not differ.
func (p *Project) gateDrift(m *Milestone, a *MilestoneApproval) []string {
	var out []string
	if strings.TrimSpace(m.Gate) != strings.TrimSpace(a.GateCmd) {
		out = append(out, "the command was `"+a.GateCmd+"`, is `"+m.Gate+"`")
	}
	for _, n := range gateScripts(m.Gate) {
		was, ok := a.GateFiles[n]
		if !ok {
			continue
		}
		switch is, exists := p.fileSHA256(n); {
		case !exists:
			out = append(out, n+" is gone")
		case was != is:
			out = append(out, n+" changed")
		}
	}
	return out
}

// gateScriptUnrecorded reports a script of m's gate that exists now and
// that a's record does not hold.
func (p *Project) gateScriptUnrecorded(m *Milestone, a *MilestoneApproval) bool {
	for n := range p.gateFiles(m.Gate) {
		if _, ok := a.GateFiles[n]; !ok {
			return true
		}
	}
	return false
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
			if rec == nil || rec.DoneAt == "" && rec.Result != GateGreen && rec.AcceptRed == nil {
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
