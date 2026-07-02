package orchestrator

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// TestContractVerifier_HappyPathWithAgent — single UserPromptSubmit
// matching the agent + skill prefix satisfies the contract.
func TestContractVerifier_HappyPathWithAgent(t *testing.T) {
	v := NewContractVerifier()
	var fired []ContractViolation
	v.OnViolation = func(c ContractViolation) { fired = append(fired, c) }

	v.BeginStep(StepContract{
		Stage:   "create-prd",
		StepIdx: 0,
		Skill:   "apex-create-prd",
		Agent:   "apex-agent-pm",
	})
	v.Consume(promptPayload("/apex-agent-pm --autonomous -- apex-create-prd --autonomous"))

	if len(fired) != 0 {
		t.Fatalf("unexpected violations: %+v", fired)
	}
}

// TestContractVerifier_HappyPathNoAgent — single UserPromptSubmit
// matching the no-agent skill prefix satisfies the contract.
func TestContractVerifier_HappyPathNoAgent(t *testing.T) {
	v := NewContractVerifier()
	var fired []ContractViolation
	v.OnViolation = func(c ContractViolation) { fired = append(fired, c) }

	v.BeginStep(StepContract{
		Stage:   "shard-prd",
		StepIdx: 0,
		Skill:   "apex-shard-doc",
	})
	v.Consume(promptPayload("/apex-shard-doc --autonomous --no-commit --doc prd"))

	if len(fired) != 0 {
		t.Fatalf("unexpected violations: %+v", fired)
	}
}

// TestContractVerifier_WrongAgentViolates covers the agent-prefix
// mismatch path — the verifier's only remaining check.
func TestContractVerifier_WrongAgentViolates(t *testing.T) {
	v := NewContractVerifier()
	var fired []ContractViolation
	v.OnViolation = func(c ContractViolation) { fired = append(fired, c) }

	v.BeginStep(StepContract{
		Stage:   "create-prd",
		StepIdx: 0,
		Skill:   "apex-create-prd",
		Agent:   "apex-agent-pm",
	})
	// Wrong agent — apex-agent-dev instead of apex-agent-pm.
	v.Consume(promptPayload("/apex-agent-dev --autonomous -- apex-create-prd --autonomous"))

	if len(fired) != 1 {
		t.Fatalf("expected 1 violation, got %d: %+v", len(fired), fired)
	}
	if !strings.Contains(fired[0].Reason, "expected skill prompt") {
		t.Errorf("reason = %q, want substring 'expected skill prompt'", fired[0].Reason)
	}
}

// TestContractVerifier_WrongSkillViolates covers the skill-name
// mismatch path.
func TestContractVerifier_WrongSkillViolates(t *testing.T) {
	v := NewContractVerifier()
	var fired []ContractViolation
	v.OnViolation = func(c ContractViolation) { fired = append(fired, c) }

	v.BeginStep(StepContract{
		Stage:   "create-prd",
		StepIdx: 0,
		Skill:   "apex-create-prd",
		Agent:   "apex-agent-pm",
	})
	v.Consume(promptPayload("/apex-agent-pm --autonomous -- some-other-skill --autonomous"))

	if len(fired) != 1 {
		t.Fatalf("expected 1 violation, got %d: %+v", len(fired), fired)
	}
}

// TestContractVerifier_ExtraPromptsAfterDoneIgnored — once the contract
// is satisfied, further UserPromptSubmit events don't fire violations.
// Models often issue follow-on user turns internally.
func TestContractVerifier_ExtraPromptsAfterDoneIgnored(t *testing.T) {
	v := NewContractVerifier()
	var fired []ContractViolation
	v.OnViolation = func(c ContractViolation) { fired = append(fired, c) }

	v.BeginStep(StepContract{
		Stage:   "elicit",
		StepIdx: 0,
		Skill:   "apex-create-prd",
		Agent:   "apex-agent-pm",
	})
	v.Consume(promptPayload("/apex-agent-pm --autonomous -- apex-create-prd --autonomous"))
	v.Consume(promptPayload("yes, proceed"))
	v.Consume(promptPayload("another follow-on"))

	if len(fired) != 0 {
		t.Fatalf("unexpected violations after contract satisfied: %+v", fired)
	}
}

// TestContractVerifier_NoActiveStepIgnoresPrompts — prompts arriving
// before BeginStep or after EndStep are silently ignored.
func TestContractVerifier_NoActiveStepIgnoresPrompts(t *testing.T) {
	v := NewContractVerifier()
	var fired []ContractViolation
	v.OnViolation = func(c ContractViolation) { fired = append(fired, c) }

	v.Consume(promptPayload("/random"))
	if len(fired) != 0 {
		t.Errorf("violation fired before BeginStep: %+v", fired)
	}

	v.BeginStep(StepContract{Stage: "s", StepIdx: 0, Skill: "x"})
	v.Consume(promptPayload("/x --autonomous --no-commit"))
	v.EndStep()
	v.Consume(promptPayload("/random"))
	if len(fired) != 0 {
		t.Errorf("violation fired after EndStep: %+v", fired)
	}
}

// TestContractVerifier_MalformedPayloadViolates covers the
// malformed-payload guard.
func TestContractVerifier_MalformedPayloadViolates(t *testing.T) {
	v := NewContractVerifier()
	var fired []ContractViolation
	v.OnViolation = func(c ContractViolation) { fired = append(fired, c) }

	v.BeginStep(StepContract{Stage: "s", StepIdx: 0, Skill: "x"})
	// Unparseable JSON is a genuine hook regression → violation.
	v.Consume(json.RawMessage(`{not valid json`))
	if len(fired) != 1 {
		t.Fatalf("expected 1 violation for unparseable payload, got %d", len(fired))
	}
}

// TestContractVerifier_MissingPromptFieldSkipped: a well-formed
// payload that lacks a `prompt` field (empty prompt) is a non-skill
// event — skipped, not a violation. A legitimate skill UPS always
// carries a slash command, so an empty prompt is dismissal/menu noise.
func TestContractVerifier_MissingPromptFieldSkipped(t *testing.T) {
	v := NewContractVerifier()
	var fired []ContractViolation
	v.OnViolation = func(c ContractViolation) { fired = append(fired, c) }

	v.BeginStep(StepContract{Stage: "s", StepIdx: 0, Skill: "x"})
	v.Consume(json.RawMessage(`{"not-prompt":"foo"}`))
	if len(fired) != 0 {
		t.Fatalf("empty-prompt payload must be skipped, got violation: %+v", fired)
	}
}

// TestContractVerifier_OnViolationFiresOncePerStep covers the
// one-violation-per-step rule.
func TestContractVerifier_OnViolationFiresOncePerStep(t *testing.T) {
	v := NewContractVerifier()
	var (
		mu    sync.Mutex
		count int
	)
	v.OnViolation = func(_ ContractViolation) { mu.Lock(); count++; mu.Unlock() }

	v.BeginStep(StepContract{Stage: "s", StepIdx: 0, Skill: "x"})
	v.Consume(promptPayload("/wrong-first"))
	v.Consume(promptPayload("/wrong-second"))
	v.Consume(promptPayload("/wrong-third"))

	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Errorf("OnViolation fired %d times, want 1", count)
	}
}

// TestContractVerifier_SkipsTrustAcceptKeystroke is the v0.0.29
// regression guard for the trust-accept leak: in a fresh (untrusted)
// dir the folder-trust modal's dismissal keystroke can surface as a
// UserPromptSubmit whose async hook races into the step-contract
// window. Such a non-slash-command event must be SKIPPED (not consumed
// as the skill prompt, not a violation) — the first UPS the verifier
// acts on is the real skill command, never a dismissal keystroke.
func TestContractVerifier_SkipsTrustAcceptKeystroke(t *testing.T) {
	v := NewContractVerifier()
	var fired []ContractViolation
	v.OnViolation = func(c ContractViolation) { fired = append(fired, c) }

	v.BeginStep(StepContract{
		Stage:   "create-prd",
		StepIdx: 0,
		Skill:   "apex-create-prd",
		Agent:   "apex-agent-pm",
	})

	// Dismissal-keystroke leaks that must NOT trip the contract:
	// a bare "1" selection, an empty submit, a plain "y".
	for _, noise := range []string{"1", "", "y"} {
		v.Consume(promptPayload(noise))
		if len(fired) != 0 {
			t.Fatalf("non-slash UPS %q must be skipped, got violation: %+v", noise, fired)
		}
		if v.HasViolated() {
			t.Fatalf("verifier marked done on dismissal keystroke %q", noise)
		}
	}

	// The real skill command still satisfies the contract afterward.
	v.Consume(promptPayload("/apex-agent-pm --autonomous -- apex-create-prd --autonomous"))
	if len(fired) != 0 {
		t.Fatalf("skill prompt after dismissal noise must not violate: %+v", fired)
	}
}

func promptPayload(prompt string) json.RawMessage {
	b, err := json.Marshal(struct {
		Prompt string `json:"prompt"`
	}{Prompt: prompt})
	if err != nil {
		panic(err)
	}
	return b
}
