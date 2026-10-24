package apexv1

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/exoport/apex_process_ape/internal/atomicfile"
)

// Change statuses. Only ape moves a change between them.
const (
	StatusDraft      = "draft"
	StatusInProgress = "in-progress"
	StatusVerified   = "verified"
	StatusReviewed   = "reviewed"
	StatusBlocked    = "blocked"
	StatusArchived   = "archived"
	// StatusAbandoned is a change moved to the archive without merging
	// its delta (`ape change abandon`, trial decision D6).
	StatusAbandoned = "abandoned"
	// StatusAnswered is a blocked change a person answered (`ape change
	// answer`, H9): the orchestrator re-plans it with the answer as the
	// hint and starts it again.
	StatusAnswered = "answered"
)

// CheckResult is one command check's run: a task's, or a scenario's of
// the effective spec (Kind, Capability, Requirement, Scenario say which;
// Task then holds "<capability>/<requirement>/<scenario>").
type CheckResult struct {
	Task         string  `json:"task"`
	Kind         string  `json:"kind,omitempty"`
	Capability   string  `json:"capability,omitempty"`
	Requirement  string  `json:"requirement,omitempty"`
	Scenario     string  `json:"scenario,omitempty"`
	Delta        bool    `json:"delta,omitempty"`
	Cmd          string  `json:"cmd"`
	Exit         int     `json:"exit"`
	Secs         float64 `json:"secs"`
	OutputSHA256 string  `json:"output_sha256"`
	// Log is the project-relative path of the full output.
	Log string `json:"log,omitempty"`
	// Tail is the last lines of output — what a red check is reported by.
	Tail []string `json:"-"`
	// StrayFiles is what this check left in the tree outside the
	// development folder and ape undid: files it created (removed) and
	// tracked files it changed (restored). Item 19.
	StrayFiles []string `json:"stray_files,omitempty"`
	// StraysKept are the folders (under _apex/state/strays/) holding each
	// undone path as it was when the check ended (Q144): what ape undoes is
	// never lost, a concurrent writer's edit included.
	StraysKept []string `json:"strays_kept,omitempty"`
	// PreExistingRed marks a regression command that is red and was red
	// at its baseline: reported, not a failure (backlog 32).
	PreExistingRed bool `json:"pre_existing_red,omitempty"`
	// Reason marks a check red for what it did not run (H11): "vacuous"
	// (exit 0, output saying it ran nothing; Exit is ExitVacuous) or
	// "unrun" (a requirement missing, never run; Exit is ExitUnrun).
	// Detail says which phrase or requirement.
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	// ShellNote: exit 127, the output saying a bash/zsh builtin was not
	// found — /bin/sh has none of them where it is dash (RN-1).
	ShellNote string `json:"shell_note,omitempty"`
	// Leftovers: what the check left running when it exited — each
	// "<pid> <command>" ("<pid>" on Windows) — and ape ended (F15).
	Leftovers []string `json:"leftovers,omitempty"`
	// Flaky: red, then green on a retry (H12, retries:); the result is
	// green. Tries counts the runs when there was more than one; Log is the
	// last run's, the earlier ones beside it as <name>.try<k>.log.
	Flaky bool `json:"flaky,omitempty"`
	Tries int  `json:"tries,omitempty"`
	// StaleNames: a red scenario of the lasting spec whose check names
	// what `ape decision standard` removed (D232 Q131) — the names;
	// StalePointer says how to retire it.
	StaleNames []string `json:"stale_names,omitempty"`
}

// Red reports a result that fails its run: a non-zero exit that was not
// already red at the baseline it is judged by.
func (r *CheckResult) Red() bool { return r.Exit != 0 && !r.PreExistingRed }

// Baseline is one task's result before any work began — or, for a task
// added or changed by a re-plan, at the re-plan (SHA says which).
type Baseline struct {
	Task string `json:"task"`
	Exit int    `json:"exit"`
	Cmd  string `json:"cmd,omitempty"`
	SHA  string `json:"sha,omitempty"`
}

// Replan records a re-start that re-baselined a change whose ## Tasks or
// ## Delta changed after it started.
type Replan struct {
	At   string `json:"at"`
	SHA  string `json:"sha"`
	From string `json:"from_digest"`
	To   string `json:"to_digest"`
}

// GreenAtBaseline is a check of the change's own — a task, or a scenario
// of its delta — that already passed at its baseline, before the code it
// is meant to prove existed. Such a check proves nothing about the change;
// review must justify or flag each one (backlog item 3).
type GreenAtBaseline struct {
	Task string `json:"task"`
	Kind string `json:"kind"`
	Cmd  string `json:"cmd"`
	SHA  string `json:"baseline_sha,omitempty"`
	// AtReplan: baselined at a re-plan, after code existed, rather than
	// at the first start.
	AtReplan bool `json:"at_replan,omitempty"`
	// Regression: the task is written `— regression: <command>`, a check
	// the plan declares may already pass (backlog 43).
	Regression bool `json:"regression,omitempty"`
}

// Verify is the last `ape change verify`.
type Verify struct {
	SHA          string        `json:"sha"`
	At           string        `json:"at"`
	Results      []CheckResult `json:"results"`
	PendingHuman []string      `json:"pending_human"`
	Green        bool          `json:"green"`
	// GreenAtBaseline lists the change's own checks that passed before
	// any code; always present (empty when none).
	GreenAtBaseline []GreenAtBaseline `json:"green_at_baseline"`
	// Stale marks a verify taken before ape caught the change up with a
	// base branch that moved (D40): kept for the record, it proves nothing
	// about the rebased code. StaleReason says why.
	Stale       bool   `json:"stale,omitempty"`
	StaleReason string `json:"stale_reason,omitempty"`
	// Regression is set when the verify skipped the regression command
	// (Q87): skipped "docs-only" and the docs paths the change touched.
	// A verify that ran it has it in Results (kind regression).
	Regression *RegressionVerdict `json:"regression,omitempty"`
	// FullSkipped counts the full scenarios it did not run (W2 Part 2).
	FullSkipped int `json:"full_skipped,omitempty"`
	// LiveSkipped is FullSkipped under its v1.2 JSON name, always the same
	// value (D512); a v1.2 record has it alone, and loading one fills
	// FullSkipped from it (UnmarshalJSON). Deprecated: read full_skipped;
	// live_skipped is removed no earlier than v1.5.
	LiveSkipped int `json:"live_skipped,omitempty"`
	// Advise is the change's advise files as this verify found them (W6,
	// Q-E11): {name, skill, file, bytes, sha256}, so an expert updated in
	// the middle of a change shows in the record.
	Advise []AdviseFile `json:"advise,omitempty"`
}

// UnmarshalJSON reads a verify record, filling FullSkipped from
// live_skipped for a record ape v1.2 wrote, which has that name alone
// (D512).
func (v *Verify) UnmarshalJSON(b []byte) error {
	type plain Verify
	if err := json.Unmarshal(b, (*plain)(v)); err != nil {
		return err
	}
	v.FullSkipped = fullSkippedOf(v.FullSkipped, v.LiveSkipped)
	v.LiveSkipped = v.FullSkipped
	return nil
}

// Approval records a person approving a tweak — a change with
// `milestone: none` (D40), which has no milestone to be approved through.
type Approval struct {
	By string `json:"by"`
	At string `json:"at"`
	// PlanDigest is the plan (change.md outside ## Why and ## Notes) the
	// person approved: start refuses a tweak whose plan changed since.
	PlanDigest string `json:"plan_digest"`
}

// Rebase records ape catching a change up with its base branch, which
// had moved ahead of base_sha (D40): From is the old base_sha, To the base
// tip the change now sits on, Head the change branch's HEAD after it.
// Rebased is false when the branch already held the base's commits (a
// person rebased it) and only the baseline was re-taken.
//
// A rebase that conflicted (H8) records OldTipRef, the ref keeping the
// branch's tip from before it (refs/apex/pre-rebase/<id>/<n>), and
// Conflicts, the files left with conflict markers on the branch for
// apex-apply to resolve; Head is then the base tip the branch was
// recreated at.
type Rebase struct {
	At        string   `json:"at"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	Head      string   `json:"head"`
	Rebased   bool     `json:"rebased"`
	OldTipRef string   `json:"old_tip_ref,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// Review is the recorded review verdict.
type Review struct {
	Verdict  string `json:"verdict"`
	At       string `json:"at"`
	SHA      string `json:"sha"`
	Findings string `json:"findings"`
}

// ReviewVoid records a verdict voided because its review session changed
// more than its review.md (D18 post-run assertion).
type ReviewVoid struct {
	At         string   `json:"at"`
	Run        string   `json:"run"`
	Verdict    string   `json:"verdict,omitempty"`
	Violations []string `json:"violations"`
}

// Answer records a person answering a blocked change (H9).
type Answer struct {
	By   string `json:"by"`
	At   string `json:"at"`
	Text string `json:"text"`
	// ReplansBefore is how many re-plans the change had when it was
	// answered: limits.replans judges the ones since (H19).
	ReplansBefore int `json:"replans_before,omitempty"`
}

// Accept records a person accepting a human-review task — or a delegate
// skill in a person's place (D75): By "delegate:<skill>", Run its run.
type Accept struct {
	Task string `json:"task"`
	By   string `json:"by"`
	At   string `json:"at"`
	// Evidence is what the acceptance rests on (`accept --evidence`).
	Evidence []Evidence `json:"evidence,omitempty"`
	// Run is the delegate run whose pass this acceptance records (`ape
	// change accept --delegate`); empty for a person's.
	Run string `json:"run,omitempty"`
}

// State is changes/<id>/state.json. Written only by ape.
type State struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	// BaseBranch is the branch `ape change start` created change/<id>
	// from, and BaseSHA HEAD then (D38): archive rebases the change branch
	// onto BaseBranch and squash-merges it there.
	BaseBranch string     `json:"base_branch,omitempty"`
	BaseSHA    string     `json:"base_sha"`
	StartedAt  string     `json:"started_at"`
	Baseline   []Baseline `json:"baseline"`
	// PlanDigest is the SHA-256 of change.md's ## Delta and ## Tasks at
	// start. verify and archive refuse while it differs (plan drift).
	PlanDigest string `json:"plan_digest,omitempty"`
	// BaseFingerprints maps "<capability>/<requirement>" to the SHA-256 of
	// the spec block each MODIFIED or REMOVED delta replaces, as it was at
	// start (backlog item 9). Archive refuses (exit 3) when the live block
	// differs: another change — on another branch — changed it since.
	BaseFingerprints map[string]string `json:"base_fingerprints,omitempty"`
	Replans          []Replan          `json:"replans,omitempty"`
	// Rebases are the catch-ups with a base branch that moved (D40).
	Rebases []Rebase `json:"rebases,omitempty"`
	// Approved is a tweak's approval (D40); nil for a milestone change.
	Approved *Approval `json:"approved,omitempty"`
	// Regression is the regression command's baseline (backlog 32): run
	// at the first start, kept across re-starts unless the command changed.
	Regression *RegressionBaseline `json:"regression_baseline,omitempty"`
	// MilestoneRegression is the milestone tier's baseline (W2 Part 1),
	// when the tiers differ: taken once per milestone and command, by the
	// first change of the milestone to start, so the gate can tell a red
	// that was there before the milestone's work from one it made.
	MilestoneRegression *RegressionBaseline `json:"milestone_regression_baseline,omitempty"`
	Verify              *Verify             `json:"verify"`
	Review              *Review             `json:"review"`
	Accepted            []Accept            `json:"accepted"`
	BlockedReason       *string             `json:"blocked_reason"`
	ReviewVoided        []ReviewVoid        `json:"review_voided,omitempty"`
	AllowPendingHuman   bool                `json:"allow_pending_human,omitempty"`
	// AllowPendingHumanBy is who archived it so (E2: a person's act in a
	// Claude Code session records its by and prompt).
	AllowPendingHumanBy string `json:"allow_pending_human_by,omitempty"`
	// Answer is a person's answer to what blocked the change (H9).
	Answer          *Answer `json:"answer,omitempty"`
	AbandonedReason string  `json:"abandoned_reason,omitempty"`
	ArchivedAt      string  `json:"archived_at,omitempty"`
	ArchivedTo      string  `json:"archived_to,omitempty"`
	CreatedAt       string  `json:"created_at,omitempty"`
	// BranchTip is change/<id>'s last commit when archive squash-merged
	// it or abandon deleted it (D38).
	BranchTip string `json:"branch_tip,omitempty"`
	// TipRef keeps BranchTip reachable once the branch is deleted (H16):
	// refs/apex/archive/<id> at archive, refs/apex/abandoned/<id> at
	// abandon. Local; pushed only by the project's own policy.
	TipRef string `json:"tip_ref,omitempty"`
	// DelegateReturns are, per human-review task, the delegate fails ape
	// sent back to the change (D188): each named a defect in the change's
	// own diff. One since a person last answered the change or accepted
	// the task makes the task's next fail a person's.
	DelegateReturns map[string][]DelegateReturn `json:"delegate_returns,omitempty"`
}

// Active reports whether a change is in flight: started and neither
// blocked, archived nor abandoned. A blocked change claims nothing: it
// takes no part in requirement-conflict detection (trial decision D6).
func (s *State) Active() bool {
	switch s.Status {
	case StatusInProgress, StatusVerified, StatusReviewed:
		return true
	}
	return false
}

// PendingHuman is the human-review tasks not yet accepted.
func (s *State) PendingHuman(humanTasks []string) []string {
	accepted := map[string]bool{}
	for _, a := range s.Accepted {
		accepted[a.Task] = true
	}
	var out []string
	for _, t := range humanTasks {
		if !accepted[t] {
			out = append(out, t)
		}
	}
	return out
}

// ErrNoState is returned for a change folder without state.json.
var ErrNoState = errors.New("no state.json")

// LoadState reads a state.json.
func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoState
	}
	if err != nil {
		return nil, err
	}
	return parseState(path, data)
}

// parseState parses a state.json's content; name says where it came from.
func parseState(name string, data []byte) (*State, error) {
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s does not parse: %w", name, err)
	}
	return &s, nil
}

// SaveState writes a state.json atomically.
func SaveState(path string, s *State) error {
	if s.Accepted == nil {
		s.Accepted = []Accept{}
	}
	if s.Baseline == nil {
		s.Baseline = []Baseline{}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'))
}

// now is the clock; tests replace it.
var now = time.Now

// Stamp is the timestamp format ape writes: RFC 3339, UTC, seconds.
func Stamp() string { return now().UTC().Format(time.RFC3339) }

// Today is the local date archive folders are named by.
func Today() string { return now().Format("2006-01-02") }
