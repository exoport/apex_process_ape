package migration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ShellRunner runs a line through the platform shell.
//
// A shell is required rather than an argv split: the framework's own
// entries pipe into `jq` and quote embedded JSON, and splitting those on
// whitespace would silently run something else.
//
// Env, when non-empty, replaces the child's environment. It is how the
// self-shadow below reaches the child; an empty Env inherits this
// process's, which is correct for a caller that has not built one.
type ShellRunner struct {
	Env []string
}

func (r ShellRunner) Run(ctx context.Context, dir, line string) (int, error) {
	name, flag := "sh", "-c"
	if runtime.GOOS == "windows" {
		name, flag = "cmd", "/c"
	}
	cmd := exec.CommandContext(ctx, name, flag, line)
	cmd.Dir = dir
	if len(r.Env) > 0 {
		cmd.Env = r.Env
	}
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		// The command ran and said no. That is a verdict, not a failure
		// to obtain one.
		return exitErr.ExitCode(), nil
	}
	return -1, err
}

// refusingRunner is what `--plan --no-check` uses: every check reports
// not-run rather than a verdict nobody obtained.
type refusingRunner struct{}

func (refusingRunner) Run(context.Context, string, string) (int, error) {
	return -1, errors.New("checks disabled")
}

// NoCheckRunner returns a Runner that executes nothing.
func NoCheckRunner() Runner { return refusingRunner{} }

// SelfName is the command name a migration's `check:` and `command:` use
// to mean "the binary running this migration".
const SelfName = "ape"

// NewShellRunner builds a runner in whose environment `ape` resolves to
// the RUNNING binary, and returns a cleanup plus a notice when it could
// not arrange that.
//
// Why this exists, found by running the framework's own first migration
// entry against a real machine. Its check is
//
//	ape sprint check --output-format json | jq -e '[.findings[] | select(.check=="sprint.epic_without_retro")] | length == 0'
//
// and `ape` there resolves through PATH. On a machine with an older `ape`
// installed — v0.0.56 was sitting in ~/go/bin — that older binary answers
// the question. It has `sprint check --output-format json`, it emits
// perfectly valid JSON, and its `findings` is `[]` because the check class
// did not exist yet. `jq` therefore says `true`, the check reports
// SATISFIED, the runner records the entry as applied, and the ledger makes
// that permanent. The project never gets its retrospective rows and
// nothing ever says so again.
//
// That is the same defect shape as every other one this release turned up:
// the check was there, it ran, it reported success, and it was measuring a
// stand-in rather than the thing. A check that names `ape` can only
// coherently mean the `ape` deciding whether to apply the migration.
//
// The shadow is a directory containing ONE entry, a link named `ape`, so
// nothing else on PATH changes resolution order. When it cannot be built —
// symlinks unavailable, a cross-device hard link — the runner is returned
// without it and the notice says so, because running with the wrong `ape`
// silently is the failure this exists to prevent and hiding the fallback
// would recreate it.
func NewShellRunner() (runner ShellRunner, cleanup func(), notice string) {
	exe, err := os.Executable()
	if err != nil {
		return ShellRunner{}, func() {}, fmt.Sprintf(
			"cannot locate this binary (%v), so a check naming %q resolves through PATH "+
				"and may be answered by a different %s", err, SelfName, SelfName)
	}
	if resolved, rErr := filepath.EvalSymlinks(exe); rErr == nil {
		exe = resolved
	}
	dir, cleanup, err := selfShadow(exe)
	if err != nil {
		return ShellRunner{}, func() {}, fmt.Sprintf(
			"cannot shadow %q with this binary (%v), so a check naming it resolves through PATH "+
				"and may be answered by a different %s", SelfName, err, SelfName)
	}
	return ShellRunner{Env: prependPath(os.Environ(), dir)}, cleanup, ""
}

// selfShadow builds a directory whose only entry is `ape`, pointing at
// target. A symlink first; a hard link where symlinks are unavailable,
// which is the ordinary Windows case.
func selfShadow(target string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "ape-migration-shadow-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	name := SelfName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	link := filepath.Join(dir, name)
	if symErr := os.Symlink(target, link); symErr != nil {
		if linkErr := os.Link(target, link); linkErr != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("symlink: %w; hard link: %w", symErr, linkErr)
		}
	}
	return dir, cleanup, nil
}

// prependPath puts dir at the front of PATH in a copy of env.
//
// The variable name is matched case-insensitively because Windows spells
// it `Path`, and appending a second `PATH=` entry there would leave the
// original one in force.
func prependPath(env []string, dir string) []string {
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		key, value, found := strings.Cut(kv, "=")
		if found && strings.EqualFold(key, "PATH") {
			out = append(out, key+"="+dir+string(os.PathListSeparator)+value)
			replaced = true
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, "PATH="+dir)
	}
	return out
}
