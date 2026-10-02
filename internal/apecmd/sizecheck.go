package apecmd

import (
	"fmt"
	"io"

	"github.com/exoport/apex_process_ape/internal/memory"
)

// Two commands stat a whole-file artifact and band it against the same two
// budgets: `ape memory check` over `team-memory.md` and `ape context check`
// over `project-context.md`. Skills read and append to both, so both meet
// the same two Read limits: the ~25,000-token cap on one Read (about 72 KB
// of markdown), past which a Read is partial, and the 256 KiB byte cap,
// past which it fails outright. The framework asked for one caller of the
// constants rather than two restatements of the numbers. The `--fail-at` policy, the leading size
// line and the flag wiring live here so the two commands cannot drift; the
// remediation prose does not, because it names a different skill in each.

// failAt values for `ape memory check` and `ape context check`.
const (
	failAtNever = "never"
	failAtSoft  = "soft"
	failAtHard  = "hard"
)

// sizeCheckShouldFail applies the --fail-at policy.
func sizeCheckShouldFail(state memory.State, failAt string) bool {
	switch failAt {
	case failAtSoft:
		return state == memory.StateOverSoft || state == memory.StateOverHard
	case failAtHard:
		return state == memory.StateOverHard
	default:
		return false
	}
}

// validateFailAt rejects an unknown --fail-at rather than treating it as
// `never`, which would silently disarm a CI gate someone asked for.
func validateFailAt(failAt string) error {
	switch failAt {
	case failAtNever, failAtSoft, failAtHard:
		return nil
	default:
		return usageErr(fmt.Errorf("--fail-at must be never|soft|hard, got %q", failAt))
	}
}

// emitSizeCheckLine writes the one line both checks lead with, in every
// band. label names the artifact — `memory`, `project-context` — and is
// what a skill greps for.
//
// An absent file names the path it looked at: `absent` is a verdict about
// one location, and a project that put the file somewhere else should be
// able to see that from the output alone.
func emitSizeCheckLine(w io.Writer, label string, c memory.Check) {
	if !c.Exists {
		fmt.Fprintf(w, "%s: absent (%s)\n", label, c.Path)
		return
	}
	fmt.Fprintf(w, "%s: %s / %s — %s (~%s tokens, estimated)\n",
		label, humanBytes(c.Bytes), humanBytes(c.SoftBudget), c.State, thousands(c.EstimatedTokens))
}
