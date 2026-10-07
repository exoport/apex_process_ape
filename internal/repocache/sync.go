package repocache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

var (
	finalTag     = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	candidateTag = regexp.MustCompile(`^v\d+\.\d+\.\d+-rc\.\d+$`)
)

// IsFinalTag reports whether t is a final release tag, vX.Y.Z.
func IsFinalTag(t string) bool { return finalTag.MatchString(t) }

// IsReleaseTag reports whether t is vX.Y.Z or vX.Y.Z-rc.N.
func IsReleaseTag(t string) bool { return finalTag.MatchString(t) || candidateTag.MatchString(t) }

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

// HeadRelease returns the release tag HEAD sits on (the highest, when
// several do), or "" when none does.
func HeadRelease(ctx context.Context, repo string) (string, error) {
	out, err := runGit(ctx, repo, "tag", "--points-at", "HEAD")
	if err != nil {
		return "", err
	}
	best := ""
	for t := range strings.FieldsSeq(out) {
		if IsReleaseTag(t) && (best == "" || semver.Compare(t, best) > 0) {
			best = t
		}
	}
	return best, nil
}

// FetchTags runs `git fetch --tags --force origin`.
func FetchTags(ctx context.Context, repo string) error {
	_, err := runGit(ctx, repo, "fetch", "--quiet", "--tags", "--force", "origin")
	return err
}

// SyncOptions parameterize Sync.
type SyncOptions struct {
	// Version is an exact tag to check out; empty means the newest final
	// release.
	Version string
	// NoFetch skips the tag fetch.
	NoFetch bool
	// Force overwrites a dirty USER clone (--force-clone). Ape's own is
	// always overwritten.
	Force bool
}

// Target is the release a clone is to stand at, resolved before anything
// moves: Prepare fetches and picks it, and the caller validates it (the
// install's layout and min_ape_version checks) before Move touches the
// checkout. A refused install then leaves the clone where it was.
type Target struct {
	// Tag and Commit are the release. For a pinned clone, the release HEAD
	// sits on.
	Tag    string
	Commit string
	// Origin is the clone's origin URL ("" when it has none).
	Origin string
	// Pinned: the clone is read-only and will not be touched.
	Pinned bool
	// Cloned: ape created its own copy on this run.
	Cloned bool
	// FetchWarning is set when the tag fetch failed and the tags already
	// in the clone were used.
	FetchWarning string
}

// SyncResult is what Sync (or Prepare then Move) did.
type SyncResult struct {
	Target
	// Moved: the checkout changed.
	Moved bool
	// Kept explains why a user's clone was left where it is although it is
	// not at the release — its branch is ahead of it or diverged from it
	// (committed work ape must not leave behind). Empty otherwise.
	Kept string
}

// DirtyError is a user's clone with local changes, which a sync refuses to
// overwrite without --force-clone.
type DirtyError struct {
	Clone   Clone
	Entries []string
}

func (e *DirtyError) Error() string {
	shown := e.Entries
	const most = 10
	more := ""
	if len(shown) > most {
		more = fmt.Sprintf("\n  … and %d more", len(shown)-most)
		shown = shown[:most]
	}
	return fmt.Sprintf("the %s repo %s (from %s) has local changes, and ape keeps it at the newest release "+
		"by checking that release out. Commit, stash or discard them, or pass --force-clone to overwrite them:\n  %s%s",
		e.Clone.Kind, e.Clone.Path, e.Clone.Source, strings.Join(shown, "\n  "), more)
}

// Code is the stable error code a caller reports.
func (e *DirtyError) Code() string { return string(e.Clone.Kind) + "_clone_dirty" }

// Sync is Prepare then Move, for a caller with nothing to validate in
// between (governance).
func Sync(ctx context.Context, c Clone, opts SyncOptions) (SyncResult, error) {
	t, err := Prepare(ctx, c, opts)
	if err != nil {
		return SyncResult{Target: t}, err
	}
	return Move(ctx, c, t, opts.Force)
}

// Prepare resolves the release a clone is to stand at: clone ape's copy
// when absent, fetch tags, pick the tag. It moves nothing. ErrNoRelease
// (with a usable Target) means there is no tag to move to.
func Prepare(ctx context.Context, c Clone, opts SyncOptions) (Target, error) {
	var t Target
	cloned, err := EnsureCloned(ctx, c)
	if err != nil {
		return t, err
	}
	t.Cloned = cloned
	// The configured URL, not get-url's: that applies the machine's
	// insteadOf rules, and Origin is recorded in committed history.
	t.Origin, _ = runGit(ctx, c.Path, "config", "--get", "remote.origin.url")

	if !Writable(c.Path) {
		return preparePinned(ctx, c, opts, t)
	}
	if !opts.NoFetch && !cloned {
		unlock, err := lockFile(filepath.Join(c.Path, ".git", "ape-sync.lock"))
		if err != nil {
			return t, err
		}
		if err := FetchTags(ctx, c.Path); err != nil {
			t.FetchWarning = fmt.Sprintf("could not fetch tags into the %s repo %s, so the releases already "+
				"there are all that was considered: %v", c.Kind, c.Path, err)
		}
		unlock()
	}
	tag := opts.Version
	if tag == "" {
		if tag, err = NewestRelease(ctx, c.Path); err != nil {
			return t, err
		}
		if tag == "" {
			return t, ErrNoRelease
		}
	}
	commit, err := TagCommit(ctx, c.Path, tag)
	if err != nil {
		return t, fmt.Errorf("tag %s is not in the %s repo %s", tag, c.Kind, c.Path)
	}
	t.Tag, t.Commit = tag, commit
	return t, nil
}

// preparePinned reports the release a read-only clone stands at, without
// touching it. An explicit Version is honoured — it names a tag, which a
// read-only clone can still export — and otherwise the release is HEAD's.
func preparePinned(ctx context.Context, c Clone, opts SyncOptions, t Target) (Target, error) {
	t.Pinned = true
	tag := opts.Version
	if tag == "" {
		h, err := HeadRelease(ctx, c.Path)
		if err != nil {
			return t, err
		}
		tag = h
	}
	if tag == "" {
		return t, ErrNoRelease
	}
	commit, err := TagCommit(ctx, c.Path, tag)
	if err != nil {
		return t, fmt.Errorf("tag %s is not in the read-only %s repo %s", tag, c.Kind, c.Path)
	}
	t.Tag, t.Commit = tag, commit
	return t, nil
}

// Move brings the checkout to t, under the clone's lock, deciding from the
// clone's state at that moment.
//
// Ape's own clone is detached at the release, overwritten whatever it
// holds. A USER's clone only ever moves forward, because it may be where
// someone commits — the framework's ship repo holds promotion commits past
// its last tag, and a developer's build repo holds work past an rc:
//
//   - at the release already: nothing, branch and edits kept;
//   - behind it (HEAD an ancestor of the release): a branch is
//     fast-forwarded, so "on main" stays true; a detached HEAD is detached
//     at the release; local changes refuse unless force;
//   - ahead of it or diverged: left where it is, and Kept says why.
func Move(ctx context.Context, c Clone, t Target, force bool) (SyncResult, error) {
	res := SyncResult{Target: t}
	if t.Pinned || t.Commit == "" {
		return res, nil
	}
	unlock, err := lockFile(filepath.Join(c.Path, ".git", "ape-sync.lock"))
	if err != nil {
		return res, err
	}
	defer unlock()

	head, _ := runGit(ctx, c.Path, "rev-parse", "HEAD")
	status, err := runGit(ctx, c.Path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return res, err
	}
	if head == t.Commit && (status == "" || !c.Owned()) {
		return res, nil
	}
	if c.Owned() {
		if err := checkout(ctx, c.Path, t.Commit, true); err != nil {
			return res, err
		}
		_, err := runGit(ctx, c.Path, "clean", "-q", "-ffdx")
		res.Moved = err == nil
		return res, err
	}

	if !isAncestor(ctx, c.Path, head, t.Commit) {
		res.Kept = keptReason(ctx, c.Path, head, t)
		return res, nil
	}
	if status != "" && !force {
		return res, &DirtyError{Clone: c, Entries: strings.Split(status, "\n")}
	}
	branch, _ := runGit(ctx, c.Path, "symbolic-ref", "-q", "--short", "HEAD")
	switch {
	case branch != "" && status == "":
		_, err = runGit(ctx, c.Path, "merge", "--quiet", "--ff-only", t.Commit)
	case branch != "":
		_, err = runGit(ctx, c.Path, "reset", "--quiet", "--hard", t.Commit)
	default:
		err = checkout(ctx, c.Path, t.Commit, status != "")
	}
	if err != nil {
		return res, err
	}
	if status != "" {
		if _, err := runGit(ctx, c.Path, "clean", "-q", "-fd"); err != nil {
			return res, err
		}
	}
	res.Moved = true
	return res, nil
}

func checkout(ctx context.Context, dir, commit string, force bool) error {
	args := []string{"-c", "advice.detachedHead=false", "checkout", "--quiet", "--detach", commit}
	if force {
		args = append(args, "--force")
	}
	_, err := runGit(ctx, dir, args...)
	return err
}

// isAncestor reports whether a is an ancestor of b (or b itself).
func isAncestor(ctx context.Context, dir, a, b string) bool {
	_, err := runGit(ctx, dir, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// keptReason names why a user's clone that is not at t was left alone.
func keptReason(ctx context.Context, dir, head string, t Target) string {
	where, _ := runGit(ctx, dir, "symbolic-ref", "-q", "--short", "HEAD")
	if where == "" {
		where = "HEAD (detached)"
	}
	ahead, _ := runGit(ctx, dir, "rev-list", "--count", t.Commit+".."+head)
	if isAncestor(ctx, dir, t.Commit, head) {
		return fmt.Sprintf("%s is %s commit(s) past %s", where, ahead, t.Tag)
	}
	return fmt.Sprintf("%s has diverged from %s (%s commit(s) not in it)", where, t.Tag, ahead)
}

// State is a one-line account of where a clone stands against its newest
// final release, for status: "main at v0.28.2", "main, 2 commits past
// v0.28.2", "detached at v0.28.2". Empty when it cannot tell.
func State(ctx context.Context, c Clone) string {
	if c.None() || c.Path == "" || !Exists(c.Path) {
		return ""
	}
	where, _ := runGit(ctx, c.Path, "symbolic-ref", "-q", "--short", "HEAD")
	if where == "" {
		where = "detached"
	}
	desc, err := runGit(ctx, c.Path, "describe", "--tags", "--long", "--match", "v[0-9]*", "--exclude", "*-*")
	if err != nil {
		return where + ", no release behind it"
	}
	// vX.Y.Z-<n>-g<sha>
	parts := strings.Split(desc, "-")
	const tail = 2
	if len(parts) < tail+1 {
		return where
	}
	tag, n := strings.Join(parts[:len(parts)-tail], "-"), parts[len(parts)-tail]
	if n == "0" {
		return where + " at " + tag
	}
	return fmt.Sprintf("%s, %s commit(s) past %s", where, n, tag)
}

// Describe is a one-line account of a clone for status output.
func Describe(c Clone) string {
	switch {
	case c.None():
		return "(none configured)"
	case c.Owned() && !Exists(c.Path):
		return fmt.Sprintf("%s (ape's cache, not cloned yet; from %s)", c.Path, c.URL)
	case c.Owned():
		return c.Path + " (ape's cache)"
	default:
		if _, err := os.Stat(c.Path); err != nil {
			return fmt.Sprintf("%s (from %s; does not exist)", c.Path, c.Source)
		}
		return fmt.Sprintf("%s (from %s)", c.Path, c.Source)
	}
}
