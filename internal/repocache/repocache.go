// Package repocache keeps the framework and governance clones at their
// newest release (docs/explanation/framework-and-governance-clones.md).
//
// Each clone is resolved from what the user set — a flag, an environment
// variable, a config value — and, when nothing is set, from a clone ape
// keeps under the user cache directory. Who owns the clone decides what a
// sync may do to it: ape's own clone is overwritten freely, a user's is
// refused when dirty unless forced, and one ape cannot write to (a sandbox's
// read-only mount) is pinned and never touched.
//
// A leaf package on purpose: apexcfg resolves the effective governance path
// through it, and the framework install drives the sync.
package repocache

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Kind names which clone a Clone is.
type Kind string

const (
	KindFramework  Kind = "framework"
	KindGovernance Kind = "governance"
)

// Source is where a clone's path came from.
type Source string

const (
	SourceFlag   Source = "flag"
	SourceEnv    Source = "env"
	SourceConfig Source = "config"
	SourceCache  Source = "cache"
	// SourceNone: nothing names a clone (governance only: the project has
	// no governance repo).
	SourceNone Source = ""
)

// Environment variables and the built-in framework URL.
const (
	EnvFrameworkRepo  = "APEX_FRAMEWORK_REPO"
	EnvFrameworkURL   = "APEX_FRAMEWORK_URL"
	EnvGovernanceRepo = "APEX_GOVERNANCE_REPO"

	DefaultFrameworkURL = "https://github.com/exoar/apex_process_framework.git"
)

// Clone is a resolved clone location.
type Clone struct {
	Kind   Kind
	Path   string
	Source Source
	// URL is what ape clones its own copy from. Set for SourceCache only.
	URL string
}

// Owned reports whether the clone is ape's, which a sync may overwrite
// without asking.
func (c Clone) Owned() bool { return c.Source == SourceCache }

// None reports whether nothing names a clone.
func (c Clone) None() bool { return c.Source == SourceNone }

// userCacheDir is os.UserCacheDir, a seam for tests.
var userCacheDir = os.UserCacheDir

// CacheRoot is the directory ape keeps its clones under.
func CacheRoot() (string, error) {
	dir, err := userCacheDir()
	if err != nil {
		return "", fmt.Errorf("no user cache directory for ape's clones: %w", err)
	}
	return filepath.Join(dir, "ape"), nil
}

// CachePath is where ape keeps its clone of rawURL.
func CachePath(kind Kind, rawURL string) (string, error) {
	key, err := URLKey(rawURL)
	if err != nil {
		return "", err
	}
	root, err := CacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{root, string(kind)}, strings.Split(key, "/")...)...), nil
}

// scpURL matches git's scp-like form, user@host:path.
var scpURL = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`)

// safeSegment is one path segment a cache path may contain.
var safeSegment = regexp.MustCompile(`^[A-Za-z0-9._~+-]+$`)

// URLKey turns a clone URL into the host/path ape keys its cache by:
// https://github.com/o/r.git, git@github.com:o/r.git and ssh://git@github.com/o/r
// all become github.com/o/r; file:///srv/m/r becomes file/srv/m/r. A bare
// local path is refused: a clone already on disk is what the repo variables
// are for.
func URLKey(rawURL string) (string, error) {
	s := strings.TrimSpace(rawURL)
	var host, path string
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil || (u.Host == "" && u.Scheme != "file") {
			return "", fmt.Errorf("clone URL %q has no host", rawURL)
		}
		host, path = u.Hostname(), u.Path
		if u.Scheme == "file" {
			host = "file" // a mirror on this machine
		}
	case scpURL.MatchString(s) && !filepath.IsAbs(s):
		m := scpURL.FindStringSubmatch(s)
		host, path = m[1], m[2]
	default:
		return "", fmt.Errorf("clone URL %q is neither a URL nor host:path", rawURL)
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	segments := append([]string{strings.ToLower(host)}, strings.Split(path, "/")...)
	for _, seg := range segments {
		if seg == "." || seg == ".." || !safeSegment.MatchString(seg) {
			return "", fmt.Errorf("clone URL %q gives an unusable cache path segment %q", rawURL, seg)
		}
	}
	return strings.Join(segments, "/"), nil
}

// FrameworkURL is the URL ape clones its framework copy from.
func FrameworkURL() string {
	if v := strings.TrimSpace(os.Getenv(EnvFrameworkURL)); v != "" {
		return v
	}
	return DefaultFrameworkURL
}

// ResolveFramework resolves the framework clone: the flag, then
// $APEX_FRAMEWORK_REPO, then ape's cache. The cache path is returned
// whether or not it has been cloned yet.
func ResolveFramework(flagValue string) (Clone, error) {
	if v := strings.TrimSpace(flagValue); v != "" {
		return Clone{Kind: KindFramework, Path: v, Source: SourceFlag}, nil
	}
	if v := strings.TrimSpace(os.Getenv(EnvFrameworkRepo)); v != "" {
		return Clone{Kind: KindFramework, Path: v, Source: SourceEnv}, nil
	}
	u := FrameworkURL()
	path, err := CachePath(KindFramework, u)
	if err != nil {
		return Clone{}, fmt.Errorf("%s: %w", EnvFrameworkURL, err)
	}
	return Clone{Kind: KindFramework, Path: path, Source: SourceCache, URL: u}, nil
}

// ResolveGovernance resolves the governance clone: the project's
// governance_repository_path, then $APEX_GOVERNANCE_REPO, then ape's cache
// of governance_repository_url. With none of them it returns SourceNone. A
// URL that cannot key a cache is an error, so a typo is not read as "no
// governance repo".
func ResolveGovernance(configPath, rawURL string) (Clone, error) {
	if v := strings.TrimSpace(configPath); v != "" {
		return Clone{Kind: KindGovernance, Path: v, Source: SourceConfig}, nil
	}
	if v := strings.TrimSpace(os.Getenv(EnvGovernanceRepo)); v != "" {
		return Clone{Kind: KindGovernance, Path: v, Source: SourceEnv}, nil
	}
	if u := strings.TrimSpace(rawURL); u != "" {
		path, err := CachePath(KindGovernance, u)
		if err != nil {
			return Clone{}, fmt.Errorf("governance_repository_url: %w", err)
		}
		return Clone{Kind: KindGovernance, Path: path, Source: SourceCache, URL: u}, nil
	}
	return Clone{Kind: KindGovernance}, nil
}

// Exists reports whether path holds a git clone.
func Exists(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

// EnsureCloned clones ape's own copy when it is not there yet. A user's
// clone that does not exist is an error: ape never creates one where the
// user pointed.
func EnsureCloned(ctx context.Context, c Clone) (cloned bool, err error) {
	if Exists(c.Path) {
		return false, nil
	}
	if !c.Owned() {
		return false, fmt.Errorf("%s repo %s (from %s) is not a git clone", c.Kind, c.Path, c.Source)
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		return false, err
	}
	unlock, err := lockFile(c.Path + ".clone.lock")
	if err != nil {
		return false, err
	}
	defer unlock()
	if Exists(c.Path) { // another process cloned it while we waited
		return false, nil
	}
	_ = os.RemoveAll(c.Path) // a half-written clone from an interrupted run
	if _, err := runGit(ctx, "", "clone", "--quiet", c.URL, c.Path); err != nil {
		_ = os.RemoveAll(c.Path)
		return false, fmt.Errorf("could not clone the %s repo from %s (check that this machine has access to it): %w",
			c.Kind, c.URL, err)
	}
	return true, nil
}

// Writable reports whether ape can write into the clone's git directory.
// A read-only clone (the sandbox's mounts) is pinned.
func Writable(path string) bool {
	f, err := os.CreateTemp(filepath.Join(path, ".git"), ".ape-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// gitCmd is the git executable, a seam for tests.
var gitCmd = "git"

// runGit runs git in dir and returns trimmed stdout. safe.directory is
// scoped to dir for the same reason as the framework package's: a sandbox
// mount is owned by the host user while the guest runs as root.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	if dir != "" {
		args = append([]string{"-c", "safe.directory=" + dir}, args...)
	}
	cmd := exec.CommandContext(ctx, gitCmd, args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// ErrNoRelease: the clone holds no final vX.Y.Z tag.
var ErrNoRelease = errors.New("no release tag (vX.Y.Z)")
