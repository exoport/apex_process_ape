// Package buildident derives a binary's build identity — version, build date, commit —
// from the ldflags goreleaser stamps when present, and from Go's own embedded build info
// otherwise.
//
// It is one package rather than a copy in each command because `ape` and `aped` have to
// AGREE. aped refuses to deliver an `ape` whose version differs from its own (PLAN-23), so
// if the two derived their identity by different rules, a perfectly matched locally-built
// pair would look mismatched and the daemon would refuse to start. That is exactly what
// happened before this existed: apecmd backfilled from build info while apedcmd did not,
// so a `make build` produced an `ape` reporting a pseudo-version
// (0.0.50-0.20260726213303-3f5b476b7a97) beside an `aped` reporting a bare "dev".
//
// Note that `go build` DOES stamp a version: since Go 1.24 the main module's version is
// derived from VCS state, so an unstamped local build reports a pseudo-version carrying
// the commit hash — not "(devel)". That is useful here: it makes the version comparison
// alone sufficient to tell two local builds from different commits apart.
package buildident

import (
	"runtime/debug"
	"strings"
)

// Identity is a binary's build provenance.
type Identity struct {
	Version   string
	BuildDate string
	GitCommit string
}

// Resolve fills the gaps in ldflags-provided values from Go's embedded build info. Pass
// whatever the linker stamped (or the package defaults); placeholder values are treated as
// absent so a local build still reports something true.
func Resolve(version, buildDate, gitCommit string) Identity {
	id := Identity{Version: version, BuildDate: buildDate, GitCommit: gitCommit}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return id
	}
	// The module version covers `go install module@vX.Y.Z` and, since Go 1.24, plain
	// `go build` (as a VCS-derived pseudo-version).
	if unset(id.Version) && info.Main.Version != "" && info.Main.Version != "(devel)" {
		id.Version = strings.TrimPrefix(info.Main.Version, "v")
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if unset(id.GitCommit) {
				id.GitCommit = s.Value
			}
		case "vcs.time":
			if unset(id.BuildDate) {
				id.BuildDate = s.Value
			}
		}
	}
	return id
}

// unset reports whether a value is a placeholder rather than real provenance. The two
// commands historically used different placeholders ("unknown" vs "none"), so all of them
// are recognized instead of picking a winner and silently breaking the other.
func unset(v string) bool {
	switch strings.TrimSpace(v) {
	case "", "dev", "unknown", "none":
		return true
	}
	return false
}
