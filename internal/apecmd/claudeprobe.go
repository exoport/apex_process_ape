package apecmd

import (
	"context"
	"os"

	"github.com/exoport/apex_process_ape/internal/buildident"
	"github.com/exoport/apex_process_ape/internal/claudeprobe"
)

// ensureClaude runs the once-per-version startup probe (internal/claudeprobe)
// ahead of the spawn paths that drive claude through the PTY: `ape task`,
// `ape pipeline` and `ape prompt`. `ape chat` is not one of them — it hands
// claude the user's own terminal and never reads a pane.
//
// Its lines go to stderr in every mode, --quiet and --output-format json
// included: they appear once per claude version, and one of them is the
// reason a run did not start.
func ensureClaude(ctx context.Context, claudeBin string) error {
	if claudeBin == "" {
		claudeBin = "claude"
	}
	return claudeprobe.Ensure(ctx, claudeprobe.Options{ //nolint:wrapcheck // BrokenError is already a complete, user-facing message
		ClaudeBin:  claudeBin,
		ApeVersion: buildident.Resolve(Version, BuildDate, GitCommit).Version,
		Out:        os.Stderr,
	})
}
