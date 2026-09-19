package apecmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// changeProject is a project `ape change` would accept: a git
// repository, an ignored ape subtree, a declared evidence_folder, and a
// commit so HEAD exists.
func changeProject(t *testing.T, ignoreLine string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := projectFor(t, allExtensionsConfig+"evidence_folder: development/governance/evidence\n")
	gitInit(t, root)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"),
		[]byte(ignoreLine+"\n"), 0o644))
	gitCommitAll(t, root, "base")
	return root
}

// The ignore rule a project actually writes is the one doctor's own fix
// command hands it — `_output/ape/`, a directory-only pattern. Asked
// about the bare path, git answers "not ignored" until the directory
// exists, so a project that took that advice and whose FIRST ape run is
// `ape change` would be refused for having done exactly the right thing.
//
// A contents rule (`_output/ape/*`) is worse: git ignores what the
// folder holds and not the folder, so the bare path answers "not
// ignored" for ever. Both are what the trailing-slash query fixes.
func TestChangePreflight_AcceptsEveryIgnoreRuleShape(t *testing.T) {
	for _, rule := range []string{"_output/ape/", "/_output/ape/", "_output/ape", "_output/", "_output/ape/*", "_output/ape/**"} {
		t.Run(rule, func(t *testing.T) {
			root := changeProject(t, rule)
			cfg := tryResolveProjectConfig(root)
			require.NotNil(t, cfg)

			// Resolving the config ISSUES a timestamp, which persists
			// {output_folder}/ape/timestamp.state and so creates the very
			// directory this is about. Removed again here, because the
			// case under test is the project whose first ape run is this
			// one — where the bare-path query answers "not ignored".
			require.NoError(t, os.RemoveAll(filepath.Join(root, "_output")))
			require.NoDirExists(t, filepath.Join(root, "_output", "ape"))
			require.NoError(t, changePreflight(context.Background(), cfg), "rule %q should satisfy the gate", rule)
		})
	}
}

func TestChangePreflight_RefusesTheStatesItCannotCommitFrom(t *testing.T) {
	t.Run("the ape subtree is not ignored", func(t *testing.T) {
		root := changeProject(t, "something-else")
		err := changePreflight(context.Background(), tryResolveProjectConfig(root))
		require.ErrorContains(t, err, "not ignored by git")
	})

	t.Run("a dirty tree", func(t *testing.T) {
		root := changeProject(t, "_output/ape/")
		require.NoError(t, os.WriteFile(filepath.Join(root, "dirt.txt"), []byte("x\n"), 0o644))
		err := changePreflight(context.Background(), tryResolveProjectConfig(root))
		require.ErrorContains(t, err, "not clean")
		require.ErrorContains(t, err, "dirt.txt", "the report names what is in the way")
	})

	t.Run("a detached HEAD", func(t *testing.T) {
		root := changeProject(t, "_output/ape/")
		cmd := exec.CommandContext(context.Background(), "git", "checkout", "--detach", "HEAD")
		cmd.Dir = root
		require.NoError(t, cmd.Run())
		err := changePreflight(context.Background(), tryResolveProjectConfig(root))
		require.ErrorContains(t, err, "detached")
	})

	t.Run("no evidence_folder", func(t *testing.T) {
		root := projectFor(t, allExtensionsConfig)
		gitInit(t, root)
		require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("_output/ape/\n"), 0o644))
		gitCommitAll(t, root, "base")
		err := changePreflight(context.Background(), tryResolveProjectConfig(root))
		require.ErrorContains(t, err, "evidence_folder is not set")
	})

	t.Run("not a git repository", func(t *testing.T) {
		root := projectFor(t, allExtensionsConfig+"evidence_folder: development/governance/evidence\n")
		err := changePreflight(context.Background(), tryResolveProjectConfig(root))
		require.ErrorContains(t, err, "not a git repository")
	})
}

func TestResolveChangeRequest(t *testing.T) {
	t.Run("the positional form is a human at a shell", func(t *testing.T) {
		got, err := resolveChangeRequest("the reference is stale", "", strings.NewReader(""), "")
		require.NoError(t, err)
		require.Equal(t, "the reference is stale", got)
	})

	t.Run("stdin is spelled -", func(t *testing.T) {
		got, err := resolveChangeRequest("", "-", strings.NewReader("from stdin\n"), "")
		require.NoError(t, err)
		require.Equal(t, "from stdin", got, "exactly one trailing newline is stripped")
	})

	t.Run("a file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "req.txt")
		require.NoError(t, os.WriteFile(path, []byte("from a file\n"), 0o644))
		got, err := resolveChangeRequest("", path, strings.NewReader(""), "")
		require.NoError(t, err)
		require.Equal(t, "from a file", got)
	})

	t.Run("--fixes alone has no request", func(t *testing.T) {
		got, err := resolveChangeRequest("", "", strings.NewReader(""), "20260919-a1b2c3")
		require.NoError(t, err)
		require.Empty(t, got, "no request means no Request: trailer")
	})

	t.Run("two sources are refused", func(t *testing.T) {
		_, err := resolveChangeRequest("typed", "/tmp/x", strings.NewReader(""), "")
		require.ErrorContains(t, err, "mutually exclusive")
	})

	t.Run("nothing at all is refused", func(t *testing.T) {
		_, err := resolveChangeRequest("", "", strings.NewReader(""), "")
		require.ErrorContains(t, err, "nothing to do")
	})
}

// The request is typed into the REPL as keystrokes. Each of these is
// ordinary text that would not survive the typing, so each is refused at
// the door rather than diagnosed from a stuck pane an hour later.
func TestValidateTypedLine_RefusesWhatCannotBeTyped(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"a line break mid-text", "first\nsecond\n", "line break"},
		{"a second trailing newline", "text\n\n", "line break"},
		{"a tab", "a\tb", "control character"},
		{"an escape", "a\x1bb", "control character"},
		{"a trailing backslash", `end with \`, "backslash"},
		{"empty", "\n", "is empty"},
		{"whitespace only", "   \n", "is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateTypedLine([]byte(tc.in))
			require.ErrorContains(t, err, tc.want)
		})
	}

	t.Run("a CRLF file is not a malformed request", func(t *testing.T) {
		got, err := validateTypedLine([]byte("written on windows\r\n"))
		require.NoError(t, err)
		require.Equal(t, "written on windows", got)
	})

	t.Run("an @path at the end survives", func(t *testing.T) {
		got, err := validateTypedLine([]byte("read @docs/reference/cli.md\n"))
		require.NoError(t, err)
		require.Equal(t, "read @docs/reference/cli.md", got)
	})
}

// A contract written outside the change directory is an untracked file
// in the working tree that no goal claims, and the reconciliation would
// then refuse the whole run over ape's own artifact.
func TestResolveContractOut(t *testing.T) {
	dir := t.TempDir()
	changeDir := filepath.Join(dir, "_output", "ape", "changes", "20260919-120000-abc1234")
	require.NoError(t, os.MkdirAll(changeDir, 0o755))

	got, err := resolveContractOut(changeDir, "")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(changeDir, "contract.yaml"), got)

	got, err = resolveContractOut(changeDir, filepath.Join(changeDir, "pinned.yaml"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(changeDir, "pinned.yaml"), got)

	_, err = resolveContractOut(changeDir, filepath.Join(dir, "elsewhere.yaml"))
	require.ErrorContains(t, err, "must sit inside the change directory")
}

// The monotonic stamp is second-granularity, so two changes in the same
// second would otherwise write into one directory and read each other's
// contract.
func TestComputeChangeID_DistinctWithinOneSecond(t *testing.T) {
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	first := computeChangeID(at, "one request", "/p")
	second := computeChangeID(at.Add(time.Millisecond), "another request", "/p")

	require.True(t, strings.HasPrefix(first, "20260919-120000-"))
	require.NotEqual(t, first, second)
}
