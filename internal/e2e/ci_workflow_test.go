package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestRepo_CIWorkflowTriggers keeps CI from drifting unseen (F5). Until v1.2
// ci.yml ran only on a push to main and on pull requests, so every candidate
// round needed a throwaway trigger commit, and a runner-image change (26.04,
// Git 2.55) landed with nobody watching. Three triggers fix that, and this
// test holds them:
//   - push on ci/** as well as main: a candidate branch runs as it is;
//   - a daily schedule: image drift and overnight advisories show up on main;
//   - workflow_dispatch: a re-run without a commit.
//
// The workflow is read as a yaml.Node because its `on:` key is a boolean
// under YAML 1.1; matching the key's text does not depend on how a decoder
// resolves it.
func TestRepo_CIWorkflowTriggers(t *testing.T) {
	t.Parallel()
	path := filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	require.Equal(t, yaml.DocumentNode, doc.Kind)
	require.NotEmpty(t, doc.Content)

	on := mappingValue(doc.Content[0], "on")
	require.NotNil(t, on, "%s: no top-level `on:` key", path)
	require.Equal(t, yaml.MappingNode, on.Kind, "%s: `on:` must be a mapping of triggers", path)

	t.Run("push", func(t *testing.T) {
		t.Parallel()
		push := mappingValue(on, "push")
		require.NotNil(t, push, "%s: no `push` trigger", path)
		branchesNode := mappingValue(push, "branches")
		require.NotNil(t, branchesNode, "%s: push has no `branches` filter", path)
		var branches []string
		require.NoError(t, branchesNode.Decode(&branches))
		for _, want := range []string{"main", "ci/**"} {
			require.True(t, slices.Contains(branches, want),
				"%s: push.branches %v lacks %q", path, branches, want)
		}
	})

	t.Run("schedule", func(t *testing.T) {
		t.Parallel()
		schedule := mappingValue(on, "schedule")
		require.NotNil(t, schedule, "%s: no `schedule` trigger", path)
		var entries []map[string]string
		require.NoError(t, schedule.Decode(&entries))
		require.NotEmpty(t, entries, "%s: schedule has no entries", path)
		cronField := `(\*|[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*)(/[0-9]+)?`
		cronRe := regexp.MustCompile(`^` + cronField + `( ` + cronField + `){4}$`)
		for _, e := range entries {
			require.Regexp(t, cronRe, e["cron"], "%s: schedule entry %v is not a five-field cron", path, e)
		}
	})

	t.Run("workflow_dispatch", func(t *testing.T) {
		t.Parallel()
		// Present; its value is usually empty, which is a null node, not nil.
		require.NotNil(t, mappingValue(on, "workflow_dispatch"), "%s: no `workflow_dispatch` trigger", path)
	})

	t.Run("concurrency_per_event", func(t *testing.T) {
		t.Parallel()
		// cancel-in-progress with a group of the ref alone would let the
		// daily schedule cancel a push run on main.
		group := mappingValue(mappingValue(doc.Content[0], "concurrency"), "group")
		require.NotNil(t, group, "%s: no concurrency.group", path)
		require.Contains(t, group.Value, "github.event_name",
			"%s: concurrency.group %q must include the event, or the schedule cancels push runs", path, group.Value)
	})
}

// mappingValue returns the value node under key in a mapping node, matching
// the key's source text, or nil.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
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
