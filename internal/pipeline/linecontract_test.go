package pipeline

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/bridge/orchestrator"
)

// Whatever line the runner types for a step, the step's own prompt
// contract accepts. The two are built in different packages from the same
// Step, and D4 (no implicit --no-commit on `ape task`'s no-agent line)
// once changed the first without the second: every no-agent `ape task`
// then failed its contract in 3.6 s.
func TestTypedLineSatisfiesItsPromptContract(t *testing.T) {
	longArgs := "--x " + strings.Repeat("y", 2000)
	for _, agent := range []string{"", "apex-agent-pm"} {
		for _, explicit := range []bool{false, true} {
			for _, args := range []string{"", "--no-commit --doc prd", longArgs} {
				step := Step{Skill: "apex-plan", Agent: agent, Args: args, PromptFlag: "--prompt", ExplicitNoCommit: explicit}
				line, _, err := stepLine(agent, step, "build it", filepath.Join(t.TempDir(), "args.txt"))
				if err != nil {
					t.Fatal(err)
				}
				v := orchestrator.NewContractVerifier()
				var fired []orchestrator.ContractViolation
				v.OnViolation = func(c orchestrator.ContractViolation) { fired = append(fired, c) }
				v.BeginStep(orchestrator.StepContract{Skill: step.Skill, Agent: agent, ExplicitNoCommit: step.ExplicitNoCommit})
				payload, err := json.Marshal(map[string]string{"prompt": line})
				if err != nil {
					t.Fatal(err)
				}
				v.Consume(payload)
				if len(fired) != 0 {
					t.Errorf("agent=%q explicit=%v args=%.20q: typed %.60q, contract says %+v", agent, explicit, args, line, fired)
				}
			}
		}
	}
}
