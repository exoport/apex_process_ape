package apexv1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func settingsProject(t *testing.T, settings string) *Project {
	t.Helper()
	root := t.TempDir()
	if settings != "" {
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(settings), 0o644))
	}
	p, err := Open(root)
	require.NoError(t, err)
	return p
}

func codesOf(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range ClassifyFindings(fs) {
		out = append(out, f.Code)
	}
	return out
}

// Backlog 31: project check reports ape's hooks missing from, or altered
// in, .claude/settings.json; `ape project init` puts them back, and a
// second init changes nothing.
func TestCheckHooks_MissingAlteredAndRestored(t *testing.T) {
	p := settingsProject(t, "")
	fs := p.CheckHooks()
	findingMessages(fs)
	require.Equal(t, []string{"hooks.missing", "hooks.missing", "hooks.missing", "hooks.missing"}, codesOf(fs))
	require.Contains(t, fs[1].Message, "hook `ape hook stop` is not registered for Stop")
	require.Contains(t, ClassifyFindings(fs)[0].Fix, "ape project init")

	_, err := MergeHooks(p.SettingsJSON())
	require.NoError(t, err)
	require.Empty(t, p.CheckHooks())

	// A person's edits: the stop hook made async with a short timeout, the
	// edit guard's matcher narrowed, the session-start hook removed. Their
	// own hook and keys stay.
	altered := `{"model": "sonnet", "hooks": {
	  "Stop": [{"hooks": [{"type": "command", "command": "ape hook stop", "timeout": 60, "async": true},
	                      {"type": "command", "command": "./mine.sh"}]}],
	  "SubagentStop": [{"hooks": [{"type": "command", "command": "ape hook stop", "timeout": 1800}]}],
	  "PreToolUse": [{"matcher": "Edit", "hooks": [{"type": "command", "command": "ape hook pre-tool"}]}]
	}}`
	require.NoError(t, os.WriteFile(p.SettingsJSON(), []byte(altered), 0o644))
	fs = p.CheckHooks()
	findingMessages(fs)
	require.Equal(t, []string{"hooks.missing", "hooks.altered", "hooks.altered"}, codesOf(fs))
	require.Contains(t, fs[1].Message, "async")
	require.Contains(t, fs[2].Message, `matcher "Edit"`)

	added, err := MergeHooks(p.SettingsJSON())
	require.NoError(t, err)
	require.Equal(t, []string{
		"SessionStart: ape hook session-start", "Stop: ape hook stop (restored)", "PreToolUse: ape hook pre-tool (restored)",
		"UserPromptSubmit: ape hook user-prompt",
	}, added)
	require.Empty(t, p.CheckHooks())
	data, err := os.ReadFile(p.SettingsJSON())
	require.NoError(t, err)
	var root map[string]any
	require.NoError(t, json.Unmarshal(data, &root))
	require.Equal(t, "sonnet", root["model"])
	require.Contains(t, string(data), "./mine.sh", "the person's own hook is kept")
	require.NotContains(t, string(data), `"async": true`)

	added, err = MergeHooks(p.SettingsJSON())
	require.NoError(t, err)
	require.Empty(t, added, "idempotent")

	require.NoError(t, os.WriteFile(p.SettingsJSON(), []byte("{nope"), 0o644))
	fs = p.CheckHooks()
	findingMessages(fs)
	require.Equal(t, []string{"settings.invalid-json"}, codesOf(fs))
}

func TestInlineHooks_CarryTheFlag(t *testing.T) {
	for i, h := range InlineHooks() {
		require.Equal(t, APEHooks()[i].Command+" --inline", h.Command)
	}
}

// Item 10 widened the pre-tool matcher to Bash: a project initialised
// before it reports the old matcher as altered, and init upgrades it.
func TestCheckHooks_PreItem10MatcherIsUpgraded(t *testing.T) {
	p := settingsProject(t, "")
	_, err := MergeHooks(p.SettingsJSON())
	require.NoError(t, err)
	data, err := os.ReadFile(p.SettingsJSON())
	require.NoError(t, err)
	old := []byte(`"matcher": "Edit|Write|MultiEdit|NotebookEdit"`)
	require.NoError(t, os.WriteFile(p.SettingsJSON(), bytesReplace(data, []byte(`"matcher": "`+PreToolMatcher+`"`), old), 0o644))
	fs := p.CheckHooks()
	findingMessages(fs)
	require.Equal(t, []string{"hooks.altered"}, codesOf(fs))
	added, err := MergeHooks(p.SettingsJSON())
	require.NoError(t, err)
	require.Equal(t, []string{"PreToolUse: ape hook pre-tool (restored)"}, added)
	require.Empty(t, p.CheckHooks())
}

func bytesReplace(s, old, repl []byte) []byte {
	return []byte(strings.Replace(string(s), string(old), string(repl), 1))
}
