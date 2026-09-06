package framework_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

// notInstalled names every file under a framework's `_apex/` that ape
// deliberately does NOT copy into a project, with the reason.
//
// It is the ONLY hand-maintained list in this test, and it is
// default-DENY: anything the framework ships that is not here must arrive
// in the project, or the test fails. That direction is the whole point.
// The installer's own path list is hand-maintained too, and twice this
// release it was missing an entry the runner read from the project —
// `commit-owners.csv` (every dispatch's commit-ownership assertion
// skipped, on every project) and `migrations/` (the upgrade runner
// unreachable, reporting "this framework ships no migration list" on a
// framework that ships one). Both were correct code, never delivered.
//
// A list of what to install cannot catch a missing entry: absence looks
// like completeness. A list of what to SKIP can, because the tree
// supplies the denominator.
var notInstalled = map[string]string{
	"config.yaml":               "seeded once by `setup`'s bootstrap; `update` must never touch it",
	"config.local.example.yaml": "seeded once by bootstrap; a local override template is the project's",
	"README.md":                 "prose about the framework's own folder, not a runtime input",
	"agent-manifest.csv":        "read by skills from the framework, not from the project",
	"apex-help.csv":             "read by skills from the framework, not from the project",
}

// TestInstallerCoversEveryFrameworkOwnedPath walks the framework's `_apex/`
// and asserts each file arrives in the project.
//
// Derived from the tree, never from a list of what we expect — the
// analogue of the framework's own `verify-ship-payload.py`, which the
// framework session confirmed derives completeness by walking both trees
// rather than by an allowlist. A framework that adds a file ape must
// install fails this test on the next update, which is the signal neither
// install gap ever produced.
func TestInstallerCoversEveryFrameworkOwnedPath(t *testing.T) {
	t.Parallel()
	fw, proj := t.TempDir(), t.TempDir()
	fakeFrameworkFull(t, fw)

	_, err := framework.Setup(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)

	srcApex := filepath.Join(fw, "_apex")
	var missing, skipped []string
	require.NoError(t, filepath.WalkDir(srcApex, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, rErr := filepath.Rel(srcApex, p)
		if rErr != nil {
			return rErr
		}
		rel = filepath.ToSlash(rel)
		if reason, ok := notInstalled[rel]; ok {
			skipped = append(skipped, rel+" ("+reason+")")
			return nil
		}
		if _, sErr := os.Stat(filepath.Join(proj, "_apex", rel)); sErr != nil {
			missing = append(missing, rel)
		}
		return nil
	}))
	sort.Strings(missing)

	require.Empty(t, missing, "the framework ships these under _apex/ and the installer does not "+
		"deliver them. A runner reading one of these FROM THE PROJECT finds nothing and reports "+
		"the absence honestly, which reads exactly like the feature being switched off. Either "+
		"install it, or add it to notInstalled with the reason it is not the project's.\nmissing: %v",
		missing)
	require.NotEmpty(t, skipped, "the deny-list should be exercised, or this test is asserting nothing")
}

// fakeFrameworkFull builds a framework carrying EVERY file the real one
// ships under `_apex/`, including the two whose absence from the installer
// was found the hard way.
func fakeFrameworkFull(t *testing.T, fw string) {
	t.Helper()
	fakeFramework(t, fw, "v0.16.0")
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(fw, "_apex", rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	write("README.md", "# _apex\n")
	write("agent-manifest.csv", "agent,role\n")
	write("apex-help.csv", "phase,artifact\n")
	write("terminal-contracts.csv", "skill,pattern\napex-dev-story,^dev_status:\n")
	write("ape-commands.yaml", "required_commands:\n  - ape version\n")
	write("commit-owners.csv", "skill,commit_kind,message_regex\napex-story-batch-dev,dev,^dev: .+$\n")
	write("apex-operating-rules.md", "# rules\n")
	write("migrations/v0.16.0_seq-01_x.md", migrationEntry)
	write("aboard/recipes/kanban.md", "---\nname: kanban\n---\n\nbody\n")
	commitAll(t, fw, "a framework carrying every _apex/ file")
}
