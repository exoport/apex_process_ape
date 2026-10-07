package apecmd

// The release-install half of `ape framework setup|update` (ape v0.4.0;
// docs/explanation/framework-updates-from-releases.md): which tag to
// install, whether this ape is new enough for it, updating ape when it is
// not, and the commits that record the install.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/updatecache"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const (
	// exitCodeApeBelowMinimum: ape is older than the release's
	// min_ape_version and was not, or could not be, updated. Nothing was
	// written.
	exitCodeApeBelowMinimum = 11
	// exitCodeCommitFailed: the install is complete but a commit could not
	// be made (a hook failed); the changes are left staged.
	exitCodeCommitFailed = 12

	// envFrameworkReexec marks the run ape starts after updating itself,
	// so a run that is still below the minimum stops instead of updating
	// again.
	envFrameworkReexec = "APE_FRAMEWORK_UPDATE_REEXEC"

	apeModulePath    = "github.com/exoport/apex_process_ape/cmd/ape"
	generatorTrailer = "Generator: ape framework update"
)

// releaseFlags are the flags setup and update share.
type releaseFlags struct {
	version      string
	fromWorktree bool
	noCommit     bool
}

func addReleaseFlags(cmd *cobra.Command, f *releaseFlags) {
	cmd.Flags().StringVar(&f.version, "version", "",
		"Install exactly this release tag, vX.Y.Z or vX.Y.Z-rc.N (default: the highest final vX.Y.Z; a candidate is only ever installed by name)")
	cmd.Flags().BoolVar(&f.fromWorktree, "from-worktree", false,
		"Install the framework repo's working tree instead of a release tag (the pre-v0.4.0 behaviour: main-only, clean, fast-forward)")
	cmd.Flags().BoolVar(&f.noCommit, "no-commit", false,
		"Leave the result in the working tree instead of committing it")
}

// selector turns the flags into a release selection; nil means a
// working-tree install.
func (f *releaseFlags) selector(noFetch bool) (*framework.ReleaseSelector, error) {
	if f.fromWorktree {
		if f.version != "" {
			return nil, usageErrExit(ExitUsage, errors.New("--version and --from-worktree are exclusive: a worktree install has no tag to choose"))
		}
		return nil, nil //nolint:nilnil // a nil selection IS the worktree install, by UpdateOptions.Release's contract
	}
	if f.version != "" && !framework.ValidReleaseRef(f.version) {
		return nil, usageErrExit(ExitUsage, fmt.Errorf("--version %q is neither vX.Y.Z nor vX.Y.Z-rc.N", f.version))
	}
	return &framework.ReleaseSelector{Version: f.version, Fetch: !noFetch}, nil
}

// readableSource returns a directory holding the framework as the install
// would see it — the release's export, or the working tree — for `--plan`,
// `--dry-run` and the minimum-version check.
func readableSource(ctx context.Context, repo string, sel *framework.ReleaseSelector) (dir, tag string, cleanup func(), err error) {
	if sel == nil {
		return repo, "", func() {}, nil
	}
	dir, rel, cleanup, err := framework.ExportRelease(ctx, repo, *sel)
	if err != nil {
		return "", "", nil, err
	}
	if rel.FetchWarning != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", rel.FetchWarning)
	}
	return dir, rel.Tag, cleanup, nil
}

// prepareInstall resolves the release once, checks this ape against its
// min_ape_version, and returns the selection the install must use: the
// resolved tag, pinned, with no second fetch — so the install cannot land
// on a different tag than the one that was checked. A nil selection (a
// worktree install) is checked against the working tree.
//
// newest says the release was chosen as the newest, not named by
// --version: the clone sync has already pinned sel to a tag by then, so
// sel alone no longer says so.
func prepareInstall(ctx context.Context, repo, projectRoot string, sel *framework.ReleaseSelector, commit, newest bool) (
	*framework.ReleaseSelector, func(), error,
) {
	src, tag, cleanup, err := readableSource(ctx, repo, sel)
	if err != nil {
		return nil, nil, err
	}
	// A default update never goes backwards: a candidate installed by
	// name, newer than the newest final release, stays until --version
	// says otherwise.
	if sel != nil && newest {
		if meta, mErr := framework.ReadMetadata(projectRoot); mErr == nil &&
			framework.InstalledIsAhead(meta.Framework.VersionTag, tag) {
			cleanup()
			fmt.Fprintf(os.Stdout, "framework: installed %s is newer than the newest release %s (a candidate, installed by "+
				"name); keeping it. Pass --version %s to go back to the release.\n", meta.Framework.VersionTag, tag, tag)
			return nil, nil, errKeepInstalled
		}
	}
	if err := checkApeMinimum(ctx, os.Stdout, src, tag, projectRoot, commit); err != nil {
		cleanup()
		return nil, nil, err
	}
	if sel == nil {
		return nil, cleanup, nil
	}
	return &framework.ReleaseSelector{Version: tag}, cleanup, nil
}

// errKeepInstalled ends an update that has nothing to do because the
// installed release is ahead of the newest one. Not a failure: exit 0.
var errKeepInstalled = errors.New("installed release is ahead of the newest release")

// installLabelSHA is how much of a commit names an untagged install.
const installLabelSHA = 12

// installLabel names what was installed in commit subjects.
func installLabel(meta framework.Metadata) string {
	if meta.Framework.VersionTag != "" {
		return meta.Framework.VersionTag
	}
	if len(meta.Framework.GitHash) >= installLabelSHA {
		return meta.Framework.GitHash[:installLabelSHA]
	}
	return meta.Framework.GitHash
}

// ---------------------------------------------------------------------
// min_ape_version

// checkApeMinimum compares this ape with the release's min_ape_version
// and, when it falls short, updates ape and re-runs the command on the new
// binary. It returns nil when the install may proceed in this process.
// A re-run's own outcome is final, so it exits with the child's status.
func checkApeMinimum(ctx context.Context, w io.Writer, src, tag, projectRoot string, commit bool) error {
	manifest, err := framework.LoadApeCommands(src)
	if err != nil {
		return usageErrExit(exitCodeFrameworkValidation, err)
	}
	minimum := ""
	if manifest != nil {
		minimum = manifest.MinApeVersion
	}
	label := tag
	if label == "" {
		label = "this framework"
	}
	switch framework.CompareApeVersion(Version, minimum) {
	case framework.ApeVersionNoMinimum:
		fmt.Fprintf(w, "ape: %s declares no min_ape_version\n", label)
		return nil
	case framework.ApeVersionUnknown:
		fmt.Fprintf(os.Stderr, "warning: ape %s cannot be compared with %s's min_ape_version %s (a dev build or a pseudo-version) — continuing\n",
			Version, label, minimum)
		return nil
	case framework.ApeVersionOK:
		offerNewerApe(w)
		return nil
	case framework.ApeVersionBelow:
	}

	route := apeUpdateRoute(projectRoot)
	fmt.Fprintf(os.Stderr, "ape %s is older than %s needs (%s).\n", Version, label, minimum)
	if os.Getenv(envFrameworkReexec) == "1" {
		return usageErrExit(exitCodeApeBelowMinimum, fmt.Errorf(
			"still below %s's minimum after updating ape — the newest ape release does not meet it yet; nothing was written", label))
	}
	if !interactive() {
		fmt.Fprintf(os.Stderr, "run: %s\n", route.command("latest"))
		return usageErrExit(exitCodeApeBelowMinimum, errors.New("not updating ape without a terminal to ask; nothing was written"))
	}
	if !askYesNo(fmt.Sprintf("Update ape first (%s)?", route.name), true) {
		return usageErrExit(exitCodeApeBelowMinimum, errors.New("ape not updated; nothing was written"))
	}
	return updateApeAndRerun(ctx, w, route, projectRoot, commit)
}

// offerNewerApe mentions, and never forces, a newer final ape when this
// one already meets the minimum. It reads the version the background
// update check last cached rather than asking the network: an install must
// not depend on GitHub being reachable for a courtesy line.
func offerNewerApe(w io.Writer) {
	entry := updatecache.Load()
	if entry == nil || !isNewerVersion(Version, entry.LatestVersion) {
		return
	}
	fmt.Fprintf(w, "ape: %s is available (this is %s, which meets the release's minimum); `ape update` installs it\n",
		entry.LatestVersion, Version)
}

// apeRoute is how ape gets updated in this project.
type apeRoute struct {
	name  string
	bingo bool
}

func apeUpdateRoute(projectRoot string) apeRoute {
	if _, err := os.Stat(filepath.Join(projectRoot, ".bingo", "ape.mod")); err == nil {
		return apeRoute{name: "bingo, pinned in this project", bingo: true}
	}
	return apeRoute{name: "ape update"}
}

func (r apeRoute) command(version string) string {
	if r.bingo {
		return "bingo get " + apeModulePath + "@" + version
	}
	return "ape update"
}

// updateApeAndRerun updates ape by the project's route, commits a bingo
// pin change when committing, and runs the same command on the new binary.
func updateApeAndRerun(ctx context.Context, w io.Writer, route apeRoute, projectRoot string, commit bool) error {
	var newBin string
	if route.bingo {
		bin, err := updateApeViaBingo(ctx, w, route, projectRoot, commit)
		if err != nil {
			return err
		}
		newBin = bin
	} else {
		if err := runUpdate(ctx, Version, "human"); err != nil {
			return usageErrExit(exitCodeApeBelowMinimum, err)
		}
		bin, err := os.Executable()
		if err != nil {
			return usageErrExit(exitCodeApeBelowMinimum, err)
		}
		newBin = bin
	}
	return rerun(ctx, newBin)
}

// updateApeViaBingo moves the project's bingo pin to the newest final ape,
// commits the pin change on its own when committing, and returns the new
// binary's path.
func updateApeViaBingo(ctx context.Context, w io.Writer, route apeRoute, projectRoot string, commit bool) (string, error) {
	latest, err := fetchLatestVersion(ctx, os.Getenv("GITHUB_TOKEN"))
	if err != nil {
		return "", usageErrExit(exitCodeApeBelowMinimum, fmt.Errorf("cannot find the newest ape release: %w", err))
	}
	tag := "v" + trimV(latest)
	if _, err := exec.LookPath("bingo"); err != nil {
		fmt.Fprintf(os.Stderr, "run: %s\n", route.command(tag))
		return "", usageErrExit(exitCodeApeBelowMinimum, errors.New("this project pins ape with bingo, and bingo is not on PATH"))
	}
	get := exec.CommandContext(ctx, "bingo", "get", apeModulePath+"@"+tag) //nolint:gosec // the tag is GitHub's newest release, reshaped as vX.Y.Z
	get.Dir, get.Stdout, get.Stderr = projectRoot, w, os.Stderr
	if err := get.Run(); err != nil {
		return "", usageErrExit(exitCodeApeBelowMinimum, fmt.Errorf("bingo get %s@%s: %w", apeModulePath, tag, err))
	}
	bin, err := bingoBinary(ctx, "ape", tag)
	if err != nil {
		return "", usageErrExit(exitCodeApeBelowMinimum, err)
	}
	if commit {
		if err := commitPaths(ctx, projectRoot, []string{".bingo"},
			"chore(ape): update ape to "+tag+"\n\n"+generatorTrailer+"\n"); err != nil {
			return "", err
		}
		fmt.Fprintf(w, "ape: updated to %s and committed (chore(ape)); it stays committed whatever the install does next\n", tag)
	}
	return bin, nil
}

// bingoBinary is where bingo put a pinned tool: $GOBIN, else $GOPATH/bin,
// named <tool>-<version>.
func bingoBinary(ctx context.Context, tool, version string) (string, error) {
	dir := ""
	for _, v := range []string{"GOBIN", "GOPATH"} {
		out, err := exec.CommandContext(ctx, "go", "env", v).Output()
		if err != nil {
			continue
		}
		if p := strings.TrimSpace(string(out)); p != "" {
			if v == "GOPATH" {
				p = filepath.Join(strings.Split(p, string(os.PathListSeparator))[0], "bin")
			}
			dir = p
			break
		}
	}
	bin := filepath.Join(dir, tool+"-"+version)
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("bingo reported success but %s does not exist", bin)
	}
	return bin, nil
}

// rerun starts bin with this process's arguments and exits with its
// status: the re-run's outcome IS the command's outcome.
func rerun(ctx context.Context, bin string) error {
	cmd := exec.CommandContext(ctx, bin, os.Args[1:]...) //nolint:gosec // the ape binary this command just installed, with this command's own arguments
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), envFrameworkReexec+"=1")
	err := cmd.Run()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return reportedErr(exitErr.ExitCode(), err)
	}
	if err != nil {
		return usageErrExit(exitCodeApeBelowMinimum, fmt.Errorf("re-running with the updated ape (%s): %w", bin, err))
	}
	return nil
}

func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
}

func askYesNo(question string, defaultYes bool) bool {
	hint := "[y/N]"
	if defaultYes {
		hint = "[Y/n]"
	}
	fmt.Fprintf(os.Stderr, "%s %s ", question, hint)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return defaultYes
	case "y", "yes":
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------
// Commits

// commitPrecheck enforces commit mode's precondition before anything is
// written: a git repository, on a branch, with no tracked file modified or
// staged. It returns the untracked files present now, which the install
// commit must leave out.
func commitPrecheck(ctx context.Context, projectRoot string) (map[string]bool, error) {
	if !framework.IsGitRepo(ctx, projectRoot) {
		return nil, usageErrExit(exitCodeProjectSkillsModified, fmt.Errorf(
			"%s is not a git repository: ape commits the install, so it needs one (git init), or pass --no-commit", projectRoot))
	}
	if _, err := gitOut(ctx, projectRoot, "symbolic-ref", "-q", "HEAD"); err != nil {
		return nil, usageErrExit(exitCodeProjectSkillsModified, errors.New(
			"HEAD is detached: ape commits the install onto a branch, so check one out, or pass --no-commit"))
	}
	status, err := porcelain(ctx, projectRoot)
	if err != nil {
		return nil, usageErrExit(exitCodeProjectSkillsModified, err)
	}
	untracked := map[string]bool{}
	var dirty []string
	for _, e := range status {
		if e.untracked {
			untracked[e.path] = true
			continue
		}
		dirty = append(dirty, e.path)
	}
	if len(dirty) > 0 {
		sort.Strings(dirty)
		fmt.Fprintln(os.Stderr, "Error: tracked files have uncommitted changes (commit or stash them, or pass --no-commit):")
		for _, p := range dirty {
			fmt.Fprintln(os.Stderr, "  - "+p)
		}
		return nil, reportedErr(exitCodeProjectSkillsModified, errors.New("tracked files have uncommitted changes"))
	}
	return untracked, nil
}

// commitInstall stages what the install and its migrations changed — every
// tracked change (the precheck proved there were none before) and every
// untracked file that was not there before — and commits it. No change,
// no commit.
func commitInstall(ctx context.Context, w io.Writer, projectRoot string, before map[string]bool,
	subject string, meta framework.Metadata, migrations []string,
) error {
	status, err := porcelain(ctx, projectRoot)
	if err != nil {
		return err
	}
	var paths []string
	for _, e := range status {
		if e.untracked && before[e.path] {
			continue
		}
		paths = append(paths, e.path)
	}
	if len(paths) == 0 {
		fmt.Fprintln(w, "commit: nothing changed, nothing committed")
		return nil
	}
	msg := subject + "\n\nFramework-Version: " + meta.Framework.VersionTag +
		"\nFramework-Commit: " + meta.Framework.GitHash
	if len(migrations) > 0 {
		msg += "\nFramework-Migrations: " + strings.Join(migrations, ", ")
	}
	if g := meta.Governance; g != nil && g.GitHash != "" {
		msg += "\nGovernance-Version: " + g.VersionTag + "\nGovernance-Commit: " + g.GitHash
	}
	msg += "\n" + generatorTrailer + "\n"
	if err := commitPaths(ctx, projectRoot, paths, msg); err != nil {
		return err
	}
	fmt.Fprintf(w, "commit: %s\n", subject)
	return nil
}

// commitPaths stages paths and commits them with msg, running the
// project's hooks. A failed commit leaves everything staged.
func commitPaths(ctx context.Context, projectRoot string, paths []string, msg string) error {
	add := append([]string{"add", "-A", "--"}, paths...)
	if _, err := gitOut(ctx, projectRoot, add...); err != nil {
		return usageErrExit(exitCodeCommitFailed, fmt.Errorf("staging the install: %w", err))
	}
	f, err := os.CreateTemp("", "ape-commit-msg-")
	if err != nil {
		return usageErrExit(exitCodeCommitFailed, err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(msg); err != nil {
		_ = f.Close()
		return usageErrExit(exitCodeCommitFailed, err)
	}
	_ = f.Close()
	cmd := exec.CommandContext(ctx, "git", "commit", "-q", "-F", f.Name()) //nolint:gosec // a temp file ape just wrote
	cmd.Dir = projectRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		subject, _, _ := strings.Cut(msg, "\n")
		fmt.Fprintf(os.Stderr, "%s", out)
		return usageErrExit(exitCodeCommitFailed, fmt.Errorf(
			"could not commit %q (a hook failed?): the changes are staged — fix it and commit them, or reset them", subject))
	}
	return nil
}

type statusEntry struct {
	path      string
	untracked bool
}

// porcelain lists changed paths, untracked ones individually.
func porcelain(ctx context.Context, dir string) ([]statusEntry, error) {
	out, err := gitOut(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	// A porcelain v1 entry is "XY path": two status letters, a space.
	const prefix = len("XY ")
	var entries []statusEntry
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) <= prefix {
			continue
		}
		xy, path := f[:2], f[prefix:]
		entries = append(entries, statusEntry{path: path, untracked: xy == "??"})
		if xy[0] == 'R' || xy[0] == 'C' {
			i++ // the rename's source path follows
		}
	}
	return entries, nil
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}
