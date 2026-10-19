package apexv1

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The tag guard (item 10): `ape hook pre-tool` denies a Bash command that
// creates, deletes or pushes a git tag. A tag is a release, and only a
// person releases. EnvAllowTag=1 in the environment the person launched
// the session with — the hook's, which a command cannot set for itself
// (overrideSet) — lets a session tag.
//
// What is denied:
//
//	git tag <name> [...]                 creating (-a, -s, -m, -f, a bare name)
//	git tag -d|--delete <name>           deleting
//	git push --tags | --follow-tags | --mirror
//	git push <remote> <refspec>          where the refspec names refs/tags/...
//	                                     (either side, :refs/tags/x deletion
//	                                     included), is `tag <name>`, or is a
//	                                     short name the repository has as a
//	                                     tag and not as a branch
//	git push --delete|-d <remote> <name> under the same rule for <name>
//
// Listing and inspecting stay allowed: `git tag`, `git tag -l|--list
// [<pattern>]`, `-n`, `--contains`, `--merged`, `--points-at`, `--sort`,
// `--format`, `-v|--verify`.
//
// The command is parsed, not grepped: quotes and escapes are honoured;
// commands are split at && || ; | & newlines, parentheses and backticks;
// leading NAME=value assignments and the wrappers env, command, exec,
// nohup, time, sudo, doas, xargs, timeout, nice, setsid, busybox and
// stdbuf (by base name, with their options) are skipped; git's global options (-C <dir>,
// -c k=v, --git-dir, --work-tree, ...) are skipped; `sh|bash|zsh -c
// <script>`, `eval <words>` and `env -S <string>` are parsed recursively
// (three levels). So
// `echo "git tag v1"` is not flagged. Limits: a command substitution
// inside double quotes ("$(git tag x)"), a git alias, a script file, or a
// git invoked through a variable is not seen; and a short refspec name is
// read against the local repository's refs, so a tag that exists only on
// the remote is not recognised when deleted by its short name.
//
// Fast for every Bash call: a command that does not mention git returns
// before any parsing, and the repository is consulted only for a push of
// a short refspec name.

// EnvAllowTag lets a session create, delete and push tags.
const EnvAllowTag = "APE_ALLOW_TAG"

const tagGuardReason = "only a person tags or releases: %s — leave the tag to the human (ask them to run it). " +
	"A session they started with " + EnvAllowTag + "=1 may tag."

// TagGuard decides a Bash command; Deny is set for a tag write.
func TagGuard(command, cwd string) PreToolDecision {
	if !strings.Contains(command, "git") || overrideSet(EnvAllowTag) {
		return PreToolDecision{}
	}
	if what := tagWrite(command, cwd, 0); what != "" {
		return PreToolDecision{Deny: true, Reason: strings.Replace(tagGuardReason, "%s", what, 1) + inlineOverrideNote(command)}
	}
	return PreToolDecision{}
}

// tagWrite returns a description of the first tag write in a command line,
// "" for none.
func tagWrite(command, cwd string, depth int) string {
	return scanGit(command, cwd, depth, judgeTag)
}

// gitJudge judges one git invocation: its subcommand, the arguments after
// it, and the directory it runs in (-C applied). It returns a description
// of what the guard denies, "" to allow.
type gitJudge func(sub string, args []string, dir string) string

// cmdJudge judges one simple command: its words with the leading
// assignments and wrappers removed, so words[0] is the program run, and
// the directory it runs in. It returns a description of what the guard
// denies, "" to allow.
type cmdJudge func(words []string, cwd string) string

// scanGit returns the first non-empty verdict judge gives a git
// invocation in a command line, "" for none.
func scanGit(command, cwd string, depth int, judge gitJudge) string {
	return scanCmds(command, cwd, depth, func(words []string, cwd string) string {
		if filepath.Base(words[0]) != "git" {
			return ""
		}
		return gitInvocation(words[1:], cwd, judge)
	})
}

// scanCmds returns the first non-empty verdict judge gives a simple
// command of a command line, "" for none. `sh -c <script>`, `eval
// <words>` and `env -S <string>` are followed into (three levels).
func scanCmds(command, cwd string, depth int, judge cmdJudge) string {
	if depth > 3 {
		return ""
	}
	for _, words := range shellCommands(command) {
		if what := scanCmd(words, cwd, depth, judge); what != "" {
			return what
		}
	}
	return ""
}

// judgeTag is the tag guard's judge.
func judgeTag(sub string, args []string, dir string) string {
	switch sub {
	case "tag":
		return gitTagCmd(args)
	case "push":
		return gitPushCmd(args, dir)
	}
	return ""
}

var assignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// wrappers run the command that follows their options; matched by base
// name, so /usr/bin/sudo is sudo.
var wrappers = map[string]bool{
	"env": true, "command": true, "exec": true, "nohup": true, "time": true, "sudo": true, "doas": true,
	"xargs": true, "timeout": true, "nice": true, "setsid": true, "busybox": true, "stdbuf": true,
}

// wrapperValueOpts are a wrapper's options that take the next word as
// their value (`sudo -u root pkill x`: root is not the command).
var wrapperValueOpts = map[string]map[string]bool{
	"sudo":    {"-u": true, "-g": true, "-p": true, "-C": true, "-D": true, "-r": true, "-t": true, "-U": true, "-T": true, "-R": true},
	"doas":    {"-u": true, "-C": true},
	"env":     {"-u": true, "--unset": true, "-C": true, "--chdir": true},
	"exec":    {"-a": true},
	"timeout": {"-s": true, "--signal": true, "-k": true, "--kill-after": true},
	"nice":    {"-n": true, "--adjustment": true},
	"xargs":   {"-I": true, "-n": true, "-P": true, "-d": true, "-E": true, "-L": true, "-s": true, "-a": true},
	"stdbuf":  {"-i": true, "-o": true, "-e": true},
}

// scanCmd judges one simple command's words.
func scanCmd(words []string, cwd string, depth int, judge cmdJudge) string {
	// Leading assignments and wrappers.
	for len(words) > 0 {
		w := words[0]
		base := filepath.Base(w)
		if assignRe.MatchString(w) {
			words = words[1:]
			continue
		}
		if !wrappers[base] {
			break
		}
		words = words[1:]
	opts:
		for len(words) > 0 && strings.HasPrefix(words[0], "-") {
			o := words[0]
			words = words[1:]
			switch {
			case o == "--":
				break opts
			case base == "command" && (o == "-v" || o == "-V"):
				return "" // a lookup: `command -v pkill` runs nothing
			case base == "env" && (o == "-S" || o == "--split-string") && len(words) > 0:
				return scanCmds(strings.Join(words, " "), cwd, depth+1, judge)
			case wrapperValueOpts[base][o] && len(words) > 0:
				words = words[1:]
			}
		}
		if base == "timeout" && len(words) > 0 {
			words = words[1:] // the duration
		}
	}
	if len(words) == 0 {
		return ""
	}
	switch base := filepath.Base(words[0]); base {
	case "sh", "bash", "zsh", "dash", "ksh":
		for i := 1; i < len(words)-1; i++ {
			if f := words[i]; strings.HasPrefix(f, "-") && !strings.HasPrefix(f, "--") && strings.Contains(f, "c") {
				return scanCmds(words[i+1], cwd, depth+1, judge)
			}
		}
		return ""
	case "eval":
		return scanCmds(strings.Join(words[1:], " "), cwd, depth+1, judge)
	}
	return judge(words, cwd)
}

// gitInvocation skips git's global options and judges the rest.
func gitInvocation(args []string, cwd string, judge gitJudge) string {
	dir := cwd
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch a := args[0]; {
		case a == "-C" && len(args) > 1:
			if filepath.IsAbs(args[1]) {
				dir = args[1]
			} else {
				dir = filepath.Join(dir, args[1])
			}
			args = args[2:]
		case (a == "-c" || a == "--git-dir" || a == "--work-tree" || a == "--namespace" ||
			a == "--config-env" || a == "--super-prefix") && len(args) > 1:
			args = args[2:]
		default:
			args = args[1:]
		}
	}
	if len(args) == 0 {
		return ""
	}
	return judge(args[0], args[1:], dir)
}

// tagValueOpts take the next word as their value.
var tagValueOpts = map[string]bool{
	"-m": true, "--message": true, "-F": true, "--file": true, "-u": true, "--local-user": true,
	"--cleanup": true, "--sort": true, "--format": true, "--trailer": true,
}

// tagOptionalValueOpts take the next word as a commit when it is not an
// option.
var tagOptionalValueOpts = map[string]bool{
	"--contains": true, "--no-contains": true, "--merged": true, "--no-merged": true, "--points-at": true,
}

func gitTagCmd(args []string) string {
	list, verify, del := false, false, false
	var names []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			names = append(names, args[i+1:]...)
			i = len(args)
		case a == "-l" || a == "--list":
			list = true
		case a == "-v" || a == "--verify":
			verify = true
		case a == "-d" || a == "--delete":
			del = true
		case tagValueOpts[a]:
			i++
		case tagOptionalValueOpts[a]:
			list = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		case strings.HasPrefix(a, "--contains=") || strings.HasPrefix(a, "--no-contains=") ||
			strings.HasPrefix(a, "--merged=") || strings.HasPrefix(a, "--no-merged=") || strings.HasPrefix(a, "--points-at="):
			list = true
		case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "d") && !strings.HasPrefix(a, "-n"):
			del = true // -d bundled with others, e.g. -fd
		case strings.HasPrefix(a, "-"):
		default:
			names = append(names, a)
		}
	}
	switch {
	case del:
		return "`git tag " + strings.Join(args, " ") + "` deletes a tag"
	case list || verify || len(names) == 0:
		return ""
	}
	return "`git tag " + strings.Join(args, " ") + "` creates a tag"
}

// pushValueOpts take the next word as their value.
var pushValueOpts = map[string]bool{
	"-o": true, "--push-option": true, "--repo": true, "--receive-pack": true, "--exec": true,
}

func gitPushCmd(args []string, dir string) string {
	del := false
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			pos = append(pos, args[i+1:]...)
			i = len(args)
		case a == "--tags" || a == "--follow-tags" || a == "--mirror":
			return "`git push " + a + "` pushes tags"
		case a == "-d" || a == "--delete":
			del = true
		case pushValueOpts[a]:
			i++
		case strings.HasPrefix(a, "-"):
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) < 2 {
		return "" // no refspec: the configured push, which does not push tags unless push.followTags is set
	}
	refs := pos[1:]
	for i := range refs {
		r := strings.TrimPrefix(refs[i], "+")
		if r == "tag" && i+1 < len(refs) {
			return "`git push ... tag " + refs[i+1] + "` pushes a tag"
		}
		if strings.Contains(r, "refs/tags/") {
			return "`git push ... " + refs[i] + "` pushes a tag"
		}
		src, dst, colon := strings.Cut(r, ":")
		name, deleting := src, del
		if colon && src == "" {
			name, deleting = dst, true
		}
		if name == "" || !isLocalTagOnly(dir, name) {
			continue
		}
		if deleting {
			return "`git push ... " + refs[i] + "` deletes the tag " + name
		}
		return "`git push ... " + refs[i] + "` pushes the tag " + name
	}
	return ""
}

// isLocalTagOnly reports whether the repository at dir has refs/tags/name
// and no refs/heads/name.
func isLocalTagOnly(dir, name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	has := func(ref string) bool {
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "show-ref", "--verify", "--quiet", ref)
		return cmd.Run() == nil
	}
	return has("refs/tags/"+name) && !has("refs/heads/"+name)
}

// shellCommands splits a command line into simple commands, each a list of
// words with quotes and escapes resolved.
func shellCommands(line string) [][]string {
	lx := &shellLexer{rs: []rune(line)}
	for lx.i = 0; lx.i < len(lx.rs); lx.i++ {
		lx.step()
	}
	lx.endCmd()
	return lx.cmds
}

// shellLexer is shellCommands' state: the input, its position, the words
// of the command being read and the word being read.
type shellLexer struct {
	rs    []rune
	i     int
	cmds  [][]string
	words []string
	cur   strings.Builder
	inTok bool
}

func (lx *shellLexer) endWord() {
	if lx.inTok {
		lx.words = append(lx.words, lx.cur.String())
		lx.cur.Reset()
		lx.inTok = false
	}
}

func (lx *shellLexer) endCmd() {
	lx.endWord()
	if len(lx.words) > 0 {
		lx.cmds = append(lx.cmds, lx.words)
		lx.words = nil
	}
}

func (lx *shellLexer) next() (rune, bool) {
	if lx.i+1 < len(lx.rs) {
		return lx.rs[lx.i+1], true
	}
	return 0, false
}

// step consumes the rune at lx.i (and whatever it introduces).
func (lx *shellLexer) step() {
	c := lx.rs[lx.i]
	switch {
	case c == '\\':
		if n, ok := lx.next(); ok {
			lx.i++
			if n != '\n' {
				lx.cur.WriteRune(n)
				lx.inTok = true
			}
		}
	case c == '\'':
		lx.inTok = true
		for lx.i++; lx.i < len(lx.rs) && lx.rs[lx.i] != '\''; lx.i++ {
			lx.cur.WriteRune(lx.rs[lx.i])
		}
	case c == '"':
		lx.double()
	case c == ' ' || c == '\t':
		lx.endWord()
	case c == '>' || c == '<':
		lx.redirect()
	case strings.ContainsRune("\n;&|()`", c), (c == '{' || c == '}') && !lx.inTok:
		lx.endCmd()
	case c == '$':
		if n, ok := lx.next(); ok && n == '(' {
			lx.endCmd()
			lx.i++
			return
		}
		lx.cur.WriteRune(c)
		lx.inTok = true
	case c == '#' && !lx.inTok:
		for lx.i < len(lx.rs) && lx.rs[lx.i] != '\n' {
			lx.i++
		}
		lx.endCmd()
	default:
		lx.cur.WriteRune(c)
		lx.inTok = true
	}
}

// double reads a double-quoted string; a backslash escapes " \ $ and `.
func (lx *shellLexer) double() {
	lx.inTok = true
	for lx.i++; lx.i < len(lx.rs) && lx.rs[lx.i] != '"'; lx.i++ {
		if n, ok := lx.next(); ok && lx.rs[lx.i] == '\\' && strings.ContainsRune(`"\$`+"`", n) {
			lx.i++
		}
		lx.cur.WriteRune(lx.rs[lx.i])
	}
}

// redirect drops a redirection, its fd number and its target, so
// `git tag 2>&1` has no stray word.
func (lx *shellLexer) redirect() {
	if lx.inTok && strings.Trim(lx.cur.String(), "0123456789") == "" {
		lx.cur.Reset()
		lx.inTok = false
	}
	lx.endWord()
	skip := func(in func(rune) bool) {
		for n, ok := lx.next(); ok && in(n); n, ok = lx.next() {
			lx.i++
		}
	}
	skip(func(r rune) bool { return strings.ContainsRune("<>&|", r) })
	skip(func(r rune) bool { return r == ' ' || r == '\t' })
	skip(func(r rune) bool { return !strings.ContainsRune(" \t\n;&|()", r) })
}

// BashCommand reads a Bash tool call's command.
func BashCommand(input json.RawMessage) string { return bashCommand(input) }

// bashCommand reads a Bash tool call's command.
func bashCommand(input json.RawMessage) string {
	var in struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(input, &in)
	return in.Command
}

// ApeCommands are the words after `ape` of every ape invocation in a
// command line, parsed as the guards parse it (person acts, E2).
func ApeCommands(command string) [][]string {
	var out [][]string
	scanCmds(command, "", 0, func(words []string, _ string) string {
		if base := filepath.Base(words[0]); base == "ape" || base == "ape.exe" {
			out = append(out, words[1:])
		}
		return ""
	})
	return out
}
