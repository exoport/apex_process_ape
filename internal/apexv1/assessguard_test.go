package apexv1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// R1/R2 (step 4): the assessor's guard binds every path a tool names to
// the batch folder, resolved in full — a link inside it that points out,
// a relative path that climbs, Grep's path and glob, Glob's path and
// pattern — and allows only notice.json to be written. The controls: the
// same tools inside the folder are allowed.
func TestAssessGuard_PathsResolvedAndBound(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "batch")
	require.NoError(t, os.MkdirAll(filepath.Join(folder, "src"), 0o700))
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(folder, "src", "out")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "notice.json"), filepath.Join(folder, "alias.json")))
	call := func(tool string, in map[string]string) PreToolDecision {
		raw, err := json.Marshal(in)
		require.NoError(t, err)
		return AssessGuard(HookPayload{ToolName: tool, Cwd: folder, ToolInput: raw}, folder)
	}
	for name, tc := range map[string]struct {
		tool string
		in   map[string]string
		deny bool
	}{
		"read inside":            {"Read", map[string]string{"file_path": "src/a.go"}, false},
		"read climbing":          {"Read", map[string]string{"file_path": "src/../../x"}, true},
		"read through a link":    {"Read", map[string]string{"file_path": "src/out/secret"}, true},
		"read absolute":          {"Read", map[string]string{"file_path": "/etc/passwd"}, true},
		"grep default path":      {"Grep", map[string]string{"pattern": "x"}, false},
		"grep path outside":      {"Grep", map[string]string{"pattern": "x", "path": outside}, true},
		"grep path via link":     {"Grep", map[string]string{"pattern": "x", "path": "src/out"}, true},
		"grep glob climbing":     {"Grep", map[string]string{"pattern": "x", "glob": "../*.creds"}, true},
		"grep glob local":        {"Grep", map[string]string{"pattern": "x", "glob": "**/*.go"}, false},
		"glob pattern local":     {"Glob", map[string]string{"pattern": "src/**/*.go"}, false},
		"glob pattern climbing":  {"Glob", map[string]string{"pattern": "src/../../**"}, true},
		"glob pattern absolute":  {"Glob", map[string]string{"pattern": "/home/**"}, true},
		"glob path outside":      {"Glob", map[string]string{"pattern": "*", "path": outside}, true},
		"write notice.json":      {"Write", map[string]string{"file_path": "notice.json"}, false},
		"write notice.json abs":  {"Write", map[string]string{"file_path": filepath.Join(folder, "notice.json")}, false},
		"write another file":     {"Write", map[string]string{"file_path": "input.md"}, true},
		"write through an alias": {"Write", map[string]string{"file_path": "alias.json"}, true},
		"write via link dir":     {"Write", map[string]string{"file_path": "src/out/../../notice.json"}, true},
		"edit":                   {"Edit", map[string]string{"file_path": "notice.json"}, true},
		"bash":                   {"Bash", map[string]string{"command": "true"}, true},
		"mcp":                    {"mcp__ape__await_message", map[string]string{}, true},
		"tool search":            {"ToolSearch", map[string]string{"query": "x"}, true},
	} {
		require.Equal(t, tc.deny, call(tc.tool, tc.in).Deny, name)
	}
	require.NotEmpty(t, AssessDenials(folder), "denials are recorded beside the folder")
	require.NoFileExists(t, filepath.Join(folder, "notice.json.denied.jsonl"))
}
