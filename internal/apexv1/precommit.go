package apexv1

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Q89 (rc.6e): a commit a model makes in its own session (or a person
// makes) is not ape's, so ScanCommit never saw it; only the archive
// squash did, at the latest. `ape project init` and the lift install a
// git pre-commit hook that runs `ape hook pre-commit`: the commit scan,
// on the staged added lines — the same rules, the guard's baseline allow
// list (R23) and the same refusal (commit.secret). The hooks folder is
// git's own answer (`git rev-parse --git-path hooks`): core.hooksPath when
// set, the common .git/hooks in a linked worktree. A hook that is not
// ape's is never touched: project check's notice hooks.pre-commit-absent
// names the line to add to it, and the same notice reports a fresh clone,
// whose .git/hooks git never versions (`ape project init` installs it).
// `git commit --no-verify` skips it: the person's escape; the archive's
// scan stays the backstop.

const (
	// HookCmdPreCommit is the command ape's pre-commit hook runs: a bare
	// `ape`, resolved on PATH when git runs the hook, like ape's other
	// hooks.
	HookCmdPreCommit = "ape hook pre-commit"
	// EnvScanned=1 skips the pre-commit scan: ape sets it on its own
	// commits that run hooks (the archive squash), which ScanCommit has
	// scanned already. The hook script checks it first, so no ape starts.
	EnvScanned = "APE_SCANNED"
	// preCommitMarker is the line that makes a pre-commit hook ape's: one
	// without it is never written.
	preCommitMarker = "# ape: pre-commit secret scan"
)

// preCommitScript is the hook ape installs.
const preCommitScript = "#!/bin/sh\n" +
	preCommitMarker + " (apex-v1; installed by `ape project init`, which never edits a hook without this line)\n" +
	`[ "$` + EnvScanned + `" = 1 ] && exit 0` + "\n" +
	`command -v ape >/dev/null 2>&1 || { echo "pre-commit: ape is not on PATH, so the APEX secret scan cannot run — put ape on PATH, or skip the scan with git commit --no-verify" >&2; exit 1; }` + "\n" +
	"exec " + HookCmdPreCommit + "\n"

// What init and the lift did with the pre-commit hook (PreCommitHook.Status).
const (
	PreCommitInstalled = "installed"
	PreCommitPresent   = "present"
	PreCommitKept      = "kept"
)

// PreCommitHook is what init or the lift did with git's pre-commit hook.
type PreCommitHook struct {
	// Path is the hook, relative to the project when inside it (as
	// .git/hooks/pre-commit), else absolute.
	Path string `json:"path"`
	// Status: installed (ape wrote it now), present (ape's was there
	// already), or kept (another hook was there: not touched, see
	// RunsApe).
	Status string `json:"status"`
	// RunsApe: the hook runs `ape hook pre-commit` — always for ape's
	// own (installed, present); for a kept one, when it already calls it.
	// Always emitted, as init's help says.
	RunsApe bool `json:"runs_ape"`
}

// preCommitHookPath is where git looks for the pre-commit hook of the
// repository at root (core.hooksPath and worktrees honoured), absolute;
// "" outside a git work tree.
func (p *Project) preCommitHookPath(ctx context.Context) string {
	if !p.isWorkTree(ctx) {
		return ""
	}
	path, err := git(ctx, p.Root, "rev-parse", "--path-format=absolute", "--git-path", "hooks/pre-commit")
	if err != nil || path == "" {
		return ""
	}
	return filepath.FromSlash(path)
}

// hookRel is a hook path as init and the notice show it.
func (p *Project) hookRel(path string) string {
	if r, err := filepath.Rel(p.Root, path); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return path
}

// runsApePreCommit reports whether a hook's text runs `ape hook
// pre-commit` on a line that is not a comment.
func runsApePreCommit(doc string) bool {
	for l := range strings.SplitSeq(doc, "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "#") && strings.Contains(l, HookCmdPreCommit) {
			return true
		}
	}
	return false
}

// executable reports whether git would run the hook file: on Windows any
// file, elsewhere one with an execute bit.
func executable(st os.FileInfo) bool {
	return runtime.GOOS == "windows" || st.Mode()&0o111 != 0
}

// installPreCommitHook installs ape's pre-commit hook where git looks for
// it, when no hook is there; nil outside a git work tree. Ape's own hook
// is left as it is (made executable again if it lost the bit), and any
// other hook is never touched.
func (p *Project) installPreCommitHook(ctx context.Context) (*PreCommitHook, error) {
	path := p.preCommitHookPath(ctx)
	if path == "" {
		return nil, nil //nolint:nilnil // no git work tree: no hook
	}
	h := &PreCommitHook{Path: p.hookRel(path)}
	data, err := os.ReadFile(path)
	switch {
	case err == nil && strings.Contains(string(data), preCommitMarker):
		h.Status, h.RunsApe = PreCommitPresent, true
		if st, err := os.Stat(path); err == nil && !executable(st) {
			if err := os.Chmod(path, 0o755); err != nil {
				return nil, err
			}
		}
		return h, nil
	case err == nil:
		h.Status, h.RunsApe = PreCommitKept, runsApePreCommit(string(data))
		return h, nil
	case !errors.Is(err, os.ErrNotExist):
		// A folder, a dangling link, an unreadable file: someone's.
		h.Status = PreCommitKept
		return h, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, preCommitScript); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o755); err != nil {
		return nil, err
	}
	h.Status, h.RunsApe = PreCommitInstalled, true
	return h, nil
}

// preCommitCommitPath is the installed hook's project-relative path when
// it belongs in init's commit: inside the project (a core.hooksPath such
// as .githooks, which the project versions) and not ignored; "" for
// .git/hooks and anything git would not stage.
func (p *Project) preCommitCommitPath(ctx context.Context, h *PreCommitHook) string {
	if h == nil || h.Status != PreCommitInstalled || filepath.IsAbs(h.Path) {
		return ""
	}
	if top, err := git(ctx, p.Root, "rev-parse", "--path-format=absolute", "--git-dir"); err == nil {
		if r, err := filepath.Rel(filepath.FromSlash(top), filepath.Join(p.Root, filepath.FromSlash(h.Path))); err == nil && !strings.HasPrefix(r, "..") {
			return "" // inside .git: never versioned
		}
	}
	if len(gitIgnored(ctx, p.Root, true, h.Path)) > 0 {
		return ""
	}
	return h.Path
}

// preCommitNotices is project check's notice when git's pre-commit hook
// does not run ape's scan (Q89): it is absent (a fresh clone), another
// hook that does not run `ape hook pre-commit`, or one that does but git
// skips as not executable. None outside a git work tree.
func (p *Project) preCommitNotices(ctx context.Context) []Diagnostic {
	path := p.preCommitHookPath(ctx)
	if path == "" {
		return nil
	}
	rel := p.hookRel(path)
	const unscanned = ": a commit ape does not make (a session's own git commit, a person's) is not scanned for secrets until the archive"
	var msg string
	data, err := os.ReadFile(path)
	st, serr := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist) && errors.Is(serr, os.ErrNotExist):
		msg = rel + " is absent (git does not version it, so a fresh clone has none)" + unscanned + " — `ape project init` installs ape's"
	case err != nil:
		msg = rel + " is not a file ape can read" + unscanned + " — make it a hook that runs `" + HookCmdPreCommit + "` (ape never replaces a hook it did not write)"
	case !strings.Contains(string(data), preCommitMarker) && !runsApePreCommit(string(data)):
		msg = rel + " is another tool's hook and does not run ape's scan" + unscanned + " — add the line `" + HookCmdPreCommit + " || exit 1` to it (ape never edits a hook it did not write)"
	case serr == nil && !executable(st):
		msg = rel + " runs ape's scan but is not executable, so git skips it" + unscanned + " — `chmod +x " + rel + "`"
	default:
		return nil
	}
	return []Diagnostic{notice(Diagnostic{Code: CodeHooksPreCommitAbsent, File: rel, Message: msg})}
}

// PreCommitHookNotice is the hooks.pre-commit-absent notice, or nil when
// git's pre-commit hook runs ape's scan (or there is no work tree): what
// `ape doctor` reports.
func (p *Project) PreCommitHookNotice(ctx context.Context) *Diagnostic {
	if ns := p.preCommitNotices(ctx); len(ns) > 0 {
		return &ns[0]
	}
	return nil
}

// PreCommitScan is `ape hook pre-commit`: the commit scan of what git is
// about to commit — the staged added lines, against HEAD (ScanCommit), in
// the index git hands its hooks (GIT_INDEX_FILE, a temporary one for
// `git commit -a` or `git commit <paths>`). nil when EnvScanned is set,
// or dir is in no v1 project; a hit is commit.secret, which the hook
// turns into exit 1, so git commits nothing.
func PreCommitScan(ctx context.Context, dir string) error {
	if os.Getenv(EnvScanned) == "1" {
		return nil
	}
	p, err := Open(FindRoot(dir))
	if err != nil || !p.IsV1() || !p.isWorkTree(ctx) {
		return nil //nolint:nilerr // not a v1 project: nothing of ape's to scan for
	}
	err = ScanCommit(ctx, p.Root, "this commit")
	if fe, ok := errors.AsType[*FailError](err); ok && fe.Code == CodeCommitSecret {
		fe.Msg += "; its files stay staged: take the secret out, `git add` them again and commit again"
	}
	return err
}
