package apexv1

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// V12-12: the queue's workflow runs on self-hosted runners with a project's
// live services, and checks out what a pull request proposes; its
// GITHUB_TOKEN reads the repository and nothing more. GitHub's default
// grants write wherever the repository's setting says so, so the workflow
// says contents: read itself, and no job widens it.
func TestQueueWorkflow_TokenReadsOnly(t *testing.T) {
	openRunnerMode(t)
	wf, err := QueueWorkflowFor("apex-r1")
	require.NoError(t, err)
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(wf), &doc))
	root := doc.Content[0]
	perms := mapValue(root, "permissions")
	require.NotNil(t, perms, "no top-level permissions: the token gets the repository's default")
	var got map[string]string
	require.NoError(t, perms.Decode(&got))
	require.Equal(t, map[string]string{"contents": "read"}, got)
	jobs := mapValue(root, "jobs")
	require.NotNil(t, jobs)
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		require.Nil(t, mapValue(jobs.Content[i+1], "permissions"), "job %s sets its own permissions", jobs.Content[i].Value)
	}
}

// mapValue is the value under key in a mapping node, by the key's text
// (`on:` is a boolean to a YAML 1.1 decoder), or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// D871, V12-15: every job runs on [self-hosted, <label>], the runner's own
// label, parsed from the YAML; a label that is not the runner's own is
// refused, and no slot or bare self-hosted is left.
func TestQueueWorkflow_RunsOnTheRunnersOwnLabel(t *testing.T) {
	openRunnerMode(t)
	wf, err := QueueWorkflowFor("apex-v12-7-mmr7")
	require.NoError(t, err)
	var doc struct {
		Jobs map[string]struct {
			RunsOn []string `yaml:"runs-on"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(wf), &doc))
	require.Len(t, doc.Jobs, 2)
	for name, job := range doc.Jobs {
		require.Equal(t, []string{"self-hosted", "apex-v12-7-mmr7"}, job.RunsOn, name)
	}
	require.NotContains(t, wf, runnerLabelSlot)
	require.NotContains(t, wf, "[self-hosted]")
	for _, bad := range []string{"", "self-hosted", "Linux", "X64", "a,b", "a b"} {
		_, err := QueueWorkflowFor(bad)
		require.Error(t, err, bad)
	}
}

// D876: the project's workflow is queue/runner's; every other mode is
// ci.workflow-mode, a block that makes no mode coord.integration.
func TestProjectQueueWorkflow_Mode(t *testing.T) {
	openRunnerMode(t)
	gh := "  github:\n    repo: shop/notes\n"
	runner := "coordination:\n  integration: queue\n  events: runner\n  runner_label: apex-r1\n" + gh
	p, err := Open(writeConfig(t, runner))
	require.NoError(t, err)
	wf, err := p.QueueWorkflow()
	require.NoError(t, err)
	require.Contains(t, wf, "runs-on: [self-hosted, apex-r1]")
	for cfg, code := range map[string]string{
		"project_name: x\n":                                                                 CodeCIWorkflowMode,
		"coordination:\n  integration: trunk\n":                                             CodeCIWorkflowMode,
		"coordination:\n  integration: queue\n" + gh:                                        CodeCIWorkflowMode,
		"coordination:\n  integration: queue\n  events: watch\n" + gh:                       CodeCIWorkflowMode,
		"coordination:\n  integration: queue\n  events: runner\n" + gh:                      CodeCoordIntegration,
		"coordination:\n  integration: queue\n  events: runner\n  runner_label: x64\n" + gh: CodeCoordIntegration,
	} {
		p, err := Open(writeConfig(t, cfg))
		require.NoError(t, err)
		_, err = p.QueueWorkflow()
		fe, ok := errors.AsType[*FailError](err)
		require.True(t, ok, "%q: %v", cfg, err)
		require.Equal(t, code, fe.Code, cfg)
		_, err = p.WriteQueueWorkflow()
		require.Error(t, err, cfg)
	}
}

// openRunnerMode opens the D884 gate for a test of queue/runner's workflow
// itself, as a release that ships the mode would.
func openRunnerMode(t *testing.T) {
	t.Helper()
	was := RunnerModeReleased
	RunnerModeReleased = true
	t.Cleanup(func() { RunnerModeReleased = was })
}

// D884: 1.5 does not ship queue/runner. events: runner still resolves as
// the mode, but its workflow is refused, ci.runner-not-in-release naming
// queue/watch, before the label is read; every other mode is refused
// ci.workflow-mode as before.
func TestRunnerMode_NotInRelease(t *testing.T) {
	require.False(t, RunnerModeReleased, "this release does not ship queue/runner")
	for _, label := range []string{"apex-r1", "", "self-hosted"} {
		_, err := QueueWorkflowFor(label)
		fe, ok := errors.AsType[*FailError](err)
		require.True(t, ok, "%q: %v", label, err)
		require.Equal(t, CodeCIRunnerNotInRelease, fe.Code, "refused before the label (%q)", label)
		require.Contains(t, fe.Msg, "queue/watch")
		require.Contains(t, fe.Msg, "not available in this release")
	}
	gh := "  github:\n    repo: shop/notes\n"
	runner := "coordination:\n  integration: queue\n  events: runner\n  runner_label: apex-r1\n" + gh
	root := writeConfig(t, runner)
	p, err := Open(root)
	require.NoError(t, err)
	cfg, err := LoadConfig(root)
	require.NoError(t, err)
	mode, err := cfg.Coordination.Mode()
	require.NoError(t, err)
	require.Equal(t, ModeQueueRunner, mode, "events: runner still resolves")
	_, err = p.QueueWorkflow()
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodeCIRunnerNotInRelease, fe.Code)
	changed, err := p.WriteQueueWorkflow()
	require.Error(t, err)
	require.False(t, changed)
	require.NoFileExists(t, filepath.Join(root, filepath.FromSlash(QueueWorkflowRel)), "nothing written")
	// The control: queue/watch is ci.workflow-mode, as before D884.
	p, err = Open(writeConfig(t, "coordination:\n  integration: queue\n  events: watch\n"+gh))
	require.NoError(t, err)
	_, err = p.QueueWorkflow()
	fe, ok = errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodeCIWorkflowMode, fe.Code)
}
