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

const credsReason = NATSCredsRel + " is the project's NATS credential: ape connects to NATS itself, and no session reads or writes it"

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
			if why := credsWord(w, cwd, root); why != "" {
				return PreToolDecision{Deny: true, Reason: "the command " + why + ": " + credsReason +
					" — read a check's own secret by its name (hot/<name>), never the folder or a pattern over it"}
			}
		}
	}
	return PreToolDecision{}
}

// credsWord says how one argument reaches the credential, "" when it does
// not.
func credsWord(w, cwd, root string) string {
	if i := strings.LastIndex(w, "="); i >= 0 && strings.HasPrefix(w, "-") {
		w = w[i+1:] // --file=hot/x
	}
	if w == "" || strings.HasPrefix(w, "-") && !strings.Contains(w, "/") {
		return ""
	}
	abs := w
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, abs)
	}
	rel, err := filepath.Rel(root, filepath.Clean(abs))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	rel = filepath.ToSlash(rel)
	switch {
	case rel == NATSCredsRel:
		return "names " + NATSCredsRel
	case rel == "hot":
		return "names the hot/ folder"
	case strings.ContainsAny(rel, "*?["):
		if ok, _ := path.Match(rel, NATSCredsRel); ok {
			return "has a pattern (" + w + ") that matches " + NATSCredsRel
		}
		if ok, _ := path.Match(rel, "hot"); ok {
			return "has a pattern (" + w + ") that matches the hot/ folder"
		}
	}
	return ""
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
