package apecmd

import (
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// `ape task --help` names the depth-1 allow-list from allowlist.go (eval rc.3
// probe: the help omitted apex-accept, which the refusal message listed),
// and the rules that extend it.
func TestTaskHelp_AllowListIsGenerated(t *testing.T) {
	flat := strings.Join(strings.Fields(newTaskCmd().Long), " ")
	require.Contains(t, flat, "allow-list ("+strings.Join(apexv1.CoreChildSkills, ", ")+", the human_review: delegate skill,")
	require.Contains(t, flat, "every skill the tools: slots name")
	require.Contains(t, apexv1.CoreChildSkills, "apex-accept")
	require.Contains(t, flat, "apex-change, apex-vet, apex-apply", "Q4a: the help names apex-vet, in loop order")
	require.NotContains(t, newTaskCmd().Long, "{{")
	for line := range strings.SplitSeq(depthOneHelp(), "\n") {
		require.LessOrEqual(t, len(line), 76, line)
	}
}
