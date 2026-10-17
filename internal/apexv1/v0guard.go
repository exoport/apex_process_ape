package apexv1

import (
	"os"
	"path/filepath"
)

// Backlog 38: ape v1 retired APEX v0's ceremony commands. A v0 project
// keeps the v0 ape its .bingo pins until it is lifted, so a v1 command or
// `ape task` run there by this ape refuses, naming both ways out.
// `ape framework update` and `ape project lift` are the exceptions: they
// are how a v0 project becomes a v1 one.

// V0ProjectMsg is the refusal's text.
const V0ProjectMsg = "this is an APEX v0 project: use its pinned v0 ape (.bingo), or lift it " +
	"(`ape framework update` to v1 runs `ape project lift`)"

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{"project.v0", 2, ActionPerson, "a v1 command or ape task in an APEX v0 project (a v0 framework install, no v1 layout): use the project's pinned v0 ape, or lift it (ape framework update to v1 runs ape project lift)"},
	)
}

// IsV0Project reports whether root holds an APEX v0 install and no v1
// layout: _apex/framework.yaml records a v0 version, or _apex/config.yaml
// carries v0's folder keys, and neither _apex/state/, development/project.md
// nor an older ape's development/.apex/ is there. A v1 layout wins: a lifted
// project keeps a v0 framework.yaml in its history, never in its tree, but a
// half-updated one is v1's to report (project check), not this refusal's.
func IsV0Project(root string) bool {
	if p, err := Open(root); err == nil && p.IsV1() {
		return false
	}
	if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(frameworkYAML))); err == nil {
		if m := readMeta(string(data)); m != nil && (m.major() == "v0" || m.major() == "" && m.Framework.GitHash != "") {
			return true
		}
	}
	data, err := os.ReadFile(ConfigPath(root))
	return err == nil && v0ConfigRe.Match(data)
}

// V0ProjectError is the refusal as a usage error (exit 2).
func V0ProjectError() error { return &UsageError{Code: "project.v0", Msg: V0ProjectMsg} }
