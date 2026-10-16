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
}

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

// Accept records a person accepting a human-review task.
type Accept struct {
	Task string `json:"task"`
	By   string `json:"by"`
	At   string `json:"at"`
}

// State is changes/<id>/state.json. Written only by ape.
type State struct {
	ID        string     `json:"id"`
	Status    string     `json:"status"`
	BaseSHA   string     `json:"base_sha"`
	StartedAt string     `json:"started_at"`
	Baseline  []Baseline `json:"baseline"`
	// PlanDigest is the SHA-256 of change.md's ## Delta and ## Tasks at
	// start. verify and archive refuse while it differs (plan drift).
	PlanDigest string `json:"plan_digest,omitempty"`
	// BaseFingerprints maps "<capability>/<requirement>" to the SHA-256 of
	// the spec block each MODIFIED or REMOVED delta replaces, as it was at
	// start (backlog item 9). Archive refuses (exit 3) when the live block
	// differs: another change — on another branch — changed it since.
	BaseFingerprints  map[string]string `json:"base_fingerprints,omitempty"`
	Replans           []Replan          `json:"replans,omitempty"`
	Verify            *Verify           `json:"verify"`
	Review            *Review           `json:"review"`
	Accepted          []Accept          `json:"accepted"`
	BlockedReason     *string           `json:"blocked_reason"`
	ReviewVoided      []ReviewVoid      `json:"review_voided,omitempty"`
	AllowPendingHuman bool              `json:"allow_pending_human,omitempty"`
	AbandonedReason   string            `json:"abandoned_reason,omitempty"`
	ArchivedAt        string            `json:"archived_at,omitempty"`
	ArchivedTo        string            `json:"archived_to,omitempty"`
	CreatedAt         string            `json:"created_at,omitempty"`
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
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s does not parse: %w", path, err)
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
