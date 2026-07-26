package aped

import (
	"debug/buildinfo"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

/*
The `ape` this node delivers into workspaces (PLAN-23).

A workspace's `ape` is NOT baked into the image. The image would otherwise carry
whatever release was current when it was built, and since project work happens
INSIDE workspaces, an `ape` upgrade made to unblock that work would not reach the
place the work happens until a new image shipped. So aped mounts one at runtime,
exactly as it already mounts the framework.

WHICH one: the `ape` sitting beside the running `aped`. They are built and released
together — the linux archive carries both binaries — so "the ape next to me" is the
matching version by construction, with no fetch, no pin, and no second pipeline.

HOW it is trusted: by READING the binary, never by running it. Executing a candidate
inside the rootful daemon to ask its version would be the wrong shape, and it is also
circular — a wrong binary reports whatever version it likes. Go embeds the answers:
the main package path proves it is our `ape` (a much stronger signal than any version
string, since it catches an unrelated program of the same name), GOOS/GOARCH prove it
can execute in the guest at all, and the stamped ldflags carry the version.

The guest executes this file as root inside its VM, with the shared credential and the
project mounts in reach. The VM is the security boundary, but a file that anyone can
rewrite is still not a file to hand a workspace, hence the writability check.
*/

// apeMainPath is the main package path every genuine `ape` binary reports in its build
// info. This is the identity check: a different program that happens to be named `ape`
// fails here, where a version comparison alone would have to trust its self-report.
const apeMainPath = "github.com/exoport/apex_process_ape/cmd/ape"

// apeVersionLDFlag extracts the version goreleaser stamps into the binary
// (-X …/internal/apecmd.Version=0.0.50). Read from the recorded -ldflags rather than by
// executing the binary.
var apeVersionLDFlag = regexp.MustCompile(`-X\s+\S*internal/apecmd\.Version=(\S+)`)

// ApeBinary is a verified `ape` this node can deliver into workspaces.
type ApeBinary struct {
	// Path is the binary on the host.
	Path string
	// Dir is the directory mounted into the guest at sandbox.ApeBinDest. A directory
	// rather than the file itself: directory binds are what the rest of this stack uses,
	// and it leaves room for completions later without another reserved destination.
	Dir string
	// Version is the stamped version, for reporting to operators and recording on the
	// workspace. "dev" for a local build.
	Version string
	// Revision is the vcs.revision when the build recorded one. It is what separates two
	// `dev` builds from different commits, which carry an IDENTICAL version string.
	Revision string
	// Warnings are conditions worth an operator's attention that do not justify refusing
	// to serve workspaces. The caller logs them.
	Warnings []string
}

// String renders the identity an operator needs to see in a log line.
//
// The revision is appended only when the version does not already carry it: an unstamped
// local build reports a pseudo-version that ENDS in the commit, and printing it twice
// ("0.0.50-0.20260726225711-0de99a9fd52b+0de99a9fd52b") reads as a bug in the daemon.
func (a ApeBinary) String() string {
	s := a.Path + " (" + a.Version
	if rev := shortRev(a.Revision); rev != "" && !strings.Contains(a.Version, rev) {
		s += "+" + rev
	}
	return s + ")"
}

func shortRev(rev string) string {
	const short = 12
	if len(rev) > short {
		return rev[:short]
	}
	return rev
}

// ErrNoApeBinary signals this node cannot produce an `ape` to deliver. It is fatal by
// design: a workspace with no `ape` is not a degraded workspace, it is a broken one, and
// finding out at `ape sandbox up` — or worse, as `command not found` inside the guest —
// costs far more than refusing at startup.
var ErrNoApeBinary = errors.New("aped: no deliverable ape binary")

// ResolveApeBinary locates and verifies the `ape` this node delivers.
//
// explicit overrides the default location for unusual installs; empty means "beside the
// running aped". selfVersion and selfRevision are this daemon's own build identity: a
// mismatch means the pair was installed out of step, which is exactly the staleness this
// project has been bitten by, so it is refused rather than warned about.
func ResolveApeBinary(explicit, selfVersion, selfRevision string) (ApeBinary, error) {
	path, err := apeBinaryCandidate(explicit)
	if err != nil {
		return ApeBinary{}, err
	}
	return verifyApeBinary(path, selfVersion, selfRevision)
}

// apeBinaryCandidate returns the path to check, without touching its contents.
func apeBinaryCandidate(explicit string) (string, error) {
	if p := strings.TrimSpace(explicit); p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", fmt.Errorf("%w: %s: %w", ErrNoApeBinary, p, err)
		}
		return abs, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("%w: cannot locate this aped executable: %w", ErrNoApeBinary, err)
	}
	// Resolve the daemon's own path first: an install is commonly a stable symlink onto a
	// version-stamped binary, and the sibling `ape` lives beside the TARGET, not the link.
	if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
		self = resolved
	}
	return filepath.Join(filepath.Dir(self), "ape"), nil
}

// verifyApeBinary runs every check that can be made without executing the file.
func verifyApeBinary(path, selfVersion, selfRevision string) (ApeBinary, error) {
	st, err := os.Stat(path)
	if err != nil {
		hint := ""
		if errors.Is(err, fs.ErrNotExist) {
			hint = " — aped delivers the `ape` beside it into every workspace, so both binaries " +
				"must be installed together (the release archive ships both; deploy/dev-host.sh " +
				"installs both). Override the location with --ape-binary."
		}
		return ApeBinary{}, fmt.Errorf("%w: %s: %w%s", ErrNoApeBinary, path, err, hint)
	}
	if !st.Mode().IsRegular() {
		return ApeBinary{}, fmt.Errorf("%w: %s is not a regular file", ErrNoApeBinary, path)
	}
	if st.Mode().Perm()&0o111 == 0 {
		return ApeBinary{}, fmt.Errorf("%w: %s is not executable (mode %v)", ErrNoApeBinary, path, st.Mode().Perm())
	}
	// WORLD-writable is fatal: any account on the node could rewrite what runs as root
	// inside every workspace. There is no legitimate reason for it.
	if st.Mode().Perm()&0o002 != 0 {
		return ApeBinary{}, fmt.Errorf("%w: %s is world-writable (mode %v) — it is mounted into "+
			"every workspace and executed there, so anyone on this node could choose what those "+
			"workspaces run", ErrNoApeBinary, path, st.Mode().Perm())
	}
	// GROUP-writable is only a warning. `go build` under a 002 umask emits 0775, so
	// refusing it would reject ordinary build output and make --ape-binary unusable during
	// development — which pushes people to chmod things, a worse outcome than a warning.
	// An `install -m 0755` deploy (deploy/dev-host.sh) normalizes it anyway.
	var warnings []string
	if st.Mode().Perm()&0o020 != 0 {
		warnings = append(warnings, fmt.Sprintf("%s is group-writable (mode %v): every member of "+
			"its group can change what workspaces execute", path, st.Mode().Perm()))
	}

	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return ApeBinary{}, fmt.Errorf("%w: %s carries no Go build info (%w) — a wrapper script or "+
			"a stripped/corrupt binary, not an ape release", ErrNoApeBinary, path, err)
	}
	if info.Path != apeMainPath {
		return ApeBinary{}, fmt.Errorf("%w: %s is a different program: it reports main package %q, "+
			"want %q", ErrNoApeBinary, path, info.Path, apeMainPath)
	}

	settings := buildSettings(info)
	if goos := settings["GOOS"]; goos != "" && goos != "linux" {
		return ApeBinary{}, fmt.Errorf("%w: %s is built for GOOS=%s — the guest is Linux, so it "+
			"would fail inside the VM as an exec format error", ErrNoApeBinary, path, goos)
	}
	if arch := settings["GOARCH"]; arch != "" && arch != runtime.GOARCH {
		return ApeBinary{}, fmt.Errorf("%w: %s is built for GOARCH=%s but this node is %s — "+
			"the workspace shares the node's architecture", ErrNoApeBinary, path, arch, runtime.GOARCH)
	}

	bin := ApeBinary{
		Path:     path,
		Dir:      filepath.Dir(path),
		Version:  apeBinaryVersion(info, settings),
		Revision: settings["vcs.revision"],
		Warnings: warnings,
	}
	if err := bin.matches(selfVersion, selfRevision); err != nil {
		return ApeBinary{}, err
	}
	return bin, nil
}

// matches refuses an `ape` that is not the counterpart of this `aped`.
//
// Version first, then revision — and the revision check is the one that earns its keep:
// locally built binaries both report "dev", so version equality passes between two builds
// from different commits, which is precisely how a stale binary reaches a node. When
// either side lacks vcs info (a goreleaser build may not stamp it) the comparison is
// skipped rather than guessed at; deploy/dev-host.sh's mtime warning covers that gap.
func (a ApeBinary) matches(selfVersion, selfRevision string) error {
	if selfVersion != "" && a.Version != "" && a.Version != selfVersion {
		return fmt.Errorf("%w: %s is version %s but this aped is %s — install both from the same "+
			"release (they ship in one archive); a workspace must not run an ape older than the "+
			"daemon that provisions it", ErrNoApeBinary, a.Path, a.Version, selfVersion)
	}
	if selfRevision != "" && a.Revision != "" && a.Revision != selfRevision {
		return fmt.Errorf("%w: %s was built from commit %s but this aped from %s — same version "+
			"string, different builds, which is what a partial `make build` looks like",
			ErrNoApeBinary, a.Path, shortRev(a.Revision), shortRev(selfRevision))
	}
	return nil
}

// buildSettings flattens build settings into a map for lookup.
func buildSettings(info *buildinfo.BuildInfo) map[string]string {
	out := make(map[string]string, len(info.Settings))
	for _, s := range info.Settings {
		out[s.Key] = s.Value
	}
	return out
}

// apeBinaryVersion recovers the version without running the binary: the ldflags stamp
// when goreleaser built it, else the module version, else "dev" — matching what `ape
// version` itself would report for the same build.
func apeBinaryVersion(info *buildinfo.BuildInfo, settings map[string]string) string {
	if m := apeVersionLDFlag.FindStringSubmatch(settings["-ldflags"]); len(m) == 2 {
		return strings.TrimPrefix(m[1], "v")
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return strings.TrimPrefix(v, "v")
	}
	return "dev"
}

// apeBinStageDir is the directory under the state dir holding the staged copy.
const apeBinStageDir = "apebin"

// StageApeBinary copies a verified `ape` into a directory of its OWN and returns the
// binary rebased onto it.
//
// This exists because the mount is a DIRECTORY and the directory an `ape` is installed in
// is /usr/local/bin — 48 entries on the dev node, including containerd, the Kata shims,
// buildkitd and aped itself. Mounting that into every workspace, FIRST on PATH, would not
// only expose all of it: it would shadow the image's own tooling with the host's, so a
// workspace's `bingo` and `asdf` would silently become the host's copies rather than the
// versions the image pins. The delivered binary therefore gets a directory containing
// nothing but itself.
//
// Copied rather than linked: the state dir and /usr/local/bin are usually different
// filesystems. The copy is also a feature — it pins what a workspace runs at stage time, so
// replacing the host binary cannot swap the ape under a workspace that is already running.
func StageApeBinary(stateDir string, bin ApeBinary) (ApeBinary, error) {
	if strings.TrimSpace(stateDir) == "" {
		return ApeBinary{}, fmt.Errorf("%w: no state dir to stage into", ErrNoApeBinary)
	}
	dir := filepath.Join(stateDir, apeBinStageDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ApeBinary{}, fmt.Errorf("%w: create %s: %w", ErrNoApeBinary, dir, err)
	}
	dest := filepath.Join(dir, "ape")
	if err := copyIfChanged(bin.Path, dest); err != nil {
		return ApeBinary{}, fmt.Errorf("%w: stage %s: %w", ErrNoApeBinary, bin.Path, err)
	}
	staged := bin
	staged.Path, staged.Dir = dest, dir
	return staged, nil
}

// copyIfChanged copies src to dest unless dest already matches it in size and modification
// time, and preserves the source mtime so that comparison keeps working. Size+mtime rather
// than a hash: this runs on every create, and rehashing ~50MB to discover nothing changed
// would be a cost paid for no information.
func copyIfChanged(src, dest string) error {
	si, err := os.Stat(src)
	if err != nil {
		return err
	}
	if di, derr := os.Stat(dest); derr == nil &&
		di.Size() == si.Size() && di.ModTime().Equal(si.ModTime()) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	// temp + rename, so a workspace created while this runs sees either the old binary or
	// the new one and never a half-written file.
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".ape.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil { // executed inside the guest
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chtimes(name, si.ModTime(), si.ModTime()); err != nil {
		return err
	}
	return os.Rename(name, dest)
}
