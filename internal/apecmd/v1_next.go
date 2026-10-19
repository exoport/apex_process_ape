package apecmd

import (
	"fmt"
	"io"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
)

func newNextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "next",
		Short: "Record a finding for later in development/next.md (Next)",
		Long: `Next holds what was noticed but is out of scope now (H26): one dated line per
finding, which apex-plan triages at the next delivery — a milestone, kept, or deleted
with the reason in its commit. Next is not a milestone.

Since D217 it lives in development/next.md; the roadmap keeps one line in its place,
"` + apexv1.NextPointer + `". A project made before keeps Next in development/roadmap.md
(a "## Next" section, or items under apex-plan's "Next — not milestones:" label) until
` + "`ape next move`" + ` moves it (the framework's migration v1.0.0_seq-09).`,
	}
	cmd.AddCommand(newNextAddCmd(), newNextMoveCmd())
	return cmd
}

func newNextAddCmd() *cobra.Command {
	var f v1Flags
	var from string
	cmd := &cobra.Command{
		Use:   `add "<text>" [--from <change-id>]`,
		Short: "Append a dated finding to Next (development/next.md), on the base branch",
		Long: `Append one line to Next:

  - YYYY-MM-DD <text> (from <change-id>)

(without "(from …)" when --from is not given) at the end of development/next.md (D217).
next.md is used when it exists, or when the roadmap holds no Next block: a project with
neither gets next.md from its skeleton (a "# Next" title and a line saying these are
findings to triage, not milestones) and the roadmap the line pointing at it,
"` + apexv1.NextPointer + `", above its first milestone. A project not yet
moved — its roadmap holds Next and there is no next.md — keeps getting the line in the
roadmap, so its items are never split across two files (D217 Q125): at the end of its
"## Next" section; else under apex-plan's "Next — not milestones:" label above the first
milestone, after its list. ` + "`ape next move`" + ` moves them (see its --help).

Committed alone on the base branch as "apex: next — <text>": from the base branch
itself, or from a change's branch, change/<id>, without a checkout — the change's branch
and working tree do not move, and a base branch moved only by such a record does not
make the change catch up. Refused when the base branch is checked out in another
worktree (git.base-checked-out).

For models and people alike: apex-apply and apex-change record what they notice outside
the change's scope here instead of building it (--from <id>, the change that noticed it).
The archive carries a review's "Noticed, out of scope" items the same way.

JSON: {"line", "path" (the file the line went to: development/next.md, or the roadmap
of a project not yet moved), "pointer" (the roadmap, when it was given the pointer
line), "branch", "commit"}.

Exit codes: 0 recorded · 1 no roadmap.md, or the base branch is checked out elsewhere ·
2 usage (empty or multi-line text, --from not a change id).`,
		Example: `  ape next add "the CSV export ignores --tz" --from add-tenant-export
  ape next add "ask finance whether refunds need an audit trail"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			res, err := p.AddNext(cmd.Context(), args[0], from)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				on := ""
				if res.Branch != "" {
					on = " on " + res.Branch
				}
				fmt.Fprintf(w, "%s: %s (committed%s)\n", res.Path, res.Line, on)
				if res.Pointer != "" {
					fmt.Fprintf(w, "%s: %s (the line pointing at %s)\n", res.Pointer, apexv1.NextPointer, res.Path)
				}
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().StringVar(&from, "from", "", "the change that found it")
	return cmd
}

// rc.7b (rc7b-next): D217 Q126.
func newNextMoveCmd() *cobra.Command {
	var f v1Flags
	var check bool
	cmd := &cobra.Command{
		Use:   "move [--check]",
		Short: "Move the roadmap's Next into development/next.md (the framework's seq-09 runs it)",
		Long: `Move Next out of development/roadmap.md into development/next.md (D217), in one commit,
"apex: Next moved to development/next.md (seq-09)". The framework's migration
v1.0.0_seq-09 runs it (command: ape next move; check: ape next move --check); ape
project init and the lift lay the new layout down directly.

  - the items of the roadmap's "## Next" sections and those under apex-plan's
    "Next — not milestones:" label above the first milestone are appended to next.md
    byte for byte (list line and continuation lines), in order — a section's other
    lines (prose, sub-headings, code) with them — and each item once: one next.md
    holds already, or the block held earlier, is left out (JSON "duplicates")
  - next.md is created from its skeleton when absent: a "# Next" title and one line
    saying these are findings to triage, not milestones
  - the label and its list are replaced, in place, by the line
    "` + apexv1.NextPointer + `"; each "## Next" section is removed, and
    with no label to replace the line goes above the first milestone. A roadmap that
    has the line keeps it, and gets no second one. Nothing else of the roadmap changes

Idempotent: once the roadmap holds no Next block and has the line, and next.md exists,
it writes nothing (JSON "pending": false). A project with both — a roadmap block and
next.md — is merged. ` + "`ape project check`" + ` lists the notice roadmap.next-inline
while the roadmap holds Next; ` + "`ape next add`" + ` and the archive keep adding to the
roadmap's block until then, when there is no next.md.

Refused on a change branch (branch.change-branch), outside a git repository
(git.not-a-repo), and while roadmap.md or next.md has uncommitted changes (tree.dirty):
nothing is written. A command any session may run (not person-only): it moves the
project's own lines and decides nothing.

--check writes nothing: exit 0 when Next is in next.md already, 1 (next.move-pending,
action apply, naming what is left) when a move is due — the migration's check: 0
applied, 1 not applied.

JSON: {"moved": [the items, as ape status lists them], "duplicates": […], "path"
(next.md), "roadmap", "created", "pointer_added", "pending", "commit"}. Under --check it
says what is due, under "result" beside the failure's ok/exit/diagnostics (exit 1).

Exit codes: 0 moved, or nothing due (--check: nothing due) · 1 refused, no roadmap.md,
or (--check) a move due.`,
		Example: "  ape next move --check\n  ape next move\n  ape next move --json",
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
				res, err := p.NextMoveCheck()
				if err != nil {
					return v1Fail(cmd, &f, err, res)
				}
				return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
					fmt.Fprintf(w, "Next is in %s already; nothing due\n", res.Path)
				})
			}
			res, err := p.NextMove(cmd.Context())
			if err != nil {
				return v1Fail(cmd, &f, err, res)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				if !res.Pending {
					fmt.Fprintf(w, "Next is in %s already; nothing to move\n", res.Path)
					return
				}
				fmt.Fprintf(w, "%d item(s) moved from %s to %s", len(res.Moved), res.Roadmap, res.Path)
				if n := len(res.Duplicates); n > 0 {
					fmt.Fprintf(w, " (%d already there, left out)", n)
				}
				fmt.Fprintln(w)
				if res.Created {
					fmt.Fprintf(w, "%s created\n", res.Path)
				}
				if res.PointerAdded {
					fmt.Fprintf(w, "%s: %s\n", res.Roadmap, apexv1.NextPointer)
				}
				if res.Commit != "" {
					fmt.Fprintf(w, "commit %.7s\n", res.Commit)
				}
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().BoolVar(&check, "check", false, "write nothing: exit 0 when Next is in next.md already, 1 when a move is due")
	return cmd
}
