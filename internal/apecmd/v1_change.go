package apecmd

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
)

func newChangeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "change",
		Short: "The unit of change: new, check, start, verify, review, accept, block, archive",
		Long: `A change is development/changes/<id>/change.md (written by the model) plus state.json
(written only by ape). Its lifecycle:

  new → draft ──start──▶ in-progress ──verify green──▶ verified ──review pass──▶ reviewed ──archive──▶ archived
                              ▲   │                        │                         │
                              └───┴── verify red ◀─────────┴──── review block ◀──────┘
  any status but archived ──block──▶ blocked ──start──▶ in-progress
  any status but archived ──abandon──▶ abandoned (moved to the archive, no delta merged)

Done is a command ape ran at a sha: models never set a status. Every status change is
committed by ape: state.json alone, subject "apex: <id> <status>", trailer "Change: <id>"
(archive commits "archive: <id>"). Skills never stage state.json. A blocked change claims
no requirements: it takes no part in start's conflict check.

Exit codes for every subcommand: 0 ok · 1 check failed (one-line remediation) · 2 usage ·
3 conflict (another change claims the same requirement, or the delta no longer applies;
nothing written).`,
	}
	cmd.AddCommand(
		newChangeNewCmd(), newChangeCheckCmd(), newChangeStartCmd(), newChangeVerifyCmd(),
		newChangeReviewCmd(), newChangeAcceptCmd(), newChangeBlockCmd(), newChangeArchiveCmd(),
		newChangeAbandonCmd(), newChangeDiffCmd(),
	)
	return cmd
}

func splitList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// v1ChangeLeaf builds a `ape change <verb> <id>` leaf around run.
func v1ChangeLeaf(use, short, long, example string, f *v1Flags, run func(cmd *cobra.Command, p *apexv1.Project, id string) error) *cobra.Command {
	cmd := &cobra.Command{
		Use:     use,
		Short:   short,
		Long:    long,
		Example: example,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, f, p); err != nil {
				return err
			}
			return run(cmd, p, args[0])
		},
	}
	addV1Flags(cmd, f)
	return cmd
}

func newChangeNewCmd() *cobra.Command {
	var f v1Flags
	var milestone, touches, paths string
	cmd := v1ChangeLeaf(
		"new <id> --milestone <M> --touches a,b [--paths globs]",
		"Validate the id and write the change.md skeleton and state.json (draft)",
		`Create development/changes/<id>/ with a change.md skeleton (frontmatter id, milestone,
touches, paths; # title; ## Why, ## Out of scope (pre-filled with the milestone's Out of
scope bullets), ## Delta, ## Tasks, ## Notes) and state.json with status draft, and
commit both as "apex: <id> draft" (trailer "Change: <id>"). Filling change.md in is the
author's own commit.

<id> is a verb-led kebab slug, ^[a-z][a-z0-9-]{2,39}$, unique across active AND archived
changes. --touches lists the capabilities the change adds to or modifies (kebab slugs;
new ones are fine — a capability is born from its first change). --paths are globs used
for overlap detection. Overlap (shared touches or paths) with another unarchived change
is a warning, not a failure.

Then fill change.md and run `+"`ape change check <id>`"+`.

Exit codes: 0 ok · 1 id taken (incl. archive) or milestone not in roadmap · 2 usage.`,
		"  ape change new add-tenant-delete --milestone M2 --touches tenants --paths 'internal/api/**'",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			if milestone == "" {
				return v1Fail(cmd, &f, &apexv1.UsageError{Code: "usage.milestone", Msg: "--milestone is required (e.g. M1)"}, nil)
			}
			res, err := p.NewChange(cmd.Context(), id, milestone, splitList(touches), splitList(paths))
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			if !f.json {
				for _, o := range res.Overlaps {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: overlaps %s (touches %v, paths %v)\n", o.With, o.Touches, o.Paths)
				}
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				fmt.Fprintf(w, "%s  draft  %s\n", res.ID, res.Path)
			})
		})
	cmd.Flags().StringVar(&milestone, "milestone", "", "milestone id from roadmap.md (required)")
	cmd.Flags().StringVar(&touches, "touches", "", "comma-separated capability slugs (required)")
	cmd.Flags().StringVar(&paths, "paths", "", "comma-separated globs, for overlap detection")
	return cmd
}

func newChangeCheckCmd() *cobra.Command {
	var f v1Flags
	return v1ChangeLeaf(
		"check <id>",
		"Validate change.md: shape, caps, delta against specs, a check on every task",
		`Validate one change and list every failure:

  - frontmatter id = folder, milestone in roadmap.md, touches non-empty kebab slugs,
    decisions exist in decisions/, paths are valid globs
  - # title; ## Why and ## Out of scope non-empty; ## Delta; ## Tasks
  - change.md ≤ 8 KB and design.md ≤ 8 KB ("split this change")
  - ## Delta: "### ADDED|MODIFIED|REMOVED [<capability>] Requirement: <name>" blocks, or
    the single line "none — <reason>". The capability must be in touches. ADDED names must
    not exist in specs/<capability>/spec.md yet; MODIFIED/REMOVED must name an existing
    heading exactly. ADDED/MODIFIED carry ≥ 1 "#### Scenario:" with exactly one
    "- check: <command> | human-review"; REMOVED carries "Reason:". A MODIFIED block
    keeps every scenario of the requirement it replaces, each with the same check
    (it may add scenarios); changed behaviour is REMOVED + ADDED under a new name.
    A name that nearly matches an existing heading (case, spacing, a typo) gets a
    "did you mean"; an ADDED that nearly matches one is refused.
  - nothing archive would drop: no prose before the first delta block, nothing but
    "Reason:" in a REMOVED block
  - a delta that REMOVES every requirement of a capability needs "retire: [<cap>]" in
    the frontmatter (archive then deletes its spec); retire: lists only such capabilities
  - ## Tasks: "- T<n> <what> — check: <command> | human-review", no checkboxes, unique ids

Exit codes: 0 green · 1 findings · 2 usage.`,
		"  ape change check add-tenant-delete",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			fs := p.CheckChange(id)
			if len(fs) > 0 {
				return v1Fail(cmd, &f, &apexv1.FailError{
					Code:     "change.check-failed",
					Msg:      fmt.Sprintf("change %s: %d finding(s) — fix change.md and run `ape change check %s` again", id, len(fs), id),
					Findings: fs,
				}, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, map[string]any{"id": id, "ok": true, "findings": []apexv1.Finding{}}, func(w io.Writer) {
				fmt.Fprintf(w, "change %s: green\n", id)
			})
		})
}

func newChangeStartCmd() *cobra.Command {
	var f v1Flags
	cmd := v1ChangeLeaf(
		"start <id>",
		"Check green + milestone approved → record base_sha and baseline, set in-progress",
		`Start a draft (or resume a blocked) change:

  1. `+"`ape change check <id>`"+` must be green, and its milestone approved
  2. refused with exit 3 if another active change (in-progress, verified, reviewed —
     not blocked) also ADDs, MODIFIES or REMOVES one of the same requirements
  3. records base_sha (HEAD) and started_at (kept when resuming a blocked change)
  4. runs every check verify will run once, as the baseline (which were already green):
     the command tasks and the effective spec's scenario checks; from the project root
     with sh -c; output under development/.apex/logs/<id>/
  5. sets in-progress, freezes the plan (SHA-256 of change.md outside ## Why and ## Notes), fingerprints
     the spec block of every requirement it MODIFIES or REMOVES, and commits state.json
     ("apex: <id> in-progress")

Re-start: once the plan is frozen, verify and archive refuse while change.md differs from
it anywhere but ## Why and ## Notes (frontmatter, title, Out of scope, Delta, Tasks, …). Running start again on an in-progress, verified or reviewed change
re-baselines: recorded under "replans" in state.json, verify and review cleared, new or
changed checks baselined now (unchanged ones keep their original baseline), fingerprints
re-taken. It applies when the plan changed, or when a spec block the change modifies or
removes was changed by another change since start; otherwise it is refused.

Exit codes: 0 started · 1 check red, milestone not approved, or wrong status · 2 usage ·
3 requirement conflict with another active change.`,
		"  ape change start add-tenant-delete",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			res, err := p.Start(cmd.Context(), id, f.runOptions())
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				fmt.Fprintf(w, "change %s: in-progress from %.7s; baseline:\n", id, res.BaseSHA)
				printChecks(w, res.Baseline, false)
			})
		})
	addV1TimeoutFlag(cmd, &f)
	return cmd
}

func newChangeVerifyCmd() *cobra.Command {
	var f v1Flags
	cmd := v1ChangeLeaf(
		"verify <id>",
		"Run the task checks and the effective spec at HEAD; verified iff all exit 0",
		`Run, at HEAD, every task's command check and every scenario check of the effective
spec — the spec as it will be once this change is archived: each current requirement the
change does not MODIFY or REMOVE (regression), plus the scenarios of its own ADDED and
MODIFIED blocks. human-review checks are not run. A command shared by several checks runs
once. Results (sha, exit, seconds, output sha256; kind, capability, requirement, scenario
for scenario checks) are recorded in state.json; output under development/.apex/logs/<id>/.
What a check leaves in the tree outside development/ — files it created, tracked files it
changed — is undone after it and reported (JSON "stray_files"); files that were there
before it ran are never touched.
The working tree must hold no uncommitted changes outside development/ — commit first
(trailer "Change: <id>"): a verify belongs to a sha.

All checks exit 0 → status verified (a reviewed change whose code has not moved
since its review stays reviewed), state.json committed. Any red → the failing checks' last
lines are printed. Green at baseline: the change's own checks (tasks, delta scenarios) that
already passed when they were baselined, before the code, are listed (JSON
"green_at_baseline", also recorded in state.json) for review to justify or flag.
On red: a change that was in-progress is left exactly as it was (nothing written,
nothing committed), a verified/reviewed one goes back to in-progress (committed).
Refused while change.md differs from the plan frozen at start outside ## Why and ## Notes
(re-start first). human-review tasks are never run; those not yet accepted are listed
as pending.

Exit codes: 0 all command checks green · 1 a check is red, uncommitted code, or wrong
status · 2 usage.`,
		"  ape change verify add-tenant-delete",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			res, err := p.Verify(cmd.Context(), id, f.runOptions())
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			human := func(w io.Writer) {
				fmt.Fprintf(w, "change %s at %.7s: %s\n", id, res.SHA, res.Status)
				printChecks(w, res.Results, true)
				if len(res.PendingHuman) > 0 {
					fmt.Fprintf(w, "human-review pending: %s\n", strings.Join(res.PendingHuman, ", "))
				}
				printGreenAtBaseline(w, res.GreenAtBaseline)
				printStrays(w, res.StrayFiles)
			}
			if !res.Green {
				if !f.json {
					human(cmd.OutOrStdout())
				}
				return v1Fail(cmd, &f, &apexv1.FailError{
					Code: "verify.red",
					Msg: fmt.Sprintf("change %s: %d command check(s) red — fix the code (never the check) and verify again, or `ape change block %s --reason ...`",
						id, len(res.Failing()), id),
				}, res)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, human)
		})
	addV1TimeoutFlag(cmd, &f)
	return cmd
}

// printStrays lists what the checks left in the tree and ape undid.
func printStrays(w io.Writer, strays []string) {
	if len(strays) > 0 {
		fmt.Fprintf(w, "cleaned up after the checks (created or changed by them, outside development/): %s\n", strings.Join(strays, ", "))
	}
}

// printGreenAtBaseline lists the change's own checks that passed before
// any code: evidence of nothing, for review to justify or flag.
func printGreenAtBaseline(w io.Writer, gab []apexv1.GreenAtBaseline) {
	if len(gab) == 0 {
		return
	}
	fmt.Fprintf(w, "green at baseline — passed before any code, so they prove nothing about this change; review must justify or flag each:\n")
	for _, g := range gab {
		when := "at start"
		if g.AtReplan {
			when = "at a re-plan, after code existed"
		}
		fmt.Fprintf(w, "  %-4s %s  (baselined %s)\n", g.Task, g.Cmd, when)
	}
}

func newChangeReviewCmd() *cobra.Command {
	var f v1Flags
	var verdict, findings, model string
	var runSession bool
	var wait time.Duration
	cmd := v1ChangeLeaf(
		"review <id> (--verdict pass|block --findings <file> | --run [--wait 9m] [--model M])",
		"Record a review verdict, or run the review in its own fresh Claude Code session",
		`Record the review of a verified change. --findings names the review file (normally
development/changes/<id>/review.md); it must exist and is recorded by path.

  pass   needs a green verify whose code has not moved since → status reviewed
  block  → status in-progress (fix, verify, review again)

--run reviews the change in its own Claude Code session — a separate process with an
empty context and its own hooks, never a sub-agent of the session that built it:
ape starts `+"`ape task apex-review --args <id>`"+` detached (survives the caller's shell),
and that session writes review.md and records the verdict with --verdict/--findings.
It is the one sanctioned exception to the nesting rule: allowed from inside an ape
session, never from inside a review session (depth 1), skill fixed to apex-review.
  - read-only: the session runs with APE_REVIEW=<id>, and `+"`ape hook pre-tool`"+` denies
    every edit inside the project except changes/<id>/review.md (outside the project,
    e.g. a throwaway copy to break, is allowed)
  - post-run assertion: a session that committed or left anything but review.md (and
    the state.json ape commits) has its verdict voided (recorded under review_voided)
  - --wait (default 9m, under the Bash tool's 10-minute cap) bounds one call; exit 75
    means still running: call `+"`ape change review <id> --run`"+` again to keep waiting —
    it attaches to the running session, it never starts a second one
  - the session's cost is its own ape task manifest (ape status sums it)

Exit codes: 0 recorded / review passed · 1 not verified, code changed since the verify,
review block (--run: findings in JSON "outcome"), verdict voided, or no verdict · 2 usage
(bad verdict, findings file missing, --run inside a review session) · 75 --run: still
running, call again.`,
		"  ape change review add-tenant-delete --verdict pass --findings development/changes/add-tenant-delete/review.md\n"+
			"  ape change review add-tenant-delete --run --wait 9m --json",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			if runSession {
				if verdict != "" || findings != "" {
					return v1Fail(cmd, &f, &apexv1.UsageError{Code: "usage.review-run", Msg: "--run records its verdict from the review session; drop --verdict/--findings"}, nil)
				}
				return runReviewSession(cmd, &f, p, id, model, wait)
			}
			if findings == "" {
				return v1Fail(cmd, &f, &apexv1.UsageError{Code: "usage.findings", Msg: "--findings <file> is required"}, nil)
			}
			st, err := p.Review(cmd.Context(), id, verdict, findings)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, st, func(w io.Writer) {
				fmt.Fprintf(w, "change %s: review %s → %s\n", id, st.Review.Verdict, st.Status)
			})
		})
	cmd.Flags().StringVar(&verdict, "verdict", "", "pass or block (required without --run)")
	cmd.Flags().StringVar(&findings, "findings", "", "the review file (required without --run)")
	cmd.Flags().BoolVar(&runSession, "run", false, "review in a fresh, detached, read-only Claude Code session (apex-review)")
	cmd.Flags().DurationVar(&wait, "wait", defaultReviewWait, "--run: how long this call waits for the session (exit 75 when it is still running)")
	cmd.Flags().StringVar(&model, "model", "", "--run: model for the review session (default: as ape task)")
	return cmd
}

func newChangeAcceptCmd() *cobra.Command {
	var f v1Flags
	var task, by string
	cmd := v1ChangeLeaf(
		"accept <id> --task <T> --by <who>",
		"Accept a human-review task",
		`Record that a person accepted a human-review task (one whose check is "human-review").
Archive requires every human-review task accepted, unless --allow-pending-human.

Exit codes: 0 recorded · 1 the task has a command check · 2 usage (no such task).`,
		"  ape change accept add-tenant-delete --task T2 --by diego",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			if task == "" {
				return v1Fail(cmd, &f, &apexv1.UsageError{Code: "usage.task", Msg: "--task <T> is required"}, nil)
			}
			st, err := p.Accept(cmd.Context(), id, task, by)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, st, func(w io.Writer) {
				fmt.Fprintf(w, "change %s: %s accepted by %s\n", id, task, by)
			})
		})
	cmd.Flags().StringVar(&task, "task", "", "task id, e.g. T2 (required)")
	cmd.Flags().StringVar(&by, "by", "", "who accepts (required)")
	return cmd
}

func newChangeBlockCmd() *cobra.Command {
	var f v1Flags
	var reason string
	cmd := v1ChangeLeaf(
		`block <id> --reason "<text>"`,
		"Set blocked, with the reason shown by ape status",
		`Set a change blocked. The reason is what `+"`ape status`"+` shows: what blocks it, or the
question only a person can answer. A blocked change lets the stop hook allow a stop.
`+"`ape change start <id>`"+` resumes it.

Exit codes: 0 blocked · 1 no such change or archived · 2 usage.`,
		`  ape change block add-tenant-delete --reason "needs a decision: soft or hard delete?"`,
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			st, err := p.Block(cmd.Context(), id, reason)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, st, func(w io.Writer) {
				fmt.Fprintf(w, "change %s: blocked — %s\n", id, *st.BlockedReason)
			})
		})
	cmd.Flags().StringVar(&reason, "reason", "", "what blocks it (required)")
	return cmd
}

func newChangeDiffCmd() *cobra.Command {
	var f v1Flags
	return v1ChangeLeaf(
		"diff <id>",
		"Show the specs as archive would leave them (unified diff per capability)",
		`Merge the change's delta into the current specs in memory — the same merge archive
makes, nothing written — and print a unified diff per capability it touches (a new
capability diffs from /dev/null). For review and for handoff: what the living spec will
say once this change is archived. JSON: {id, specs: [{capability, path, created, added,
modified, removed, diff}], delta_none?}.

Exit codes: 0 · 1 no such change · 2 usage · 3 the delta no longer applies to the specs
(conflicts listed).`,
		"  ape change diff add-tenant-delete\n  ape change diff add-tenant-delete --json",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			res, err := p.PreviewDelta(id)
			if err != nil {
				return v1Fail(cmd, &f, err, res)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				if len(res.Specs) == 0 {
					fmt.Fprintf(w, "change %s changes no spec (delta: none — %s)\n", id, res.DeltaNone)
					return
				}
				for _, s := range res.Specs {
					fmt.Fprint(w, s.Diff)
				}
			})
		})
}

func newChangeAbandonCmd() *cobra.Command {
	var f v1Flags
	var reason string
	cmd := v1ChangeLeaf(
		`abandon <id> --reason "<text>"`,
		"Move a change to the archive as abandoned, merging no delta; commit",
		`Drop a change: its folder moves to development/changes/archive/<YYYY-MM-DD>-<id>/ with
status abandoned and the reason recorded in state.json; its delta is NOT merged into
specs/. Committed as "apex: <id> abandoned" with trailer "Change: <id>". The id stays
taken. Abandon a blocked change only after planning its replacement, or with the reason
none is needed.

Exit codes: 0 abandoned · 1 no such change, archived, or git failed · 2 usage ·
3 the archive folder already exists.`,
		`  ape change abandon add-tenant-delete --reason "replaced by split-tenant-delete"`,
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			res, err := p.Abandon(cmd.Context(), id, reason)
			if err != nil {
				return v1Fail(cmd, &f, err, res)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				fmt.Fprintf(w, "abandoned %s → %s", id, res.ArchivedTo)
				if res.Commit != "" {
					fmt.Fprintf(w, " (commit %.7s)", res.Commit)
				}
				fmt.Fprintln(w)
			})
		})
	cmd.Flags().StringVar(&reason, "reason", "", "why it is dropped, and what replaces it (required)")
	return cmd
}

func newChangeArchiveCmd() *cobra.Command {
	var f v1Flags
	var allowPending bool
	cmd := v1ChangeLeaf(
		"archive <id>",
		"Apply the delta to specs/, move the change to the archive, commit `archive: <id>`",
		`Archive a reviewed change:

  1. needs status reviewed (review pass) and the plan frozen at start unchanged;
     re-runs verify when HEAD moved since it (red →
     in-progress, exit 1); refused if code changed since the review
  2. human-review tasks must be accepted, unless --allow-pending-human (recorded)
  3. applies the delta to development/specs/: ADDED appends, MODIFIED replaces, REMOVED
     deletes; a new capability gets specs/<cap>/spec.md (Purpose from the change's Why).
     Any conflict — including a MODIFIED that would drop a scenario the current spec has,
     or change its check, or a MODIFIED/REMOVED requirement whose spec block changed since
     start (another branch archived first: re-plan and re-start) — → exit 3 and NOTHING is
     written
  4. moves the folder to development/changes/archive/<YYYY-MM-DD>-<id>/, status archived
  5. commits the specs and the move (only those paths; hooks skipped) as
     "archive: <id>" with trailer "Change: <id>"

Exit codes: 0 archived (prints the commit) · 1 not reviewed, re-verify red, stale review,
pending human-review, or git failed · 2 usage · 3 the delta no longer applies.`,
		"  ape change archive add-tenant-delete",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			res, err := p.Archive(cmd.Context(), id, allowPending, f.runOptions())
			if err != nil {
				if res != nil && res.Verify != nil && !f.json {
					printChecks(cmd.OutOrStdout(), res.Verify.Results, true)
				}
				return v1Fail(cmd, &f, err, res)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				for _, s := range res.Specs {
					verb := "updated"
					if s.Created {
						verb = "created"
					}
					fmt.Fprintf(w, "%s %s (+%d ~%d -%d)\n", verb, s.Path, s.Added, s.Modified, s.Removed)
				}
				fmt.Fprintf(w, "archived %s → %s\ncommit %.7s archive: %s\n", id, res.ArchivedTo, res.Commit, id)
				if res.AllowPendingHuman {
					fmt.Fprintf(w, "recorded: archived with human-review pending (%s)\n", strings.Join(res.PendingHuman, ", "))
				}
			})
		})
	cmd.Flags().BoolVar(&allowPending, "allow-pending-human", false, "archive with human-review tasks unaccepted (recorded in state.json)")
	addV1TimeoutFlag(cmd, &f)
	return cmd
}
