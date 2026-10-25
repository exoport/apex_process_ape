package apexv1

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// The push and kill guards: `ape hook pre-tool` denies, besides tag
// writes (TagGuard) and rebases on a change branch (RebaseGuard):
//
//	git push, in any form (D45, H3): a push publishes, and publishing is
//	the person's job. With push.followTags set — as it is on the
//	operator's machines — even `git push origin main` publishes every
//	reachable unpushed annotated tag, so the tag guard alone cannot keep
//	tags in. EnvAllowPush=1 lets a session push; a tag push still needs
//	EnvAllowTag=1 as well.
//
//	pkill, killall, killall5 and skill, in any form (D46, H40): they stop
//	processes by name or pattern, which hits processes the session did
//	not start — another session's, an eval runner's, the person's own.
//	`kill <pid>` stays allowed. EnvAllowKill=1 lets a session use them.
//
// Both are parsed like the tag guard (quotes, && || ; | chains, subshells,
// NAME=value prefixes, the wrappers env/sudo/doas/xargs/timeout/nice/…,
// absolute paths, git -C/-c, sh -c, eval and env -S), with the same
// limits: a script file, an alias or a program run through a variable is
// not seen.
//
// Overrides. Every APE_ALLOW_* counts only from the hook process's own
// environment: the environment the person launched the session (or ape
// task) with, which Claude Code hands to its hooks. A command cannot set
// it for itself — `APE_ALLOW_PUSH=1 git push`, `env APE_ALLOW_PUSH=1 git
// push` and `export APE_ALLOW_PUSH=1; git push` change the environment of
// what the command runs, never the hook's, and the parser skips such
// assignments, so the command is judged as if they were not there (D47
// Q6: no override from inside a model session).

// EnvAllowPush lets a session run git push.
const EnvAllowPush = "APE_ALLOW_PUSH"

// EnvAllowKill lets a session run pkill, killall, killall5 and skill.
const EnvAllowKill = "APE_ALLOW_KILL"

// overrideSet reports whether the person who launched the session set
// name=1 (see Overrides above).
func overrideSet(name string) bool { return os.Getenv(name) == "1" }

// inlineOverrideNote is added to a deny reason whose command tried to set
// an override itself.
func inlineOverrideNote(command string) string {
	if !strings.Contains(command, "APE_ALLOW_") {
		return ""
	}
	return " An APE_ALLOW_* set inside the command (VAR=1 cmd, env, export) does not count: only the environment the person launched the session with does."
}

const pushGuardReason = "only a person pushes: %s publishes to a remote (and, with push.followTags, every reachable unpushed tag) — " +
	"leave the push to the human (ask them to run it). A session they started with " + EnvAllowPush + "=1 may push."

// PushGuard decides a Bash command; Deny is set for any git push.
func PushGuard(command, cwd string) PreToolDecision {
	if overrideSet(EnvAllowPush) {
		return PreToolDecision{}
	}
	what := scanGit(command, cwd, 0, func(sub string, args []string, _ string) string {
		if sub != "push" {
			return ""
		}
		return "`" + strings.TrimSpace("git push "+strings.Join(args, " ")) + "`"
	})
	if what == "" {
		return PreToolDecision{}
	}
	return PreToolDecision{Deny: true, Reason: strings.Replace(pushGuardReason, "%s", what, 1) + inlineOverrideNote(command)}
}

// killByName are the programs that stop processes by name, pattern, user
// or terminal rather than by a PID the session holds. killall5 signals
// every process; skill is procps' kill-by-user/tty/command.
var killByName = map[string]bool{"pkill": true, "killall": true, "killall5": true, "skill": true}

const killGuardReason = "%s stops processes by name or pattern, which can stop processes this session did not start " +
	"(another session's, an eval runner's, the person's own) — stop the process you started by its PID (kill <pid>). " +
	"A session the person started with " + EnvAllowKill + "=1 may use pkill/killall."

// KillGuard decides a Bash command; Deny is set for pkill, killall,
// killall5 or skill in any form.
func KillGuard(command, cwd string) PreToolDecision {
	if overrideSet(EnvAllowKill) {
		return PreToolDecision{}
	}
	what := scanCmds(command, cwd, 0, func(words []string, _ string) string {
		if !killByName[filepath.Base(words[0])] {
			return ""
		}
		return "`" + strings.Join(words, " ") + "`"
	})
	if what == "" {
		return PreToolDecision{}
	}
	return PreToolDecision{Deny: true, Reason: strings.Replace(killGuardReason, "%s", what, 1) + inlineOverrideNote(command)}
}

// The E2 records guard (T6): the person's prompts and pre-tool's grants
// are what a person act rests on, so a model never writes them. Denied: a
// Bash command naming _apex/state/prompts or _apex/state/grants (an edit
// tool there is denied with the rest of _apex/state/), and one running the
// plumbing that writes them or acts on them — `ape hook user-prompt`
// (Claude Code runs it on the person's prompt), `ape hook pre-tool` (it
// runs before every tool call) and `ape __run` (a long run's worker, which
// ape starts). A same-UID program that writes them without naming them is
// the documented residual.

var recordPathRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])state/+(?:\./+)*(prompts|grants)(?:$|[^A-Za-z0-9_-])`)

const recordGuardReason = "%s: the person's recorded prompts and pre-tool's grants are what a person act rests on (E2), " +
	"so only Claude Code's hooks and ape write them, and only ape and Claude Code run its hook and worker plumbing — " +
	"to have a person act done, ask the person to type its phrase (`ape hook pre-tool --help`)"

// RecordGuard decides a Bash command; Deny is set for one that names the
// E2 records or runs their plumbing.
func RecordGuard(command, cwd string) PreToolDecision {
	if m := recordPathRe.FindStringSubmatch(command); m != nil {
		return PreToolDecision{Deny: true, Reason: strings.Replace(recordGuardReason, "%s", "the command names _apex/state/"+m[1], 1)}
	}
	if !strings.Contains(command, "ape") {
		return PreToolDecision{}
	}
	what := scanCmds(command, cwd, 0, func(words []string, _ string) string {
		if base := filepath.Base(words[0]); base != "ape" && base != "ape.exe" {
			return ""
		}
		args := words[1:]
		switch {
		case len(args) >= 1 && args[0] == "__run":
			return "`ape __run` is a long run's worker"
		case len(args) >= 2 && args[0] == "hook" && (args[1] == "user-prompt" || args[1] == "pre-tool"):
			return "`ape hook " + args[1] + "` is Claude Code's hook"
		}
		return ""
	})
	if what == "" {
		return PreToolDecision{}
	}
	return PreToolDecision{Deny: true, Reason: strings.Replace(recordGuardReason, "%s", what, 1)}
}

// The gate guard (v1.4, D541, D543 B2): a session runs a gate's plain
// <dev>/gates/<M>-<slug>/gate.sh only through ape — `ape milestone
// gate-test <folder>` before the milestone is registered, `ape milestone
// gate <M>` after — which run it in its own process group and end what it
// leaves running. Run from a session's Bash tool it is outside that, and a
// gate whose lifecycle code lost its server's PID left the server running
// (taskforge M4, v1.3 row 3). The stub run, `gate.sh --stub <file>`, starts
// no server (apex-plan: "the gate skips building, starting a server and
// calling earlier gates") and stays allowed. Parsed like the other guards
// (wrappers such as timeout and env, `sh -c`, `bash <script>`), with the
// same limits: a script run through a variable, a make target or `source`
// is not seen. EnvAllowGate=1 lets a session run it.

// EnvAllowGate lets a session run a gate's plain gate.sh.
const EnvAllowGate = "APE_ALLOW_GATE"

const gateGuardReason = "%s runs a gate outside ape, so nothing ends what it starts (a server whose PID the gate lost outlives it) — " +
	"run it through ape, which reaps it: `ape milestone gate-test %s` (stub and plain runs, before the milestone exists) or " +
	"`ape milestone gate <M>` once it does; `gate.sh --stub <file>` alone may run directly. A session the person started with " + EnvAllowGate + "=1 may run it."

// GateGuard decides a Bash command; Deny is set for a plain run of a gate
// folder's gate.sh.
func GateGuard(command, cwd string) PreToolDecision {
	if overrideSet(EnvAllowGate) || !strings.Contains(command, "gate.sh") {
		return PreToolDecision{}
	}
	folder := ""
	what := scanCmds(command, cwd, 0, func(words []string, dir string) string {
		script, args := words[0], words[1:]
		switch filepath.Base(script) {
		case "sh", "bash", "zsh", "dash", "ksh":
			i := 0
			for i < len(args) && strings.HasPrefix(args[i], "-") {
				if o := args[i]; !strings.HasPrefix(o, "--") && strings.Contains(o, "n") {
					return "" // `bash -n`: parsed, not run
				}
				i++
			}
			if i >= len(args) {
				return ""
			}
			script, args = args[i], args[i+1:]
		}
		if filepath.Base(script) != "gate.sh" {
			return ""
		}
		for _, a := range args {
			if a == "--stub" || strings.HasPrefix(a, "--stub=") {
				return ""
			}
		}
		abs := script
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(dir, abs)
		}
		root := FindRoot(dir)
		dev := DefaultDevFolder
		if p, err := Open(root); err == nil {
			dev = p.DevRel
		}
		rel, err := filepath.Rel(filepath.Join(root, filepath.FromSlash(dev), "gates"), filepath.Clean(abs))
		if err != nil || strings.HasPrefix(rel, "..") || strings.Count(filepath.ToSlash(rel), "/") != 1 || path.Dir(filepath.ToSlash(rel)) == "lib" {
			return "" // not a gate folder's own gate.sh (<dev>/gates/lib/ is the shared library)
		}
		folder = path.Join(dev, "gates", path.Dir(filepath.ToSlash(rel)))
		return "`" + strings.Join(words, " ") + "`"
	})
	if what == "" {
		return PreToolDecision{}
	}
	return PreToolDecision{Deny: true, Reason: fmt.Sprintf(gateGuardReason, what, folder) + inlineOverrideNote(command)}
}
