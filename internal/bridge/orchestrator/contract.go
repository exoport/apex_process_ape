package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// StepContract is the per-step verification spec the runner registers
// with a ContractVerifier before typing the step's slash command into
// claude's REPL via PTY Write (PLAN-6 / PLAN-8). PLAN-6 / C4 step
// contract:
//
//   - The UserPromptSubmit hook payload must match the agent-prefixed
//     skill prompt shape (PAT-25):
//     `/<Agent> --autonomous -- <Skill> --autonomous ...` when Agent != ""
//     `/<Skill> --autonomous --no-commit ...` when Agent == ""
//
// `/clear` between steps is driven by the runner (PTY Write) and
// fires its own UserPromptSubmit hook, but that hook arrives BETWEEN
// the previous step's EndStep and the next step's BeginStep — i.e.,
// outside any active contract window — so the verifier silently
// ignores it. The same applies to a `/model` switch when we add one
// back.
//
// Violations are hard-fail: a UserPromptSubmit that doesn't match the
// expected agent/skill prefix aborts the run via OnViolation, which
// the runner wires to runtime.RequestStop.
//
// Model and NoClear are caller-side bookkeeping fields surfaced in
// manifest records and used by the runner to decide whether to send
// `/clear` (NoClear opts the step out). The verifier itself ignores
// them.
type StepContract struct {
	Stage   string
	StepIdx int
	Skill   string
	Agent   string
	Model   string // informational; verifier no longer enforces /model
	NoClear bool   // runner uses this to skip inter-step /clear; verifier ignores
}

// ContractViolation describes a single step-contract failure.
type ContractViolation struct {
	Stage   string
	StepIdx int
	// Reason is a single-sentence description of which contract rule
	// fired and what the offending prompt looked like.
	Reason string
}

// ContractVerifier subscribes to a BridgeRuntime's UserPromptSubmit
// hook frames and enforces the per-step contract. The runner calls
// BeginStep before delivering each step's prompt via rt.SendMessage;
// the verifier then checks the next UserPromptSubmit against the
// expected agent/skill prefix and emits OnViolation on mismatch.
type ContractVerifier struct {
	mu sync.Mutex

	// active is the current step's contract. Nil when no step is in
	// flight (the verifier silently ignores prompts outside any step).
	active *StepContract

	// done is true once the active step's UserPromptSubmit has been
	// matched (or violated). Extra prompts after the match are
	// tolerated — the model may issue follow-on user turns internally
	// (skills that re-prompt themselves).
	done bool

	// OnViolation fires on the first contract failure for the active
	// step. The verifier disables further checks on this step after
	// firing (one violation per step is enough; the run aborts).
	OnViolation func(v ContractViolation)
}

// NewContractVerifier constructs a verifier. Wire it into a
// BridgeRuntime via SubscribeHookEvents / runtime.Subscribe.
func NewContractVerifier() *ContractVerifier {
	return &ContractVerifier{}
}

// BeginStep resets the verifier state for a new step. Call before
// delivering the step's prompt via rt.SendMessage so the verifier
// knows what to expect on the next UserPromptSubmit hook.
func (v *ContractVerifier) BeginStep(c StepContract) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.active = &c
	v.done = false
}

// EndStep clears the active contract. Call after the runner has
// detected step-done so a late UserPromptSubmit from the previous
// step doesn't get matched against a fresh contract.
func (v *ContractVerifier) EndStep() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.active = nil
}

// Consume processes a UserPromptSubmit hook payload. The runner wires
// this into BridgeRuntime's hook fan-out so every UserPromptSubmit
// passes through.
//
// The payload shape is the Claude Code hooks contract: a JSON object
// with a `prompt` string field holding the literal user input. Any
// other payload shape (parse failure, missing field) is treated as a
// violation — a UserPromptSubmit hook that can't be parsed is itself
// a contract regression, not a soft warning.
func (v *ContractVerifier) Consume(payload json.RawMessage) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.active == nil || v.done {
		return
	}
	prompt, err := extractPrompt(payload)
	if err != nil {
		v.violateLocked(fmt.Sprintf("UserPromptSubmit payload malformed: %v", err))
		return
	}
	if !skillPromptMatches(prompt, v.active.Skill, v.active.Agent) {
		v.violateLocked(fmt.Sprintf("expected skill prompt for %q (agent=%q), got %q",
			v.active.Skill, v.active.Agent, elide(prompt)))
		return
	}
	v.done = true
}

// HasViolated reports whether the current step has hit a violation.
// Useful for tests; production callers should rely on OnViolation.
func (v *ContractVerifier) HasViolated() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.active != nil && v.done && v.OnViolation == nil
}

func (v *ContractVerifier) violateLocked(reason string) {
	cb := v.OnViolation
	violation := ContractViolation{
		Stage:   v.active.Stage,
		StepIdx: v.active.StepIdx,
		Reason:  reason,
	}
	v.done = true // disable further checks on this step
	if cb != nil {
		// Release the lock for the callback to avoid re-entrant
		// deadlocks if the callback ends up calling back into the
		// verifier (e.g., for logging). The state transition above
		// is already final.
		v.mu.Unlock()
		cb(violation)
		v.mu.Lock()
	}
}

// extractPrompt pulls the `prompt` string out of a Claude Code
// UserPromptSubmit hook payload. The wire shape is canonical per
// https://code.claude.com/docs/en/hooks (`{"prompt": "..."}`); any
// deviation is a verifier violation, not a soft warning.
func extractPrompt(payload json.RawMessage) (string, error) {
	if len(payload) == 0 {
		return "", errors.New("empty payload")
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	return body.Prompt, nil
}

// skillPromptMatches verifies the third contract rule: the prompt
// starts with the expected agent/skill prefix per PAT-25.
//
//   - With agent: `/<Agent> --autonomous -- <Skill> --autonomous` (then args)
//   - Without:    `/<Skill> --autonomous --no-commit` (then args)
func skillPromptMatches(prompt, skill, agent string) bool {
	if agent != "" {
		want := "/" + agent + " --autonomous -- " + skill + " --autonomous"
		return strings.HasPrefix(prompt, want)
	}
	want := "/" + skill + " --autonomous --no-commit"
	return strings.HasPrefix(prompt, want)
}

// elide shortens a prompt for inclusion in a violation reason without
// flooding logs with the full payload.
func elide(s string) string {
	const maxLen = 80
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}
