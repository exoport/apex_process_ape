package apedcmd

import "github.com/exoport/apex_process_ape/internal/buildident"

// Build metadata, stamped by goreleaser via -ldflags (mirrors internal/apecmd).
var (
	Version   = "dev"
	BuildDate = "unknown"
	GitCommit = "none"
)

// Exit codes (a small, stable table).
const (
	exitOK        = 0
	exitRunFailed = 1
	exitUsage     = 2
)

func init() {
	// Same derivation as `ape` (internal/buildident): aped compares its own version
	// against the `ape` it delivers into workspaces (PLAN-23), so a locally built pair must
	// report the SAME identity or the daemon would refuse a perfectly matched pair. Before
	// this existed, apecmd backfilled from build info and apedcmd did not — `ape` reported a
	// pseudo-version while `aped` reported a bare "dev".
	id := buildident.Resolve(Version, BuildDate, GitCommit)
	Version, BuildDate, GitCommit = id.Version, id.BuildDate, id.GitCommit
}
