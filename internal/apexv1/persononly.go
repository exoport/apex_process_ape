package apexv1

import (
	"path/filepath"
	"slices"
	"strings"
)

// PersonOnlyVerb is a command a person runs from a terminal only, never a
// model session, whatever was typed (RequirePersonFlag). Each is made by
// personOnly, which lists it: the pre-tool hook denies every listed
// command's Bash call (FINDING-V15-1C-PRE-1, D762), so a new person-only
// verb is denied there by construction.
type PersonOnlyVerb struct {
	// Label names it in the refusal ("ape milestone claim", "--usage-ok").
	Label string
	// Path is the command after `ape` (milestone claim); Flag, when set,
	// is the flag that makes it person-only (task --usage-ok).
	Path []string
	Flag string
}

// PersonOnly is every PersonOnlyVerb.
var PersonOnly []PersonOnlyVerb

func personOnly(label, flag string, path ...string) PersonOnlyVerb {
	v := PersonOnlyVerb{Label: label, Path: path, Flag: flag}
	PersonOnly = append(PersonOnly, v)
	return v
}

// The person-only verbs.
var (
	PersonMilestoneClaim   = personOnly("ape milestone claim", "", "milestone", "claim")
	PersonMilestoneAdopt   = personOnly("ape milestone claim --adopt", "--adopt", "milestone", "claim")
	PersonMilestoneRelease = personOnly("ape milestone release", "", "milestone", "release")
	PersonMilestonePause   = personOnly("ape milestone pause", "", "milestone", "pause")
	PersonMilestoneResume  = personOnly("ape milestone resume", "", "milestone", "resume")
	PersonTaskUsageOK      = personOnly("--usage-ok", "--usage-ok", "task")
	PersonOrdersAcceptFrom = personOnly("ape orders accept --from", "--from", "orders", "accept")
	PersonOrdersAccept     = personOnly("ape orders accept", "", "orders", "accept")
	PersonOrdersRevoke     = personOnly("ape orders revoke", "", "orders", "revoke")
)

// matches reports whether an ape invocation's arguments run v: its path as
// consecutive positional words, and its flag (as --flag or --flag=…) when
// it has one.
func (v PersonOnlyVerb) matches(args []string) bool {
	var pos []string
	flag := v.Flag == ""
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			if a == v.Flag || strings.HasPrefix(a, v.Flag+"=") && v.Flag != "" {
				flag = true
			}
			continue
		}
		pos = append(pos, a)
	}
	if !flag {
		return false
	}
	for i := 0; i+len(v.Path) <= len(pos); i++ {
		if slices.Equal(pos[i:i+len(v.Path)], v.Path) {
			return true
		}
	}
	return false
}

// isApeProgram reports whether a command's program is ape: `ape`, a build
// named ape-<something> (the trial's ape-v15-1b), by base name.
func isApeProgram(word string) bool {
	b := filepath.Base(word)
	return b == "ape" || strings.HasPrefix(b, "ape-")
}

// PersonOnlyGuard denies a Bash call that runs a person-only verb
// (FINDING-V15-1C-PRE-1): the hook runs in a model session whatever the
// command's own environment says, so `env -u APE_SESSION -u CLAUDECODE ape
// milestone resume M` is denied here though RequirePersonFlag would pass
// it. A verb with its flag is named before the same verb without (claim
// --adopt before claim).
func PersonOnlyGuard(command, cwd string) PreToolDecision {
	what := scanCmds(command, cwd, 0, func(words []string, _ string) string {
		if !isApeProgram(words[0]) {
			return ""
		}
		for _, flagged := range []bool{true, false} {
			for _, v := range PersonOnly {
				if (v.Flag != "") == flagged && v.matches(words[1:]) {
					return v.Label
				}
			}
		}
		return ""
	})
	if what == "" {
		return PreToolDecision{}
	}
	return PreToolDecision{Deny: true, Reason: what + " is a person's act, run from a terminal outside Claude Code: this is a model session, whatever the command's environment says, so ape denies it here (FINDING-V15-1C-PRE-1) — stop and ask the person to run it themselves"}
}
