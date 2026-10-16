package apexv1

import (
	"context"
	"strings"
	"time"
)

// The rebase guard (trial decision D40, "the model never rebases"):
// `ape hook pre-tool` denies a Bash command that runs `git rebase` or
// `git merge` while the repository it runs in has a change branch,
// change/<id>, checked out. Catching a change up with its moved base is
// ape's — at `ape change start`, verify and archive, which rebase, re-take
// the baseline at the base tip and block on a conflict — and a session
// that rebased or merged by itself would leave base_sha, the baseline and
// the verify describing code the branch no longer has.
//
// Allowed: `git rebase --abort|--quit` and `git merge --abort|--quit`
// (getting out of one is always fine), every other git subcommand —
// `git merge-base`, `git merge-file`, `git merge-tree` included — and
// rebase or merge on any branch that is not a change branch. The command
// is parsed like the tag guard's (quotes, wrappers, sh -c, eval, -C), with
// the same limits. ape's own rebases run outside the session's tool calls
// and are never seen by the hook.

// RebaseGuard decides a Bash command; Deny is set for a rebase or merge
// on a change branch.
func RebaseGuard(command, cwd string) PreToolDecision {
	if !strings.Contains(command, "git") || (!strings.Contains(command, "rebase") && !strings.Contains(command, "merge")) {
		return PreToolDecision{}
	}
	if what := scanGit(command, cwd, 0, judgeRebase); what != "" {
		return PreToolDecision{Deny: true, Reason: what}
	}
	return PreToolDecision{}
}

// judgeRebase is the rebase guard's judge.
func judgeRebase(sub string, args []string, dir string) string {
	if sub != "rebase" && sub != "merge" {
		return ""
	}
	for _, a := range args {
		if a == "--abort" || a == "--quit" {
			return ""
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	branch := CurrentBranch(ctx, dir)
	id := ChangeOfBranch(branch)
	if id == "" {
		return ""
	}
	return "`git " + sub + "` on " + branch + ": ape, never the session, brings a change up to date with its base — " +
		"`ape change start " + id + "`, `ape change verify " + id + "` and `ape change archive " + id +
		"` rebase it onto its moved base and re-take the baseline (a conflict blocks the change for a person)"
}
