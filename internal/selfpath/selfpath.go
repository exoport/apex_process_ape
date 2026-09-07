// Package selfpath makes `ape` mean THIS ape inside anything ape starts.
//
// Two places shell out to something that then invokes `ape` by name: a
// migration entry's `check:` and `command:`, and every Claude Code session
// ape spawns, where 69 framework skill files run `ape …` lines. In both,
// `ape` resolves through PATH — so it is whatever the machine has
// installed, which need not be the binary that started the work.
//
// **This is not hypothetical and it does not announce itself.** On the
// development machine, `which -a ape` returns `~/go/bin/ape` and
// `/usr/local/bin/ape`, and a bare `ape version` reports v0.0.56. Two
// observations, both made live rather than reasoned about:
//
//   - A migration check reading
//     `ape sprint check --output-format json | jq -e '… sprint.epic_without_retro …'`
//     was answered by that v0.0.56 binary. It is NOT broken: it has the
//     command, it emits valid JSON, and its `findings` is `[]` because the
//     check class did not exist yet. So the check reported SATISFIED and
//     the migration would have been recorded applied — permanently, since
//     the ledger is never revisited — on a project that was never
//     migrated.
//   - `ape 0.0.67` spawned a session, and `ape version` inside that
//     session reported **0.0.56**.
//
// The second is the wider one: this release exists to raise the APEX
// framework's version floor, and a skill running a pre-floor binary inside
// a dispatch by the post-floor binary makes the floor unenforceable from
// the inside. A missing command would have errored and been caught; a
// coherent answer from the wrong binary is the only version of this that
// survives.
//
// The remedy, which the framework's own eval harness arrived at
// independently eight weeks earlier for the same reason
// (`apex_eval/runner.py:_pin_ape_on_path`, observing v0.0.52 on the same
// machine): put the running binary at the front of the child's PATH.
//
// The shadow directory holds exactly ONE entry, so nothing else on PATH
// changes resolution order. When it cannot be built the caller is told and
// proceeds unpinned — running against the wrong `ape` *silently* is the
// failure this exists to prevent, and a silent fallback would recreate it.
package selfpath

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Name is the command name a check, a command or a skill uses to mean
// "the binary running this".
const Name = "ape"

// FileName is Name as it appears ON DISK inside the shadow directory —
// `ape` everywhere, `ape.exe` on Windows, because that is the only form
// Windows will execute by that name.
//
// Exported because the distinction is invisible from outside and was got
// wrong the first time: `internal/apecmd` and `internal/repl` each
// asserted the pin by building filepath.Join(dir, Name), which is correct
// on POSIX and names a file that does not exist on Windows. Both passed
// locally and failed on the Windows runner, while this package's own
// tests passed — they had the suffix because they could see exeSuffix.
// One exported answer, so a consumer cannot spell it a second way.
func FileName() string { return Name + exeSuffix() }

// Pin returns env with a shadow directory prepended to PATH, in which
// Name resolves to the running executable.
//
// cleanup removes the shadow and must be called when the child that uses
// env has exited — not before, since the link is resolved at exec time.
// notice is empty on success and otherwise says why the pin could not be
// made; it is never an error, because failing to pin must not stop the
// work, only stop being silent about it.
func Pin(env []string) (pinned []string, cleanup func(), notice string) {
	exe, err := os.Executable()
	if err != nil {
		return env, func() {}, fmt.Sprintf(
			"cannot locate this binary (%v), so %q inside it resolves through PATH "+
				"and may be a different %s", err, Name, Name)
	}
	// Resolved so the shadow points at the real file rather than at a
	// symlink that may itself be repointed while the child runs.
	if resolved, rErr := filepath.EvalSymlinks(exe); rErr == nil {
		exe = resolved
	}
	dir, cleanup, err := shadow(exe)
	if err != nil {
		return env, func() {}, fmt.Sprintf(
			"cannot put this binary at the front of PATH (%v), so %q inside it may be a "+
				"different %s", err, Name, Name)
	}
	return prependPath(env, dir), cleanup, ""
}

// shadow builds a directory whose only entry is Name, pointing at target.
// A symlink first; a hard link where symlinks are unavailable, which is
// the ordinary Windows case.
func shadow(target string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "ape-selfpath-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	link := filepath.Join(dir, FileName())
	if symErr := os.Symlink(target, link); symErr != nil {
		if linkErr := os.Link(target, link); linkErr != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("symlink: %w; hard link: %w", symErr, linkErr)
		}
	}
	return dir, cleanup, nil
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// prependPath puts dir at the front of PATH in a copy of env.
//
// The variable name is matched case-insensitively because Windows spells
// it `Path`, and appending a second `PATH=` there would leave the original
// one in force — a pin that looks applied and is not.
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
