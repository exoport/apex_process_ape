package apecmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// newProjectRewriteLiveCmd is `ape project rewrite-live` (v1.4, D526):
// the (live) → (full) rewrite a framework migration runs.
func newProjectRewriteLiveCmd() *cobra.Command {
	var f v1Flags
	var check bool
	cmd := &cobra.Command{
		Use:   "rewrite-live [--check]",
		Short: "Rewrite the check kind (live) as (full), in one commit (the v1.4 migration)",
		Long: `Rewrite every ` + "`(live)`" + ` check tag as ` + "`(full)`" + `. (live) is (full)'s v1.2 name (D508):
v1.3 accepted it silently, v1.4 warns (project check's notice check.live-deprecated), and
it is removed no earlier than v1.5 (D526). A framework migration runs this (command: ape
project rewrite-live; check: ape project rewrite-live --check); a person may run it too.

What it rewrites, and nothing else in those files:
  specs        every ` + "`check (live):`" + ` line of development/specs/**/spec.md
  drafts       a draft change's change.md: its delta's ` + "`check (live):`" + ` lines and its
               tasks' ` + "`— check (live):`" + `
  open gates   a ` + "`# scenario (live):`" + ` comment line in development/gates/**; when the
               file is one an approval recorded and is as approved, the approval's SHA-256
               is recorded again in the same commit, so the rewrite is no gate drift
Lines inside fenced code blocks of a markdown file are examples and stay as written.

What it leaves: the archive (history), and a started change — its plan is frozen at start
(change.plan-drift), and ` + "`ape change archive`" + ` writes its delta's (live) as (full) into
the specs. They are listed as "left".

One commit, "` + apexv1.RewriteLiveSubject + `": provenance exempts it from spec.edited because
its diff is that rewrite and nothing else (a commit with the subject and any other edit is
judged like any other). Idempotent: with nothing to rewrite it writes nothing (exit 0).

Refused on a change branch (branch.change-branch), with uncommitted changes in a file it
would write (tree.dirty), and outside a git repository (git.not-a-repo). A command any
session may run (not person-only).

--check writes nothing: exit 0 when nothing is left to rewrite, 1 (project.live-left,
naming the files) when something is — the migration's check: 0 applied, 1 not applied,
2 (project.read-failed) the check itself failed. A started change's tags never count.

JSON: {"rewritten": [{"path", "kind", "count"}], "left": [{"path", "kind", "count", "why"}],
"approvals": [<milestone>], "commit"}; "rewritten" and "left" are always there. Under
--check "rewritten" lists what would be written, at the top level beside the failure's
ok/exit/diagnostics when it is not empty (exit 1).

Exit codes: 0 rewritten, or nothing to rewrite (--check: nothing left) · 1 refused, or
(--check) some left · 2 a file could not be read.`,
		Example: "  ape project rewrite-live --check\n  ape project rewrite-live\n  ape project rewrite-live --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			if check {
				res, err := p.RewriteLiveCheck()
				if err != nil {
					return v1FailWith(cmd, &f, err, nil, map[string]any{"rewritten": res.Rewritten, "left": res.Left})
				}
				return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
					fmt.Fprintln(w, "(live) rewrite: nothing left to rewrite")
					printLiveLeft(w, res.Left)
				})
			}
			res, err := p.RewriteLive(cmd.Context())
			if err != nil {
				return v1Fail(cmd, &f, err, res)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				if res.Commit == "" {
					fmt.Fprintln(w, "(live) rewrite: nothing to rewrite")
					printLiveLeft(w, res.Left)
					return
				}
				for _, r := range res.Rewritten {
					fmt.Fprintf(w, "rewrote %d (live) tag(s) in %s\n", r.Count, r.Path)
				}
				for _, m := range res.Approvals {
					fmt.Fprintf(w, "recorded %s's approved gate files again\n", m)
				}
				printLiveLeft(w, res.Left)
				fmt.Fprintf(w, "commit %.7s %s\n", res.Commit, apexv1.RewriteLiveSubject)
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().BoolVar(&check, "check", false, "write nothing: exit 0 when nothing is left to rewrite, 1 when something is")
	return cmd
}

func printLiveLeft(w io.Writer, left []apexv1.RewriteLiveFile) {
	for _, l := range left {
		fmt.Fprintf(w, "left %d (live) tag(s) in %s: %s\n", l.Count, l.Path, l.Why)
	}
}
