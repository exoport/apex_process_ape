package apexv1

import (
	"os"
	"path/filepath"
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
