//nolint:testpackage // exercising the unexported pipelineModel state machine
package tui

import (
	"testing"

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/stretchr/testify/require"
)

// A stage skipped for its extensions leaves the pending state for a
// terminal skipped one, steps included, and does not count as passed.
func TestPipelineModel_StageSkipped(t *testing.T) {
	m := NewPipelineModel(fakeSpec(t), nil, "")
	next, _ := m.Update(stageSkippedMsg{stage: "beta", skip: pipeline.Skip{Reason: "extension", Missing: []string{"ext-patterns"}}})
	m, ok := next.(pipelineModel)
	require.True(t, ok)
	beta := m.stages[m.stageIdx["beta"]]
	require.Equal(t, stateSkipped, beta.state)
	require.Equal(t, stateSkipped, beta.steps[0].state)
	require.Equal(t, "extension: needs ext-patterns", beta.skipNote)
	passed, failed, total := m.tallyStages()
	require.Equal(t, [3]int{0, 0, 2}, [3]int{passed, failed, total})

	next, _ = m.Update(stepSkippedMsg{stage: "alpha", idx: 0})
	m, ok = next.(pipelineModel)
	require.True(t, ok)
	require.Equal(t, stateSkipped, m.stages[m.stageIdx["alpha"]].steps[0].state)
}
