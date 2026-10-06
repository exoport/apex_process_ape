package apecmd

// Keeping the framework and governance clones at their newest release
// (ape v0.7.0; docs/explanation/framework-and-governance-clones.md). The
// install still reads the framework from the tag's export; the checkout
// is moved for whoever reads the clone — a person reading the docs, and the
// governance skills, which read their clone live.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/repocache"
)

// resolveFrameworkClone resolves the framework clone: --repo, then
// $APEX_FRAMEWORK_REPO, then ape's cache (which may not be cloned yet).
func resolveFrameworkClone(flagValue string) (repocache.Clone, error) {
	return repocache.ResolveFramework(flagValue)
}

// resolveFrameworkRepo is the framework clone's path for the commands that
// only read it. Ape's cache counts once it exists; these commands never
// clone it.
func resolveFrameworkRepo(flagValue string) (string, error) {
	c, err := resolveFrameworkClone(flagValue)
	if err != nil {
		return "", err
	}
	if err := requireCloned(c); err != nil {
		return "", err
	}
	return c.Path, nil
}

// requireCloned refuses ape's cache before its first clone, naming the
// two ways forward.
func requireCloned(c repocache.Clone) error {
	if c.Owned() && !repocache.Exists(c.Path) {
		return fmt.Errorf("no framework repo: --repo and $%s are unset, and ape's own clone (%s, from %s) "+
			"does not exist yet — `ape framework update` or `setup` clones it, or set $%s",
			repocache.EnvFrameworkRepo, c.Path, c.URL, repocache.EnvFrameworkRepo)
	}
	return nil
}

// prepareFramework resolves the release the install will use — cloning
// ape's own copy when absent and fetching tags — and returns it as a pinned
// selection, so the install neither fetches again nor lands on another
// tag. It moves no checkout: moveFramework does that once the install has
// been validated, so a refused install leaves the clone where it was. A
// worktree install (nil sel) only needs ape's copy to exist.
func prepareFramework(ctx context.Context, c repocache.Clone, sel *framework.ReleaseSelector) (
	repocache.Target, *framework.ReleaseSelector, error,
) {
	if sel == nil {
		if _, err := repocache.EnsureCloned(ctx, c); err != nil {
			return repocache.Target{}, nil, cloneErr("framework_clone_sync", err)
		}
		return repocache.Target{}, nil, nil
	}
	t, err := repocache.Prepare(ctx, c, repocache.SyncOptions{Version: sel.Version, NoFetch: !sel.Fetch})
	if t.FetchWarning != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", t.FetchWarning)
	}
	if errors.Is(err, repocache.ErrNoRelease) {
		if t.Pinned {
			return t, nil, cloneErr("framework_pinned_untagged", fmt.Errorf(
				"the framework repo %s is read-only (pinned), and what is checked out there is not a release: "+
					"pass --version to install a tag it holds, or --from-worktree to install it as it is", c.Path))
		}
		return t, nil, cloneErr("framework_no_release", fmt.Errorf(
			"%s has no release tag (vX.Y.Z). Point --repo at the framework's ship repo, or install its "+
				"working tree with --from-worktree", c.Path))
	}
	if err != nil {
		return t, nil, cloneErr("framework_clone_sync", err)
	}
	return t, &framework.ReleaseSelector{Version: t.Tag}, nil
}

// moveFramework brings the framework clone's checkout to the prepared
// release — the last step before the install writes, after every check
// that could refuse it.
func moveFramework(ctx context.Context, w io.Writer, c repocache.Clone, t repocache.Target, forceClone bool) error {
	if t.Tag == "" {
		return nil // a worktree install: the clone is never moved
	}
	res, err := repocache.Move(ctx, c, t, forceClone)
	if dirty, ok := errors.AsType[*repocache.DirtyError](err); ok {
		return cloneErr(dirty.Code(), dirty)
	}
	if err != nil {
		return cloneErr("framework_clone_sync", err)
	}
	fmt.Fprintf(w, "framework repo: %s at %s%s\n", repocache.Describe(c), res.Tag, syncNote(res))
	return nil
}

// syncGovernanceClone brings the project's governance clone to its newest
// release and returns what to record. Only a dirty user clone stops the
// run: the framework install does not depend on governance, and the
// reconciliation skills fail fast with their own message when the canon is
// missing, so a clone or fetch failure is a warning.
//
// A configured path that is a directory but not a git clone — the eval's
// export of a governance tag — is read as it is: no clone, no fetch, no
// record, no warning. ape never clones the URL when a path is configured.
func syncGovernanceClone(ctx context.Context, w io.Writer, projectRoot string, noFetch, forceClone bool) (
	*framework.GovernanceInfo, error,
) {
	cfg, err := apexcfg.ResolveAt(projectRoot, nil)
	if err != nil {
		if errors.Is(err, apexcfg.ErrNotFound) {
			return nil, nil //nolint:nilnil // no config yet: nothing names a governance repo
		}
		return nil, err
	}
	c, err := cfg.GovernanceClone()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: governance repo not synced: %v\n", err)
		return nil, nil //nolint:nilnil // reported; the install does not depend on it
	}
	if c.None() {
		return nil, nil //nolint:nilnil // the project has no governance repo
	}
	if !c.Owned() && !repocache.Exists(c.Path) {
		if st, statErr := os.Stat(c.Path); statErr == nil && st.IsDir() {
			fmt.Fprintf(w, "governance repo: %s (not a git clone: read as-is)\n", c.Path)
			return nil, nil //nolint:nilnil // nothing to sync or record
		}
		fmt.Fprintf(os.Stderr, "warning: the governance repo %s (from %s) does not exist; skills will not find it\n",
			c.Path, c.Source)
		return nil, nil //nolint:nilnil // reported; the install does not depend on it
	}
	res, err := repocache.Sync(ctx, c, repocache.SyncOptions{NoFetch: noFetch, Force: forceClone})
	if res.FetchWarning != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", res.FetchWarning)
	}
	if dirty, ok := errors.AsType[*repocache.DirtyError](err); ok {
		return nil, cloneErr(dirty.Code(), dirty)
	}
	if errors.Is(err, repocache.ErrNoRelease) {
		fmt.Fprintf(os.Stderr, "warning: the governance repo %s has no release tag (vX.Y.Z); skills read it as it is\n",
			repocache.Describe(c))
		return &framework.GovernanceInfo{RepoOrigin: res.Origin}, nil
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: governance repo not synced: %v\n", err)
		return nil, nil //nolint:nilnil // reported; the install does not depend on it
	}
	fmt.Fprintf(w, "governance repo: %s at %s%s\n", repocache.Describe(c), res.Tag, syncNote(res))
	// Recorded as it stands: a clone kept ahead of the release is not at it.
	head, _ := gitOut(ctx, c.Path, "rev-parse", "HEAD")
	tag := res.Tag
	if res.Kept != "" {
		tag = ""
	}
	return &framework.GovernanceInfo{RepoOrigin: res.Origin, VersionTag: tag, GitHash: strings.TrimSpace(head)}, nil
}

// cloneErr is a clone that could not be synced: exit 3, the framework
// source, like every other framework-source failure. Returned rather than
// exited on, so the caller's deferred cleanups run.
func cloneErr(code string, err error) error {
	return usageErrExit(exitCodeFrameworkValidation, fmt.Errorf("%s: %w", code, err))
}

func syncNote(res repocache.SyncResult) string {
	switch {
	case res.Kept != "":
		return " (not moved: " + res.Kept + ")"
	case res.Pinned:
		return " (read-only: pinned, not moved)"
	case res.Cloned:
		return " (cloned)"
	case res.Moved:
		return " (checked out)"
	default:
		return " (already there)"
	}
}

// syncClones moves both clones, governance first so that a refused
// governance clone stops the run before the framework clone moves. Called
// after the install has been validated and before it writes.
func syncClones(ctx context.Context, w io.Writer, c repocache.Clone, fwTarget repocache.Target, projectRoot string,
	noFetch, forceClone bool,
) (*framework.GovernanceInfo, error) {
	gov, err := syncGovernanceClone(ctx, w, projectRoot, noFetch, forceClone)
	if err != nil {
		return nil, err
	}
	if err := moveFramework(ctx, w, c, fwTarget, forceClone); err != nil {
		return nil, err
	}
	return gov, nil
}

// forceCloneHelp is --force-clone's help, shared by setup and update.
const forceCloneHelp = "Overwrite local changes in YOUR framework or governance clone when moving it to the release " +
	"(ape's own clones are always overwritten; a clone ahead of the release is never moved)"

// cloneNoteWriter is where the one-line sync notes go: stdout for a
// person, stderr when stdout carries a JSON or YAML payload.
func cloneNoteWriter(cmd interface{ OutOrStdout() io.Writer }, format output.Format) io.Writer {
	if format == output.FormatHuman {
		return cmd.OutOrStdout()
	}
	return os.Stderr
}

// printCloneLocations says where each clone is and where its path came
// from — what a person needs to find the docs of the release they run.
func printCloneLocations(w io.Writer, repoFlag, projectRoot string) {
	fmt.Fprintln(w)
	if c, err := resolveFrameworkClone(repoFlag); err != nil {
		fmt.Fprintf(w, "Framework repo:  %v\n", err)
	} else {
		fmt.Fprintf(w, "Framework repo:  %s%s\n", repocache.Describe(c), stateSuffix(c))
	}
	cfg, err := apexcfg.ResolveAt(projectRoot, nil)
	if err != nil {
		fmt.Fprintln(w, "Governance repo: (no _apex/config.yaml)")
		return
	}
	if c, err := cfg.GovernanceClone(); err != nil {
		fmt.Fprintf(w, "Governance repo: %v\n", err)
	} else {
		fmt.Fprintf(w, "Governance repo: %s%s\n", repocache.Describe(c), stateSuffix(c))
	}
}

// stateSuffix is where a clone stands against its newest release — "main,
// 2 commit(s) past v0.28.2" — so a clone ape left in place says why.
func stateSuffix(c repocache.Clone) string {
	if st := repocache.State(context.Background(), c); st != "" {
		return " — " + st
	}
	return ""
}
