package apexv1

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Attempts is one change's red-verify count (backlog 30), kept in
// _apex/state/attempts.json — ignored by git, so a red verify commits
// nothing (D38 restores D5). Since counts the red verifies since the
// change last started: every start, re-start included, sets it to 0, a
// green verify leaves it, and the red verify that brings it to
// limits.attempts blocks the change. Total is every red verify of the
// change in this checkout, never reset.
type Attempts struct {
	Since int `json:"since_start"`
	Total int `json:"total"`
	// AtAnswer is Total when a person last answered the change:
	// limits.attempts_total judges the red verifies since (H19).
	AtAnswer int `json:"total_at_answer,omitempty"`
}

// AttemptsJSON is _apex/state/attempts.json: change id → Attempts.
func (p *Project) AttemptsJSON() string { return filepath.Join(p.StateDir(), "attempts.json") }

func (p *Project) loadAttempts() map[string]Attempts {
	out := map[string]Attempts{}
	data, err := os.ReadFile(p.AttemptsJSON())
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	if out == nil {
		out = map[string]Attempts{}
	}
	return out
}

// attempts is a change's count.
func (p *Project) attempts(id string) Attempts { return p.loadAttempts()[id] }

// updateAttempts changes a change's count and writes the file.
func (p *Project) updateAttempts(id string, f func(*Attempts)) (Attempts, error) {
	all := p.loadAttempts()
	a := all[id]
	f(&a)
	all[id] = a
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return a, err
	}
	p.ensureState()
	return a, writeFileAtomic(p.AttemptsJSON(), string(data)+"\n")
}
