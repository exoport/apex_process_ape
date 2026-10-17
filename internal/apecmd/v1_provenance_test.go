package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

func provenanceCodes(t *testing.T, root string) map[string]apexv1.Diagnostic {
	t.Helper()
	stdout, _, _ := v1Exec(t, newProjectCmd, "check", "--json", "--cwd", root)
	var out struct {
		Diagnostics []apexv1.Diagnostic `json:"diagnostics"`
	}
	if stdout != "" {
		require.NoError(t, json.Unmarshal([]byte(stdout), &out), stdout)
	}
	codes := map[string]apexv1.Diagnostic{}
	for _, d := range out.Diagnostics {
		if d.Code == "spec.edited" || d.Code == "state.forged" {
			codes[d.Code] = d
		}
	}
	return codes
}

func writeAndCommit(t *testing.T, root, rel, content, msg string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", msg)
}

const provenanceSpec = "# Greeting\n\nPurpose: greets.\n\n### Requirement: Greet\nThe system SHALL greet.\n\n#### Scenario: greet\n- check: true\n"

// H14: a spec edited by a commit that is not an archive squash is
// spec.edited (action person), and start refuses on it; a git revert puts
// the history right.
func TestProvenance_SpecEdited(t *testing.T) {
	root := v1Repo(t)
	require.Empty(t, provenanceCodes(t, root))

	writeAndCommit(t, root, "development/specs/greeting/spec.md", provenanceSpec, "docs: tweak the greeting spec")
	d, ok := provenanceCodes(t, root)["spec.edited"]
	require.True(t, ok)
	require.Equal(t, apexv1.ActionPerson, d.Action)
	require.Equal(t, "development/specs/greeting/spec.md", d.File)
	require.Contains(t, d.Message, "docs: tweak the greeting spec")
	require.Contains(t, d.Fix, "git revert")

	_, _, err := v1Exec(t, newChangeCmd, "new", "add-wave", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err)
	stdout, _, err := v1Exec(t, newChangeCmd, "start", "add-wave", "--json", "--cwd", root)
	require.Error(t, err)
	require.Contains(t, stdout, `"change.provenance"`)

	v1Git(t, root, "revert", "--no-edit", "HEAD~1")
	require.Empty(t, provenanceCodes(t, root), "a revert and what it reverts are not judged")
}

// An archive squash is known by its Change: trailer naming an archived
// change, whatever its subject; a trailer naming no archived change is not
// one.
func TestProvenance_ArchiveSquashByTrailer(t *testing.T) {
	root := v1Repo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "development", "archive", "2026-10-16-add-wave"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "development", "archive", "2026-10-16-add-wave", "change.md"), []byte("x\n"), 0o644))
	writeAndCommit(t, root, "development/specs/greeting/spec.md", provenanceSpec, "feat(greeting): wave\n\nChange: add-wave\n")
	require.Empty(t, provenanceCodes(t, root))

	writeAndCommit(t, root, "development/specs/greeting/spec.md", provenanceSpec+"\n", "feat: more\n\nChange: not-archived\n")
	require.Contains(t, provenanceCodes(t, root), "spec.edited")
}

// state.json may change only in ape's commits (apex: …) or a squash.
func TestProvenance_StateForged(t *testing.T) {
	root := v1Repo(t)
	_, _, err := v1Exec(t, newChangeCmd, "new", "add-wave", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err)
	require.Empty(t, provenanceCodes(t, root), "change new's own commit is ape's")

	state := filepath.Join(root, "development", "changes", "add-wave", "state.json")
	data, err := os.ReadFile(state)
	require.NoError(t, err)
	var st map[string]any
	require.NoError(t, json.Unmarshal(data, &st))
	st["status"] = "reviewed"
	forged, err := json.Marshal(st)
	require.NoError(t, err)
	writeAndCommit(t, root, "development/changes/add-wave/state.json", string(forged)+"\n", "wip")
	d, ok := provenanceCodes(t, root)["state.forged"]
	require.True(t, ok)
	require.Equal(t, apexv1.ActionPerson, d.Action)
	require.Equal(t, "development/changes/add-wave/state.json", d.File)
}

// Nothing before the v1 epoch is judged.
func TestProvenance_BeforeTheEpochIsNotJudged(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "t"}, {"GIT_AUTHOR_EMAIL", "t@t"}, {"GIT_COMMITTER_NAME", "t"}, {"GIT_COMMITTER_EMAIL", "t@t"}} {
		t.Setenv(kv[0], kv[1])
	}
	v1Git(t, root, "init", "-q")
	writeAndCommit(t, root, "development/specs/greeting/spec.md", provenanceSpec, "an old spec")
	_, _, err := v1Exec(t, newProjectCmd, "init", "--cwd", root)
	require.NoError(t, err)
	require.Empty(t, provenanceCodes(t, root))
}
