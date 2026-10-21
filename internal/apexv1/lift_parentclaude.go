package apexv1

import (
	"os"
	"path/filepath"
	"strings"
)

// CodeLiftParentClaude is RG-2's warning: a folder above the project
// holds a CLAUDE.md with the apex-v0 managed block.
const CodeLiftParentClaude = "lift.parent-v0-claude"

// LiftWarning is something the lift does not refuse but the person must
// act on (RG-2). Path is absolute (terminal, JSON); Rel is the same file
// relative to the project root, which is what the committed disposition
// names — never a home path (D282).
type LiftWarning struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Rel     string `json:"rel"`
	Message string `json:"message"`
}

// v0OperatingRulesImport is the line the apex-v0 managed block imports
// into every Claude Code session under the folder that holds it.
const v0OperatingRulesImport = "@_apex/apex-operating-rules.md"

// parentV0Claude is RG-2 (D249, D277): the tenax rehearsal met
// ~/_dev/exoar/CLAUDE.md holding the apex-v0 managed block, a workspace
// install that imports the v0 operating rules into every session under it
// — the lifted project's included. The lift cleans the project's own
// CLAUDE.md and cannot see a parent's, so it says so: each CLAUDE.md in a
// folder above root, up to the filesystem root, whose managed block
// imports the v0 operating rules is one warning. Nothing is edited: the
// file is the person's, and other projects under it may not be lifted yet.
func parentV0Claude(root string) []LiftWarning {
	var out []LiftWarning
	for dir := filepath.Dir(root); ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, "CLAUDE.md")
		if data, err := os.ReadFile(path); err == nil && holdsV0ManagedImport(string(data)) {
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				rel = path
			}
			rel = filepath.ToSlash(rel)
			out = append(out, LiftWarning{
				Code: CodeLiftParentClaude, Path: path, Rel: rel,
				Message: rel + " holds an apex-v0 managed block that imports _apex/apex-operating-rules.md into every Claude Code session under it, this project's included — remove the block (and its _apex/) once every project under it is lifted; until then answer No if Claude Code asks to allow that import",
			})
		}
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	return out
}

// holdsV0ManagedImport reports whether doc's apex-v0 managed block
// imports the v0 operating rules.
func holdsV0ManagedImport(doc string) bool {
	in := false
	for l := range strings.SplitSeq(strings.ReplaceAll(doc, "\r\n", "\n"), "\n") {
		switch t := strings.TrimSpace(l); {
		case t == v0ManagedBegin:
			in = true
		case t == v0ManagedEnd:
			in = false
		case in && t == v0OperatingRulesImport:
			return true
		}
	}
	return false
}
