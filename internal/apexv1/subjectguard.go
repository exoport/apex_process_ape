package apexv1

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The subject guard (Q156, D331): `ape hook pre-tool` denies a Bash
// command that would make a commit whose subject starts with `apex:`.
// That prefix marks ape's own records: the provenance checks
// (config.changed, spec.edited, state.forged) exempt such a commit,
// `apex: project init` and `apex: lift` start the project's v1 epoch,
// and `apex: config accepted` is a person's acceptance. ape writes them
// itself, never through a session's Bash tool call, so no session needs
// to, and there is no override.
//
// What is denied (the subject read as git would store it, trimmed,
// case-insensitive):
//
//	git commit -m|--message <msg> (every -m, joined as git joins them)
//	git commit -F|--file <file>   (ape reads the file; -F - is stdin,
//	                               which ape cannot read, and is denied)
//	git commit -C|-c|--reuse-message|--reedit-message <commit>
//	git commit --amend            (no new message: HEAD's subject)
//	git commit -t|--template <file>
//	git merge -m|-F               (the merge commit's message)
//	git cherry-pick <commits>     (any picked commit's subject; -n allowed)
//	git commit-tree -m|-F
//	git am <mbox>                 (a patch's Subject:; stdin is denied)
//
// A message given as "$(cat <<'EOF' … EOF)" is read from its here-doc. Any
// other command substitution in a message is denied when `apex:` appears
// anywhere in it, since ape cannot know what it prints first.
//
// It is parsed like the tag rule, with the same limits (a script file, a
// git alias, a substitution ape cannot read), and also does not see a
// `rebase --exec` string or history plumbing (fast-import, filter-branch,
// replace).

const subjectGuardReason = "%s — a commit subject starting `apex:` marks ape's own record: the provenance checks " +
	"exempt it, `apex: project init` and `apex: lift` start the project's v1 epoch, and `apex: config accepted` is a " +
	"person's acceptance. Only ape writes one; commit with your own subject (the change's type: `feat: …`, `fix: …`)."

// SubjectGuard decides a Bash command; Deny is set for a commit whose
// subject would start `apex:`.
func SubjectGuard(command, cwd string) PreToolDecision {
	if !strings.Contains(command, "git") {
		return PreToolDecision{}
	}
	if what := scanGit(command, cwd, 0, judgeSubject); what != "" {
		return PreToolDecision{Deny: true, Reason: strings.Replace(subjectGuardReason, "%s", what, 1)}
	}
	return PreToolDecision{}
}

// ReservedSubject reports whether a commit subject is ape's own (`apex:`).
func ReservedSubject(subject string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(subject)), "apex:")
}

// judgeSubject is the subject guard's judge.
func judgeSubject(sub string, args []string, dir string) string {
	switch sub {
	case "commit", "commit-tree", "merge":
		return messageVerdict(sub, args, dir)
	case "cherry-pick":
		return cherryPickVerdict(args, dir)
	case "am":
		return amVerdict(args, dir)
	}
	return ""
}

// longValueOpts are long options of commit, commit-tree and merge that
// take the next word as their value and are not a message source.
var longValueOpts = map[string]bool{
	"--author": true, "--date": true, "--trailer": true, "--cleanup": true, "--pathspec-from-file": true,
	"--fixup": true, "--strategy": true, "--strategy-option": true, "--into-name": true,
}

// shortValue are the short options that take a value, per subcommand:
// the message sources, and the others whose value must be skipped.
var shortValue = map[string]string{
	"commit":      "mFCct",
	"commit-tree": "mFpS",
	"merge":       "mFsX",
}

// messageArgs are the message sources of a commit, commit-tree or merge.
type messageArgs struct {
	msgs                   []string
	file, reuse, template  string
	amend, stdin, noCommit bool
}

// take records one message option's value.
func (m *messageArgs) take(sub, opt, v string) {
	switch opt {
	case "m", "--message":
		m.msgs = append(m.msgs, v)
	case "F", "--file":
		m.file, m.stdin = v, v == "-"
	case "C", "c", "--reuse-message", "--reedit-message":
		if sub == "commit" {
			m.reuse = v
		}
	case "t", "--template":
		if sub == "commit" {
			m.template = v
		}
	}
}

// parseMessageArgs reads the message sources from a commit, commit-tree
// or merge invocation's arguments.
func parseMessageArgs(sub string, args []string) messageArgs {
	var m messageArgs
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case a == "--":
			return m
		case strings.HasPrefix(a, "--"):
			m.long(sub, a, next)
		case strings.HasPrefix(a, "-") && len(a) > 1:
			// A short-option cluster (-am "msg", -mfoo, -sm "msg"): the first
			// option that takes a value takes the rest of the cluster, or
			// the next word.
			cluster := a[1:]
			for j := range len(cluster) {
				c := string(cluster[j])
				if !strings.Contains(shortValue[sub], c) {
					continue
				}
				v := cluster[j+1:]
				if v == "" {
					v = next()
				}
				m.take(sub, c, v)
				break
			}
		}
	}
	return m
}

// long reads one long option.
func (m *messageArgs) long(sub, a string, next func() string) {
	name, v, eq := strings.Cut(a, "=")
	switch name {
	case "--message", "--file", "--reuse-message", "--reedit-message", "--template":
		if !eq {
			v = next()
		}
		m.take(sub, name, v)
	case "--amend":
		m.amend = true
	case "--no-commit", "--abort", "--quit":
		m.noCommit = true
	case "--squash":
		if sub == "merge" {
			m.noCommit = true
		} else if !eq {
			next()
		}
	default:
		if longValueOpts[name] && !eq {
			next()
		}
	}
}

// messageVerdict judges git commit, commit-tree and merge.
func messageVerdict(sub string, args []string, dir string) string {
	m := parseMessageArgs(sub, args)
	if m.noCommit {
		return ""
	}
	cmd := "`git " + sub + "`"
	switch {
	case len(m.msgs) > 0:
		if messageReserved(strings.Join(m.msgs, "\n\n")) {
			return cmd + " with a message whose subject starts `apex:`"
		}
	case m.stdin:
		return cmd + " -F - reads its message from stdin, which ape cannot check — give the message with -m"
	case m.file != "":
		if b, err := os.ReadFile(inDir(dir, m.file)); err == nil && messageReserved(string(b)) {
			return cmd + " -F " + m.file + ", whose subject starts `apex:`"
		}
	case m.reuse != "":
		if ReservedSubject(commitSubject(dir, m.reuse)) {
			return cmd + " reusing the message of " + m.reuse + ", whose subject starts `apex:`"
		}
	case sub == "commit" && m.amend:
		if ReservedSubject(commitSubject(dir, "HEAD")) {
			return "`git commit --amend` keeping HEAD's subject, which starts `apex:`"
		}
	case m.template != "":
		if b, err := os.ReadFile(inDir(dir, m.template)); err == nil && messageReserved(string(b)) {
			return cmd + " -t " + m.template + ", whose subject starts `apex:`"
		}
	}
	return ""
}

// heredocRe is the message form Claude Code writes: "$(cat <<'EOF'
// … EOF\n)", the delimiter quoted or not.
var heredocRe = regexp.MustCompile(`(?s)^\s*\$\(\s*cat\s+<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?\s*\n(.*?)\n\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)\s*$`)

// messageReserved reports whether a commit message's subject — its first
// non-empty line, as git keeps it — starts `apex:`. A message made by a
// command substitution is read from its here-doc when it is one, else any
// `apex:` in it counts.
func messageReserved(msg string) bool {
	if strings.Contains(msg, "$(") || strings.Contains(msg, "`") {
		if m := heredocRe.FindStringSubmatch(msg); len(m) == 4 && m[1] == m[3] {
			return ReservedSubject(firstLine(m[2]))
		}
		return strings.Contains(strings.ToLower(msg), "apex:")
	}
	return ReservedSubject(firstLine(msg))
}

// firstLine is the first line of text that is not blank.
func firstLine(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// cherryPickVerdict denies picking a commit whose subject starts `apex:`.
func cherryPickVerdict(args []string, dir string) string {
	var revs []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-n" || a == "--no-commit" || a == "--abort" || a == "--quit" || a == "--continue" || a == "--skip":
			return ""
		case a == "-m" || a == "--mainline" || a == "-X" || a == "--strategy-option" || a == "--strategy":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			revs = append(revs, a)
		}
	}
	for _, rev := range revs {
		var subjects []string
		if strings.Contains(rev, "..") {
			subjects = gitLines(dir, "log", "--format=%s", rev)
		} else {
			subjects = []string{commitSubject(dir, rev)}
		}
		for _, s := range subjects {
			if ReservedSubject(s) {
				return "`git cherry-pick " + rev + "` copies a commit whose subject starts `apex:` (" + s + ")"
			}
		}
	}
	return ""
}

// patchSubjectRe is an mbox patch's subject line, its [PATCH …] prefix aside.
var patchSubjectRe = regexp.MustCompile(`(?m)^Subject:\s*(?:\[[^\]]*\]\s*)*(.*)$`)

// amVerdict denies applying a patch whose subject starts `apex:`, and a
// patch read from stdin.
func amVerdict(args []string, dir string) string {
	var files []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--continue" || a == "--skip" || a == "--abort" || a == "--quit" || a == "--resolved" || a == "-r" ||
			a == "--show-current-patch" || strings.HasPrefix(a, "--show-current-patch="):
			return ""
		case a == "-p" || a == "-C" || a == "--directory" || a == "--exclude" || a == "--include" || a == "--patch-format" ||
			a == "--whitespace" || a == "--resolvemsg" || a == "--quoted-cr" || a == "--empty":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			files = append(files, a)
		}
	}
	if len(files) == 0 || slices.Contains(files, "-") {
		return "`git am` reads its patches from stdin, which ape cannot check — name the patch files"
	}
	for _, f := range files {
		b, err := os.ReadFile(inDir(dir, f))
		if err != nil {
			continue
		}
		for _, m := range patchSubjectRe.FindAllStringSubmatch(string(b), -1) {
			if ReservedSubject(m[1]) {
				return "`git am " + f + "` applies a patch whose subject starts `apex:`"
			}
		}
	}
	return ""
}

func inDir(dir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

// commitSubject is a commit's subject, "" when rev does not resolve.
func commitSubject(dir, rev string) string {
	if out := gitLines(dir, "log", "-1", "--format=%s", rev, "--"); len(out) > 0 {
		return out[0]
	}
	return ""
}

// gitLines runs a short git query in dir; nil on any error.
func gitLines(dir string, args ...string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := git(ctx, dir, args...)
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}
