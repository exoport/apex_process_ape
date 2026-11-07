package apexv1

import (
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
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(QueueWorkflow), &doc))
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
