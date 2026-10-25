package apecmd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// newMilestoneGateTestCmd is `ape milestone gate-test` (v1.4, D543 B2):
// apex-plan's gate self-test, run through ape.
func newMilestoneGateTestCmd() *cobra.Command {
	var f v1Flags
	var stub string
	cmd := &cobra.Command{
		Use:   "gate-test <folder> [--stub <file>]",
		Short: "Self-test a gate folder before its milestone exists: the stub run passes, the plain gate is red",
		Long: `Run a gate folder's gate.sh the two ways apex-plan's gate self-test asks for, through ape:
first with --stub <file> (the folder's own stub when it has one, or the file --stub names),
which must exit 0 — a gate must be able to pass; then plain, which must be red — a gate
is red until the milestone's work is done. The folder is <dev>/gates/<M>-<slug>/; the
milestone need not exist yet (apex-plan writes the gate before ` + "`ape milestone new`" + `).

Each run is a check: from the project root, with APE_MILESTONE=<M> (from the folder's name)
and APE_MILESTONE_BASE=HEAD, under timeouts.gate, in its own process group, and what it
leaves running is ended and named (F15) — which a gate run from a session's Bash tool
does not get, so the pre-tool hook refuses a plain gate.sh there and names this command.
The verdicts are approve's self-check's (D70 B): a green plain run is milestone.gate-green,
one that cannot run (a missing or unparseable script, a requires: entry missing, exit 126
or 127, a timeout) milestone.gate-broken, a red stub run milestone.gate-stub-red; all three
action replan. It records nothing: no milestone, no gate result, no commit. The logs go to
_apex/state/logs/gate-test-<M>-stub.log and -plain.log.

<folder> may be given as its name alone (M4-time-entries), under <dev>/gates/.

A long run (lock <folder's name>): --wait bounds how long the call waits; exit 75 means
still running, and the same command attaches.

JSON: {"folder", "milestone", "runs": [{"mode": "stub"|"plain", "cmd", "exit", "secs", "log",
"reason", "leftovers", "verdict": "pass"|"fail", "tail"}], "ok"}.

Exit codes: 0 both runs as they must be · 1 a verdict above · 2 not a gate folder
(usage.gate-folder) · 75 still running.`,
		Example: "  ape milestone gate-test development/gates/M4-time-entries\n" +
			"  ape milestone gate-test development/gates/M4-time-entries --stub development/gates/M4-time-entries/stub --json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			// The long run's scope is the folder's name, which the worker's
			// re-run resolves under <dev>/gates/ (from the project root, so
			// --stub goes to it absolute).
			folder := args[0]
			if stub != "" {
				if abs, err := filepath.Abs(stub); err == nil {
					_ = cmd.Flags().Set("stub", abs)
				}
			}
			return runLong(cmd, &f, p, filepath.Base(filepath.Clean(folder)), func(ctx context.Context, opt apexv1.RunOptions) renderFn {
				res, err := p.GateTest(ctx, folder, stub, opt.Progress)
				return func(cmd *cobra.Command, f *v1Flags) error {
					if err != nil {
						return v1Fail(cmd, f, err, res)
					}
					return v1Emit(cmd.OutOrStdout(), f, res, func(w io.Writer) {
						for i := range res.Runs {
							r := &res.Runs[i]
							fmt.Fprintf(w, "%s run: exit %d in %.1fs — %s (%s)\n", r.Mode, r.Exit, r.Secs, r.Verdict, r.Log)
						}
						fmt.Fprintf(w, "%s: the gate self-test passes\n", res.Folder)
					})
				}
			})
		},
	}
	addV1Flags(cmd, &f)
	addV1WaitFlags(cmd, &f)
	cmd.Flags().StringVar(&stub, "stub", "", "the stand-in for the work (default: the folder's own stub, when it has one)")
	return cmd
}
