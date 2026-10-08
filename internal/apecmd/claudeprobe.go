package apecmd

import (
	"context"
	"fmt"
	"os"

	"github.com/exoport/apex_process_ape/internal/buildident"
	"github.com/exoport/apex_process_ape/internal/claudeprobe"
	"github.com/exoport/apex_process_ape/internal/cost"
	"golang.org/x/mod/semver"
)

// ensureClaude runs the once-per-version startup probe (internal/claudeprobe)
// ahead of the spawn paths that drive claude through the PTY: `ape task`,
// `ape pipeline` and `ape prompt`. `ape chat` is not one of them — it hands
// claude the user's own terminal and never reads a pane.
//
// Its lines go to stderr in every mode, --quiet and --output-format json
// included: they appear once per claude version, and one of them is the
// reason a run did not start.
//
// models are the --model values the run will spawn with. Each one the
// installed claude is too old to know gets a warning — not a refusal: the
// run still works, on a model claude treats as unrecognized.
func ensureClaude(ctx context.Context, claudeBin string, models ...string) error {
	if claudeBin == "" {
		claudeBin = "claude"
	}
	err := claudeprobe.Ensure(ctx, claudeprobe.Options{
		ClaudeBin:  claudeBin,
		ApeVersion: buildident.Resolve(Version, BuildDate, GitCommit).Version,
		Out:        os.Stderr,
	})
	if err != nil {
		return err //nolint:wrapcheck // BrokenError is already a complete, user-facing message
	}
	if len(models) > 0 {
		for _, w := range claudeTooOldWarnings(claudeprobe.InstalledVersion(ctx, claudeBin), models) {
			fmt.Fprintln(os.Stderr, w)
		}
	}
	return nil
}

// claudeTooOldWarnings returns one warning per model the installed claude
// predates, by the price table's min_claude. ape resolves a family word to
// an id itself, so a release can start a model that the claude on this
// machine has never heard of: Claude Code 2.1.292 runs claude-haiku-5-5 as
// an unrecognized model with a 200k context window and no cost basis, and
// the wrong window changes when a step compacts. An unknown claude version
// or a model with no recorded floor warns about nothing.
func claudeTooOldWarnings(claudeVersion string, models []string) []string {
	if claudeVersion == "" || !semver.IsValid("v"+claudeVersion) {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range models {
		id := cost.NormalizeModel(m)
		floor := cost.MinClaude(id)
		if floor == "" || seen[id] || semver.Compare("v"+claudeVersion, "v"+floor) >= 0 {
			continue
		}
		seen[id] = true
		out = append(out, fmt.Sprintf(
			"⚠ claude %s predates %s (first known in Claude Code %s): it runs as an unrecognized model,\n"+
				"  with a guessed context window and no cost basis. Update Claude Code (`claude update`).",
			claudeVersion, id, floor))
	}
	return out
}
