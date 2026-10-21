package apexv1

import "regexp"

// RN-1 (D312): ape runs every check with sh, which is dash on Debian and
// Ubuntu. A check written for bash or zsh that uses one of their builtins
// exits 127 there with "sh: 2: source: not found" — and every note that
// reads a 127 said a command was missing on this machine, which is not
// what is wrong. A 127 whose output says such a builtin was not found gets
// a note naming it. The shell checks run with is unchanged.

// shellOnlyBuiltins are bash/zsh builtins (and reserved words) that dash
// does not have.
var shellOnlyBuiltins = map[string]bool{
	"source": true, "[[": true, "declare": true, "typeset": true, "shopt": true,
	"pushd": true, "popd": true, "dirs": true, "let": true, "mapfile": true,
	"readarray": true, "select": true, "setopt": true, "unsetopt": true,
	"autoload": true, "compgen": true, "complete": true, "disown": true,
	"enable": true, "caller": true, "suspend": true, "function": true,
}

// builtinNotFoundRe is a shell saying a command was not found: dash's
// "sh: 2: source: not found", bash's "sh: line 1: x: command not found".
var builtinNotFoundRe = regexp.MustCompile(`(?m)^(?:\S*sh: (?:line )?\d+: )?(\S+): (?:command )?not found\r?$`)

// shellBuiltinNotFound is the first bash/zsh builtin out says was not
// found, "" when it names none.
func shellBuiltinNotFound(out string) string {
	for _, m := range builtinNotFoundRe.FindAllStringSubmatch(ansiRe.ReplaceAllString(out, ""), -1) {
		if shellOnlyBuiltins[m[1]] {
			return m[1]
		}
	}
	return ""
}

// shellBuiltinNote is the note for a run that exited 127 with out naming
// a bash/zsh builtin as not found; "" otherwise.
func shellBuiltinNote(exit int, out string) string {
	if exit != exitNotFound {
		return ""
	}
	name := shellBuiltinNotFound(out)
	if name == "" {
		return ""
	}
	fix := "write that line in POSIX sh, or run it with bash explicitly (bash -c '…')"
	if name == "source" {
		fix = "use `.`, its POSIX form (`. <file>`)"
	}
	return "`" + name + "` is a bash/zsh builtin that /bin/sh here (dash) does not have — ape runs checks with sh, so the command is not missing from this machine: " + fix
}
