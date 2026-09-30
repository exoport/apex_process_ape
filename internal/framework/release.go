package framework

// Installing a framework RELEASE rather than a clone's working tree (ape
// v0.4.0; docs/explanation/framework-updates-from-releases.md).
//
// The clone named by --repo is used as a store of git objects: tags are
// fetched into it, the chosen tag's tree is exported with `git archive`, and
// nothing in the clone's checkout, branch or working tree is read or moved.
// That is what lets "teams only see evaluated versions" hold by construction:
// a commit pushed to the ship repo's main reaches nobody until it is tagged.

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

var (
	finalTag     = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	candidateTag = regexp.MustCompile(`^v\d+\.\d+\.\d+-rc\.\d+$`)
)

// ReleaseSelector asks for a release instead of the clone's working tree.
type ReleaseSelector struct {
	// Version is an exact tag, vX.Y.Z or vX.Y.Z-rc.N; empty means the
	// highest final vX.Y.Z. A candidate is only ever installed by name.
	Version string
	// Fetch runs `git fetch --tags --force origin` first. A failed fetch is
	// a warning, never an error: the tags already in the clone still name
	// real releases.
	Fetch bool
}

// Release is a resolved framework release.
type Release struct {
	Tag    string
	Commit string
	// FetchWarning is set when the tag fetch failed and the resolution used
	// the tags already present.
	FetchWarning string
}

// ValidReleaseRef reports whether v is a tag shape --version accepts.
func ValidReleaseRef(v string) bool {
	return finalTag.MatchString(v) || candidateTag.MatchString(v)
}

// ResolveRelease picks the tag to install from repo.
func ResolveRelease(ctx context.Context, repo string, sel ReleaseSelector) (Release, error) {
	var rel Release
	if sel.Version != "" && !ValidReleaseRef(sel.Version) {
		return rel, &ValidationError{Code: "framework_version_invalid", Detail: fmt.Sprintf(
			"--version %q is neither vX.Y.Z nor vX.Y.Z-rc.N", sel.Version)}
	}
	if sel.Fetch {
		if _, err := runGit(ctx, repo, "fetch", "--tags", "--force", "origin"); err != nil {
			rel.FetchWarning = fmt.Sprintf("could not fetch tags from origin, so the releases already in %s are all "+
				"that was considered: %v", repo, err)
		}
	}
	tag := sel.Version
	if tag == "" {
		newest, err := NewestRelease(ctx, repo)
		if err != nil {
			return rel, err
		}
		if newest == "" {
			return rel, &ValidationError{Code: "framework_no_release", Detail: fmt.Sprintf(
				"%s has no release tag (vX.Y.Z). Point --repo at the framework's ship repo, or install its "+
					"working tree with --from-worktree", repo)}
		}
		tag = newest
	}
	commit, err := TagCommit(ctx, repo, tag)
	if err != nil {
		return rel, &ValidationError{Code: "framework_tag_missing", Detail: fmt.Sprintf(
			"tag %s is not in %s%s", tag, repo, fetchHint(sel.Fetch))}
	}
	rel.Tag, rel.Commit = tag, commit
	return rel, nil
}

func fetchHint(fetched bool) string {
	if fetched {
		return ""
	}
	return " (and --no-fetch skipped fetching it)"
}

// NewestRelease returns the highest final vX.Y.Z tag in repo by semver, or
// "" when there is none. Candidates and any other suffix never count.
func NewestRelease(ctx context.Context, repo string) (string, error) {
	out, err := runGit(ctx, repo, "tag", "--list", "v*")
	if err != nil {
		return "", err
	}
	best := ""
	for t := range strings.FieldsSeq(out) {
		if finalTag.MatchString(t) && (best == "" || semver.Compare(t, best) > 0) {
			best = t
		}
	}
	return best, nil
}

// TagCommit returns the commit a tag names in repo.
func TagCommit(ctx context.Context, repo, tag string) (string, error) {
	return runGit(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/tags/"+tag+"^{commit}")
}

// ReadAtTag returns a file's bytes as of a tag, and false when the tag's
// tree has no such file.
func ReadAtTag(ctx context.Context, repo, tag, path string) (data []byte, found bool, err error) {
	spec := "refs/tags/" + tag + ":" + filepath.ToSlash(path)
	if _, err := runGit(ctx, repo, "cat-file", "-e", spec); err != nil {
		return nil, false, nil //nolint:nilerr // cat-file -e failing IS the "absent" answer
	}
	cmd := exec.CommandContext(ctx, GitCmd, "-c", "safe.directory="+repo, "show", spec) //nolint:gosec // git, with a tag ape resolved itself
	cmd.Dir = repo
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, false, fmt.Errorf("git show %s: %w (stderr: %s)", spec, err, strings.TrimSpace(stderr.String()))
	}
	return out, true, nil
}

// ExportTag writes the tree a tag names into a new temporary directory, via
// `git archive`, and returns it with its cleanup. The clone itself is only
// read.
func ExportTag(ctx context.Context, repo, tag string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "ape-framework-"+tag+"-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	cmd := exec.CommandContext(ctx, GitCmd, "-c", "safe.directory="+repo, "archive", "--format=tar", "refs/tags/"+tag) //nolint:gosec // git, with a tag ape resolved itself
	cmd.Dir = repo
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if err := cmd.Start(); err != nil {
		cleanup()
		return "", nil, err
	}
	extractErr := extractTar(stdout, dir)
	waitErr := cmd.Wait()
	if waitErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("git archive %s: %w (stderr: %s)", tag, waitErr, strings.TrimSpace(stderr.String()))
	}
	if extractErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("unpack %s: %w", tag, extractErr)
	}
	return dir, cleanup, nil
}

// extractTar unpacks a `git archive` stream into dest. Only regular files,
// directories and symlinks occur in one; anything else is skipped. A path
// that would land outside dest is refused rather than trusted.
func extractTar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	root := filepath.Clean(dest) + string(os.PathSeparator)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.FromSlash(hdr.Name))
		if !strings.HasPrefix(target+string(os.PathSeparator), root) {
			return fmt.Errorf("archive entry %q escapes the export directory", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil { //nolint:gosec // a framework release is bounded; not an untrusted upload
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}

// ApeVersionVerdict is how a running ape compares with a release's
// min_ape_version.
type ApeVersionVerdict string

const (
	ApeVersionOK        ApeVersionVerdict = "ok"
	ApeVersionBelow     ApeVersionVerdict = "below"
	ApeVersionUnknown   ApeVersionVerdict = "unknown"    // a dev build or a pseudo-version: not comparable
	ApeVersionNoMinimum ApeVersionVerdict = "no-minimum" // the framework declares none
)

// pseudoVersion matches a Go pseudo-version's tail (`-0.20260930123456-abcdef123456`,
// or `.0.2026…` after a prerelease), which a local build reports.
var pseudoVersion = regexp.MustCompile(`[-.]0\.\d{14}-[0-9a-f]{12}`)

// CompareApeVersion judges running against minimum. One rule beyond semver:
// a candidate of exactly the minimum's version satisfies it, so
// 0.4.0-rc.2 meets >= 0.4.0 — it is the build that becomes 0.4.0 — while
// it does not meet >= 0.4.1.
func CompareApeVersion(running, minimum string) ApeVersionVerdict {
	minimum = strings.TrimSpace(minimum)
	if minimum == "" {
		return ApeVersionNoMinimum
	}
	run := "v" + strings.TrimPrefix(strings.TrimSpace(running), "v")
	lo := "v" + strings.TrimPrefix(minimum, "v")
	if !semver.IsValid(run) || pseudoVersion.MatchString(run) || !semver.IsValid(lo) {
		return ApeVersionUnknown
	}
	if semver.Compare(run, lo) >= 0 {
		return ApeVersionOK
	}
	if semver.Prerelease(run) != "" && semver.Prerelease(lo) == "" &&
		strings.TrimSuffix(run, semver.Prerelease(run)) == lo {
		return ApeVersionOK
	}
	return ApeVersionBelow
}

// InstalledIsAhead reports whether an installed release tag is newer than
// candidate by semver — a release candidate installed by name, ahead of the
// newest final release. A default update must not go backwards from it.
func InstalledIsAhead(installed, candidate string) bool {
	return semver.IsValid(installed) && semver.IsValid(candidate) && semver.Compare(installed, candidate) > 0
}
