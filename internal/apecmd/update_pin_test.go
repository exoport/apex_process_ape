package apecmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
printf '#!/bin/sh\necho "$@" >> "$0.args"\nexit 0\n' > "$GOBIN/ape-$tag"
chmod +x "$GOBIN/ape-$tag"
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "bingo"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOBIN", gobin)
	return calls
}

// installPinnedApe puts a fake ape-<tag> in GOBIN that records its args.
func installPinnedApe(t *testing.T, tag string) string {
	t.Helper()
	bin := filepath.Join(os.Getenv("GOBIN"), "ape-"+tag)
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" >> \"$0.args\"\nexit 0\n"), 0o755))
	return bin
}

// rerunArgs is what a fake pinned ape was re-run with, "" if it never ran.
func rerunArgs(t *testing.T, tag string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(os.Getenv("GOBIN"), "ape-"+tag+".args"))
	if err != nil {
		return ""
	}
	return string(data)
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
		_ = os.Remove(calls) // shared by the subtests; each judges its own runs
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

		// The re-run is the fake ape-v0.99.0, which records its args and
		// exits 0; the pin's commit and that re-run are what this asserts.
		_, err := runFramework(t, "update", fw, root, "--no-fetch", "--update-ape")
		require.ErrorIs(t, err, errRerunDone, "the re-run IS the command: this process must not install again")
		require.Equal(t, ExitOK, exitCodeOf(t, err))
		require.Equal(t, "v0.99.0", pinOf(t, root))
		require.Equal(t, "chore(ape): update ape to v0.99.0", gitRun(t, root, "log", "-1", "--format=%s"))
		require.Contains(t, gitRun(t, root, "log", "-1", "--format=%b"), "Generator: ape framework update",
			"the framework's release record keys on this trailer")
		require.Equal(t, before, gitRun(t, root, "rev-parse", "HEAD~1"),
			"one commit, the pin's: the framework install is the re-run's, never mixed into it")
		require.Equal(t, ".bingo/ape.mod", gitRun(t, root, "show", "--name-only", "--format=", "HEAD"))
		args := rerunArgs(t, "v0.99.0")
		require.NotEmpty(t, args, "the command re-ran on the new pin")
		require.NotContains(t, args, "--update-ape", "a pinned ape older than 0.9.0 refuses the unknown flag")
	})
	t.Run("a launcher newer than the pin still moves the pin", func(t *testing.T) {
		// rc.2 compared the RUNNING binary: a launcher already at the newest
		// release said "nothing to update" while the pin sat behind.
		withApeVersion(t, "0.99.0")
		root := installed(t)

		_, err := runFramework(t, "update", fw, root, "--no-fetch", "--update-ape")
		require.ErrorIs(t, err, errRerunDone, "the re-run IS the command: this process must not install again")
		require.Equal(t, ExitOK, exitCodeOf(t, err))
		require.Equal(t, "v0.99.0", pinOf(t, root))
		require.Equal(t, "chore(ape): update ape to v0.99.0", gitRun(t, root, "log", "-1", "--format=%s"))
	})
	t.Run("a pin already newest is handed the command, without the flag", func(t *testing.T) {
		withApeVersion(t, "0.8.0")
		root := installed(t)
		seedApePin(t, root, "v0.99.0")
		gitCommitAll(t, root, "pin ape newest")
		installPinnedApe(t, "v0.99.0")
		head := gitRun(t, root, "rev-parse", "HEAD")

		_, err := runFramework(t, "update", fw, root, "--no-fetch", "--update-ape")
		require.ErrorIs(t, err, errRerunDone, "the re-run IS the command: this process must not install again")
		require.Equal(t, ExitOK, exitCodeOf(t, err))
		require.Equal(t, head, gitRun(t, root, "rev-parse", "HEAD"), "nothing to move, nothing committed")
		args := rerunArgs(t, "v0.99.0")
		require.NotEmpty(t, args, "the launcher handed the framework update to the pin")
		require.NotContains(t, args, "--update-ape")
	})
}

func TestUpdateApeFlagHelpers(t *testing.T) {
	require.True(t, hasUpdateApe([]string{"framework", "update", "--update-ape"}))
	require.True(t, hasUpdateApe([]string{"framework", "update", "--update-ape=true"}))
	require.False(t, hasUpdateApe([]string{"framework", "update", "--update-ape=false"}))
	require.False(t, hasUpdateApe([]string{"framework", "update", "--", "--update-ape"}))
	require.Equal(t, []string{"framework", "update", "--no-fetch", "--", "--update-ape"},
		withoutUpdateApe([]string{"framework", "update", "--update-ape", "--no-fetch", "--", "--update-ape"}))
}

// F16d: atomix pins bingo itself (.bingo/bingo.mod, built as
// $GOBIN/bingo-v0.10.0) and has no bingo on PATH; `ape update --pin` was
// refused "bingo is not on PATH". It now runs the project's own pinned
// bingo, and with none anywhere names every place it looked.
func TestUpdatePin_UsesTheProjectsPinnedBingo(t *testing.T) {
	calls := fakeBingo(t)
	fakeLatestRelease(t, "v0.99.0")
	withApeVersion(t, "0.99.0")
	// PATH without any bingo: only what git, sh and go need.
	var dirs []string
	for _, tool := range []string{"git", "sh", "go"} {
		p, err := exec.LookPath(tool)
		require.NoError(t, err)
		dirs = append(dirs, filepath.Dir(p))
	}
	t.Setenv("PATH", strings.Join(dirs, string(os.PathListSeparator)))
	if _, err := exec.LookPath("bingo"); err == nil {
		t.Skip("a bingo sits beside git, sh or go")
	}
	shim, err := os.ReadFile(filepath.Join(filepath.Dir(calls), "bingo"))
	require.NoError(t, err)

	root := committedProject(t)
	seedApePin(t, root, "v0.8.0")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".bingo", "bingo.mod"), []byte(
		"module _ // Auto generated by https://github.com/bwplotka/bingo. DO NOT EDIT\n\ngo 1.24.9\n\nrequire github.com/bwplotka/bingo v0.10.0\n"), 0o644))
	gitCommitAll(t, root, "pin ape and bingo")
	t.Chdir(root)

	err = runUpdate(t.Context(), Version, "human", true)
	require.Equal(t, ExitUsage, exitCodeOf(t, err), "no bingo anywhere: refused")
	require.ErrorContains(t, err, filepath.Join(os.Getenv("GOBIN"), "bingo-v0.10.0"), "names the project's own")
	require.ErrorContains(t, err, filepath.Join(os.Getenv("GOBIN"), "bingo"))

	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("GOBIN"), "bingo-v0.10.0"), shim, 0o755))
	require.NoError(t, runUpdate(t.Context(), Version, "human", true))
	require.Equal(t, "v0.99.0", pinOf(t, root))
	require.Equal(t, "chore(ape): update ape to v0.99.0", gitRun(t, root, "log", "-1", "--format=%s"))
	require.FileExists(t, calls, "the pinned bingo ran")
}
