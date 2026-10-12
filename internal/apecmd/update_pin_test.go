package apecmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/exoport/apex_process_ape/internal/updatecache"
	"github.com/stretchr/testify/require"
)

// fakeLatestRelease points the GitHub client at a server whose latest
// release is tag.
func fakeLatestRelease(t *testing.T, tag string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[]}`, tag)
	}))
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
}

// fakeBingo puts a `bingo` on PATH that does what `bingo get <mod>@<tag>`
// does to a project: rewrites .bingo/ape.mod and builds $GOBIN/ape-<tag>
// (here a script that exits 0). It records its arguments in calls.
func fakeBingo(t *testing.T) (calls string) {
	t.Helper()
	if runtime.GOOS == goosWindows {
		t.Skip("shell-script shims")
	}
	bin := t.TempDir()
	gobin := t.TempDir()
	calls = filepath.Join(bin, "calls")
	script := `#!/bin/sh
echo "$@" >> "` + calls + `"
case "$(pwd)" in *"/.git"*|"") exit 9;; esac
for a in "$@"; do last="$a"; done
tag="${last##*@}"
mkdir -p .bingo
printf 'module _\n\ngo 1.27.0\n\nrequire github.com/exoport/apex_process_ape %s // cmd/ape\n' "$tag" > .bingo/ape.mod
printf '#!/bin/sh\nexit 0\n' > "$GOBIN/ape-$tag"
chmod +x "$GOBIN/ape-$tag"
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "bingo"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOBIN", gobin)
	return calls
}

func seedApePin(t *testing.T, root, version string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".bingo"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".bingo", "ape.mod"), []byte(
		"module _\n\ngo 1.27.0\n\nrequire github.com/exoport/apex_process_ape "+version+" // cmd/ape\n"), 0o644))
}

func pinOf(t *testing.T, root string) string {
	t.Helper()
	p, err := findApePin(root)
	require.NoError(t, err)
	require.NotNil(t, p)
	return p.version
}

// TestUpdatePin: plain `ape update` never touches .bingo/ — a pin is a
// tracked, team-wide change — while `--pin` moves it in a commit of its own.
func TestUpdatePin(t *testing.T) {
	fakeLatestRelease(t, "v0.99.0")
	calls := fakeBingo(t)
	withApeVersion(t, "0.99.0") // the launcher is current; only the pin is behind

	t.Run("plain update leaves the pin alone", func(t *testing.T) {
		root := committedProject(t)
		seedApePin(t, root, "v0.8.0")
		gitCommitAll(t, root, "pin ape")
		head := gitRun(t, root, "rev-parse", "HEAD")
		t.Chdir(root)

		require.NoError(t, runUpdate(t.Context(), Version, "human", false))
		require.Equal(t, "v0.8.0", pinOf(t, root))
		require.Equal(t, head, gitRun(t, root, "rev-parse", "HEAD"))
		require.NoFileExists(t, calls, "bingo is never run without --pin")
	})
	t.Run("--pin moves the pin in its own commit", func(t *testing.T) {
		root := committedProject(t)
		seedApePin(t, root, "v0.8.0")
		gitCommitAll(t, root, "pin ape")
		t.Chdir(root)

		require.NoError(t, runUpdate(t.Context(), Version, "human", true))
		require.Equal(t, "v0.99.0", pinOf(t, root))
		require.Equal(t, "chore(ape): update ape to v0.99.0", gitRun(t, root, "log", "-1", "--format=%s"))
		require.Equal(t, ".bingo/ape.mod", gitRun(t, root, "show", "--name-only", "--format=", "HEAD"),
			"only the pin is committed; the user's untracked file stays out")
	})
	t.Run("--pin outside a pinned project is a usage error", func(t *testing.T) {
		root := committedProject(t)
		t.Chdir(root)
		err := runUpdate(t.Context(), Version, "human", true)
		require.Equal(t, ExitUsage, exitCodeOf(t, err))
	})
}

// TestFrameworkUpdate_NewerApeIsOptIn: above the floor a newer ape is never
// picked up unasked — no terminal and no flag changes nothing — while
// --update-ape moves the pin in its own chore(ape) commit, before and apart
// from the framework's.
func TestFrameworkUpdate_NewerApeIsOptIn(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("seeds the update cache through XDG_CACHE_HOME")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv(envFrameworkReexec, "")
	withApeVersion(t, "0.8.0")
	fakeLatestRelease(t, "v0.99.0")
	updatecache.Save("0.99.0")
	calls := fakeBingo(t)
	fw := releasedFramework(t, "v1.0.0")
	installed := func(t *testing.T) string {
		t.Helper()
		root := committedProject(t)
		out, err := runFramework(t, "setup", fw, root, "--no-fetch", "--no-bootstrap")
		require.NoError(t, err, out)
		seedApePin(t, root, "v0.8.0")
		gitCommitAll(t, root, "pin ape")
		require.NoFileExists(t, calls, "setup without a terminal or a flag ran no bingo")
		return root
	}

	t.Run("no terminal, no flag: nothing about ape changes", func(t *testing.T) {
		root := installed(t)

		out, err := runFramework(t, "update", fw, root, "--no-fetch")
		require.NoError(t, err, out)
		require.Equal(t, "v0.8.0", pinOf(t, root))
		require.NotContains(t, gitRun(t, root, "log", "--format=%s"), "chore(ape)")
		require.NoFileExists(t, calls)
	})
	t.Run("--update-ape moves the pin in its own commit", func(t *testing.T) {
		root := installed(t)
		before := gitRun(t, root, "rev-parse", "HEAD")

		// The re-run is the fake ape-v0.99.0, which exits 0 having done
		// nothing; the pin's commit is what this asserts.
		_, err := runFramework(t, "update", fw, root, "--no-fetch", "--update-ape")
		require.NoError(t, err)
		require.Equal(t, "v0.99.0", pinOf(t, root))
		require.Equal(t, "chore(ape): update ape to v0.99.0", gitRun(t, root, "log", "-1", "--format=%s"))
		require.Equal(t, before, gitRun(t, root, "rev-parse", "HEAD~1"),
			"one commit, the pin's: the framework install is the re-run's, never mixed into it")
		require.Equal(t, ".bingo/ape.mod", gitRun(t, root, "show", "--name-only", "--format=", "HEAD"))
	})
}
