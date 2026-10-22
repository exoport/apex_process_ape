// Package usercachetest keeps a test binary, and every process it starts,
// out of the developer's real user cache.
//
// ape keeps per-OS-user state under os.UserCacheDir()/ape: the usage
// reading and the usage-runs launch files (W4), the startup probe's
// verdicts, the update check, the framework and governance clones. A test
// that runs ape's launch path, in process or as a built binary, writes
// there unless the cache is somewhere else. Isolate, called from TestMain,
// puts it somewhere else for the whole binary.
package usercachetest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Env names the temp cache an outer test binary made. A process started
// with it set (a copy of the test binary run as ape, a nested test) keeps
// the environment it was given rather than making its own: the outer
// binary owns the directory, and a test that pointed the cache elsewhere
// for its child keeps that.
const Env = "APE_TEST_USER_CACHE"

// Isolate points os.UserCacheDir at a fresh temp directory for the rest of
// the process and everything it starts — XDG_CACHE_HOME on Linux (and the
// BSDs), LocalAppData on Windows — and returns the cleanup that removes
// it, for TestMain to run after m.Run.
//
// macOS is not covered: os.UserCacheDir there is $HOME/Library/Caches,
// which only HOME moves, and moving HOME would move every other per-user
// file the tests read (git's global config among them).
//
// Go's own build cache defaults to the same directory, so GOCACHE is
// pinned to where it was first: a test that runs `go build` keeps a warm
// cache instead of rebuilding into the temp directory.
func Isolate(prefix string) (cleanup func()) {
	if os.Getenv(Env) != "" {
		return func() {}
	}
	if os.Getenv("GOCACHE") == "" {
		if gocache := goCache(); gocache != "" {
			_ = os.Setenv("GOCACHE", gocache)
		}
	}
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		panic("usercachetest: " + err.Error())
	}
	for _, k := range []string{Env, "XDG_CACHE_HOME", "LocalAppData"} {
		_ = os.Setenv(k, dir)
	}
	return func() { _ = os.RemoveAll(dir) }
}

// goCache is the build cache `go` uses now, before the redirect: `go env
// GOCACHE` (which honours a `go env -w` setting, "off" included), else its
// default under the user cache; "" when neither is known.
func goCache() string {
	if out, err := exec.CommandContext(context.Background(), "go", "env", "GOCACHE").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "go-build")
	}
	return ""
}
