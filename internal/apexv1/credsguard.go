package apexv1

import (
	"path"
	"path/filepath"
	"strings"
)

// NATSCredsRel is the project's NATS credential (v1.5, OQ4): an
// ensemble-scoped JWT in the git-ignored hot/ folder. ape connects with it;
// no session ever reads it, and no session's environment carries it.
const NATSCredsRel = "hot/ape-nats.creds" //nolint:gosec // a path, not a credential

// GitHubTokenRel is the project's GitHub token (v1.5 stage 1a): a
// fine-grained token for the coordinated repository, which ape uses to
// open and enqueue its pull requests. Kept like the NATS credential.
const GitHubTokenRel = "hot/github.token" //nolint:gosec // a path, not a credential

// ApeSecrets are the secrets ape keeps in hot/ for itself: no session tool
// reads or writes them, and no session's environment carries them.
var ApeSecrets = []string{NATSCredsRel, GitHubTokenRel}

// CredsDenyRules are the permissions.deny rules `ape project init` merges
// into .claude/settings.json and every v1 session gets inline (v1.5 Q1,
// D654+, stage 1a): Claude Code applies a Read rule to Read, Grep and
// Glob, so no session tool reads ape's secrets, bypass mode included
// (measured live, check-coord-live). The forms, for each secret: the
// project root's file, and the file under any folder a session starts in.
var CredsDenyRules = credsDenyRules()

func credsDenyRules() []string {
	out := make([]string, 0, 3*len(ApeSecrets))
	for _, s := range ApeSecrets {
		out = append(out, "Read(./"+s+")", "Read(/"+s+")", "Read(**/"+s+")")
	}
	return out
}

// secretReason is why a session may not touch secret.
func secretReason(secret string) string {
	what := "the project's NATS credential: ape connects to NATS itself"
	if secret == GitHubTokenRel {
		what = "the project's GitHub token: ape opens and enqueues its pull requests itself"
	}
	return secret + " is " + what + ", and no session reads or writes it"
}

// CredsGuard denies a Bash or Monitor command that could read the NATS
// credential (v1.5 R-1): one naming the file, naming `hot` as a directory
// (`cat hot/*`, `grep -r . hot`, `tar c hot`), or a glob that matches the
// file. A check reading its own hot/<secret> by name is allowed: projects
// keep a dev stack's secrets there. A script that reads the file is not
// seen (the SEC-1 gap; docs/explanation/security-model.md).
func CredsGuard(command, cwd string) PreToolDecision {
	if !strings.Contains(command, "hot") && !strings.ContainsAny(command, "*?[") {
		return PreToolDecision{}
	}
	root := FindRoot(cwd)
	for _, words := range shellCommands(command) {
		for _, w := range pathArgs(words) {
			if why, secret := credsWord(w, cwd, root); why != "" {
				return PreToolDecision{Deny: true, Reason: "the command " + why + ": " + secretReason(secret) +
					" — read a check's own secret by its name (hot/<name>), never the folder or a pattern over it"}
			}
		}
	}
	return PreToolDecision{}
}

// credsWord says how one argument reaches one of ape's secrets, and
// which; "" when it reaches none.
func credsWord(w, cwd, root string) (why, secret string) {
	if i := strings.LastIndex(w, "="); i >= 0 && strings.HasPrefix(w, "-") {
		w = w[i+1:] // --file=hot/x
	}
	w = strings.TrimPrefix(w, "@") // curl -d @hot/x, httpie's @file: a file's contents
	if w == "" || strings.HasPrefix(w, "-") && !strings.Contains(w, "/") {
		return "", ""
	}
	abs := w
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, abs)
	}
	rel, err := filepath.Rel(root, filepath.Clean(abs))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ""
	}
	rel = filepath.ToSlash(rel)
	if rel == "hot" {
		return "names the hot/ folder", NATSCredsRel
	}
	for _, sec := range ApeSecrets {
		switch {
		case rel == sec:
			return "names " + sec, sec
		case strings.ContainsAny(rel, "*?["):
			if ok, _ := path.Match(rel, sec); ok {
				return "has a pattern (" + w + ") that matches " + sec, sec
			}
		}
	}
	if strings.ContainsAny(rel, "*?[") {
		if ok, _ := path.Match(rel, "hot"); ok {
			return "has a pattern (" + w + ") that matches the hot/ folder", NATSCredsRel
		}
	}
	return "", ""
}

// pathArgs are a command's arguments that can name a file it reads: all
// of them, except a grep's search pattern (`grep -r hot internal/`
// searches for "hot"; `grep -r . hot` searches the folder) and echo's or
// printf's, which are text.
func pathArgs(words []string) []string {
	w := bareCommand(words)
	if len(w) == 0 {
		return nil
	}
	switch filepath.Base(w[0]) {
	case "echo", "printf":
		return nil
	case "grep", "egrep", "fgrep", "rg":
	default:
		return words[1:]
	}
	pat, ok := grepPattern(w)
	if !ok {
		return w[1:]
	}
	out := make([]string, 0, len(w))
	dropped := false
	for _, a := range w[1:] {
		if !dropped && a == pat {
			dropped = true
			continue
		}
		out = append(out, a)
	}
	return out
}
