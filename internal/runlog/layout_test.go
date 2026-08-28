package runlog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeConfig seeds _apex/config.yaml with the given output_folder. An
// empty value writes the key with no value, which is how a project that
// blanked it looks on disk.
func writeConfig(t *testing.T, root, outputFolder string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	body := "config_schema_version: \"1\"\nproject_name: demo\nextensions: []\n" +
		"output_folder: " + outputFolder + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.yaml"), []byte(body), 0o600))
}

func TestOutputRoot_HonoursOutputFolder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeConfig(t, root, "build/artifacts")

	require.Equal(t, filepath.Join(root, "build", "artifacts"), OutputRoot(root))
	require.Equal(t, filepath.Join(root, "build", "artifacts", "ape"), ApeRoot(root))
	require.Equal(t, filepath.Join(root, "build", "artifacts", "ape", "pipelines", "design", "r1"),
		PipelineRunDir(root, "design", "r1"))
}

// Run paths are needed where a project config is not guaranteed — `ape
// chat` in a bare directory, a config with a syntax error. Each of these
// must resolve to the framework's default rather than fail.
func TestOutputRoot_FallsBackToTheFrameworkDefault(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{"no project config at all", func(*testing.T, string) {}},
		{"config present but unparseable", func(t *testing.T, root string) {
			t.Helper()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
			require.NoError(t, os.WriteFile(
				filepath.Join(root, "_apex", "config.yaml"), []byte("output_folder: [unclosed\n"), 0o600,
			))
		}},
		{"output_folder blanked", func(t *testing.T, root string) { t.Helper(); writeConfig(t, root, "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			tc.setup(t, root)
			require.Equal(t, filepath.Join(root, DefaultOutputDirName), OutputRoot(root))
			require.Equal(t, filepath.Join(root, DefaultOutputDirName, ApeDirName), ApeRoot(root))
		})
	}
}

func TestRunRoots_AllUnderTheResolvedApeRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeConfig(t, root, "out")
	apeRoot := filepath.Join(root, "out", "ape")

	roots := RunRoots(root)
	require.Len(t, roots, 4)
	kinds := map[string]bool{}
	for _, r := range roots {
		require.Equal(t, apeRoot, filepath.Dir(r.Path), "every run root sits directly under %s", apeRoot)
		kinds[r.Kind] = true
	}
	require.Equal(t, map[string]bool{
		KindPipeline: true, KindTask: true, KindPrompt: true, KindChat: true,
	}, kinds)
}

// On a default project the old and new homes for prompts and chats are the
// same path, so only pipelines and tasks have anywhere to go.
func TestLegacyRunRoots_DefaultProjectHasTwoRelocations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeConfig(t, root, "_output")

	rels := LegacyRunRoots(root)
	require.Len(t, rels, 2)
	require.Equal(t, []string{KindPipeline, KindTask}, []string{rels[0].Kind, rels[1].Kind})
	for _, r := range rels {
		require.NotEqual(t, filepath.Clean(r.From), filepath.Clean(r.To))
	}
}

// The asymmetry that makes this more than a rename: ape hardcoded _output
// BEFORE this change, so a project that renamed output_folder has its old
// artifacts under a literal _output — including prompts and chats, which
// on a default project never needed to move at all.
func TestLegacyRunRoots_RenamedOutputFolderMovesAllFour(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeConfig(t, root, "build/out")
	legacy := filepath.Join(root, "_output")
	newRoot := filepath.Join(root, "build", "out", "ape")

	rels := LegacyRunRoots(root)
	require.Len(t, rels, 4)

	byKind := map[string]Relocation{}
	for _, r := range rels {
		byKind[r.Kind] = r
	}
	require.Equal(t, filepath.Join(legacy, "pipelines"), byKind[KindPipeline].From)
	require.Equal(t, filepath.Join(newRoot, "pipelines"), byKind[KindPipeline].To)
	require.True(t, byKind[KindPipeline].Grouped)

	// prompts/chats were already under _output/ape — a literal path, not
	// the configured one — so they move too.
	require.Equal(t, filepath.Join(legacy, "ape", "prompts"), byKind[KindPrompt].From)
	require.Equal(t, filepath.Join(newRoot, "prompts"), byKind[KindPrompt].To)
	require.False(t, byKind[KindPrompt].Grouped)
	require.Equal(t, filepath.Join(legacy, "ape", "chats"), byKind[KindChat].From)
	require.Equal(t, filepath.Join(newRoot, "chats"), byKind[KindChat].To)
}

// End-to-end for the renamed case: ungrouped trees relocate correctly,
// which the grouped-only migration would have silently skipped.
// Loose files at the output-folder root must survive, and must stop the
// husk prune.
//
// This is the case that bites only on a RENAMED output_folder, which is
// also the only case where ape prunes an output directory at all. A project
// that renamed output_folder after running older ape has its legacy `_output`
// holding both ape's run trees AND whatever loose files the skills left there
// — defer-*, retro-epic-*, data-architecture-ripple-* and friends, which have
// no directory of their own. Once the run trees move out, `_output` still
// holds those files and must NOT be removed.
//
// pruneIfEmpty counts files for exactly this reason. hasRunDir, a few lines
// away, counts only subdirectories because it answers a different question.
// This test is what stops the two being "unified".
func TestMigrate_LooseFilesAtOutputRootBlockThePrune(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeConfig(t, root, "build/out")

	runDir := filepath.Join(root, "_output", "pipelines", "design", "r1")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "manifest.yaml"), []byte("x\n"), 0o600))

	loose := []string{
		"defer-something.md", "defer-epic-04.md", "defer-code-review-12.md",
		"retro-epic-04.md", "retro-improvement-brief-epic-04.md",
		"data-architecture-ripple-07.md",
	}
	for _, name := range loose {
		require.NoError(t, os.WriteFile(
			filepath.Join(root, "_output", name), []byte("framework-owned\n"), 0o600,
		))
	}

	res, err := Migrate(root)
	require.NoError(t, err)
	require.Equal(t, []string{"pipeline/design/r1"}, res.Moved)

	// The run moved...
	require.FileExists(t, filepath.Join(root, "build", "out", "ape", "pipelines", "design", "r1", "manifest.yaml"))
	require.NoDirExists(t, filepath.Join(root, "_output", "pipelines"))

	// ...and every loose file is still there, in a directory that survived
	// precisely because they are in it.
	require.DirExists(t, filepath.Join(root, "_output"),
		"_output holds the framework's loose files and must not be pruned")
	for _, name := range loose {
		body, rerr := os.ReadFile(filepath.Join(root, "_output", name))
		require.NoError(t, rerr, "%s must survive the migration", name)
		require.Equal(t, "framework-owned\n", string(body))
	}
}

func TestMigrate_RenamedOutputFolderMovesPromptsAndChats(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeConfig(t, root, "build/out")

	mk := func(rel string, body string) {
		dir := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "record.yaml"), []byte(body), 0o600))
	}
	mk("_output/pipelines/design/r1", "pipeline\n")
	mk("_output/ape/prompts/p1", "prompt\n")
	mk("_output/ape/chats/c1", "chat\n")

	res, err := Migrate(root)
	require.NoError(t, err)
	require.ElementsMatch(t,
		// Forward slashes on every platform: these are report labels, not
		// paths — see the path.Join in migrate.go.
		[]string{"pipeline/design/r1", "prompt/p1", "chat/c1"},
		res.Moved)
	require.Empty(t, res.Conflicts)

	newRoot := filepath.Join(root, "build", "out", "ape")
	require.FileExists(t, filepath.Join(newRoot, "pipelines", "design", "r1", "record.yaml"))
	require.FileExists(t, filepath.Join(newRoot, "prompts", "p1", "record.yaml"))
	require.FileExists(t, filepath.Join(newRoot, "chats", "c1", "record.yaml"))
	require.NoDirExists(t, filepath.Join(root, "_output", "ape", "prompts"))
	require.False(t, Pending(root))
	// The emptied legacy husks are gone too — but never the live output
	// folder, which is the framework's.
	require.NoDirExists(t, filepath.Join(root, "_output"))
	require.DirExists(t, filepath.Join(root, "build", "out"))
}
