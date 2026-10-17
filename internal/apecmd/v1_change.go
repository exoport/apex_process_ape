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
		Short: "The unit of change: new, check, approve (fixes), start, verify, review, accept, block, archive",
		Long: `A change is development/changes/<id>/change.md (written by the model) plus state.json
(written only by ape). Its lifecycle:

  new → draft ──start──▶ in-progress ──verify green──▶ verified ──review pass──▶ reviewed ──archive──▶ archived
                              ▲   │                        │                         │
                              └───┴── verify red ◀─────────┴──── review block ◀──────┘
  any status but archived ──block──▶ blocked ──start──▶ in-progress
  any status but archived ──abandon──▶ abandoned (moved to the archive, no delta merged)

Done is a command ape ran at a sha: models never set a status. Every status change is
committed by ape: state.json alone, subject "apex: <id> <status>", trailer "Change: <id>".
Skills never stage state.json. A red verify of an in-progress change commits nothing (D5):
its attempt is counted in the git-ignored _apex/state/attempts.json. A blocked change
claims no requirements: it takes no part in start's conflict check. A change whose red
verifies reach limits.attempts is blocked by verify itself (see ` + "`ape change verify --help`" + `).

Branch per change (trial decision D38):

  base branch ──change new (draft, planned here)──start──▶ change/<id> ──work, verify,
  review (ape's status commits land here)──archive──▶ ONE squash commit on the base
  branch ("<change title>", trailer "Change: <id>"); change/<id> deleted

  - start creates change/<id> from the branch HEAD is on (recorded as base_branch, with
    base_sha) and checks it out; start on a change whose branch exists checks it out
  - verify, review, accept and block record a started change's state, and need HEAD on
    change/<id> (else exit 1, code change.wrong-branch: ` + "`git switch change/<id>`" + `)
  - new, and the milestone and decision-accept records, need HEAD off every change
    branch (code branch.change-branch: ` + "`git switch <base>`" + `)
  - archive rebases change/<id> onto the base branch and squash-merges it; abandon
    deletes it. The base branch only ever receives verified, reviewed changes.
  - a change's status is read from its own branch when HEAD is elsewhere, so
    ` + "`ape status`" + ` on the base branch shows every change's real status

Fix lane (trial decision D40): a change with "milestone: none" is a fix — asked for by a
person outside the roadmap (apex-change with a request). It is approved on its own,
` + "`ape change approve <id> --by <who>`" + ` (on the base branch), instead of through a milestone,
and otherwise lives like any change: its branch, baseline, checks, a review in a separate
session, the delta merged at archive. It counts toward no milestone gate, no milestone's
regression baseline, and no milestone's spend or budget. Fixes form a queue: planned and
approved between runs (or ` + "`ape run stop`" + `, plan, approve, restart), never overlapping a
run (D45). An approved fix runs ahead of milestone work, at the orchestrator's next change
boundary; with no milestone run active, ` + "`ape task apex-orchestrator --args <id>`" + ` runs it
alone. ` + "`ape status`" + ` lists approved fixes first.

One checkout, one operator (D45): archive and abandon switch to the base branch, so they
refuse when another worktree has it checked out (exit 1, git.base-checked-out, naming that
worktree; nothing written) — an environment problem: the orchestrator stops, it does not
re-plan.

Resume after the base moved (D40): whenever a started change's base branch has moved
ahead of its base_sha, ape — at ` + "`ape change start`" + ` on a change that has its branch, at
verify and at archive — rebases change/<id> onto the base, re-runs the baseline at the new
base tip, and marks the old verify stale. A conflict is left on change/<id> for
apex-apply (H8): markers in the tree, HEAD on the branch, the change in-progress (exit 3,
change.conflicted, the files listed); apex-apply resolves them, ` + "`git add`" + `s and commits, verify
refuses until it has (change.unresolved-conflict), and the review runs again. When
apex-apply cannot resolve them it blocks the change, and the orchestrator re-plans it
(D43). The model never rebases, merges or cherry-picks on a change branch
(` + "`ape hook pre-tool`" + ` denies it).

Exit codes for every subcommand: 0 ok · 1 check failed (one-line remediation) · 2 usage ·
3 conflict (another change claims the same requirement or the delta no longer applies:
nothing written; or the change does not rebase onto its moved base: the conflict is left
on its branch).`,
	}
	cmd.AddCommand(
		newChangeNewCmd(), newChangeCheckCmd(), newChangeApproveCmd(), newChangeStartCmd(), newChangeVerifyCmd(),
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
		"new <id> --milestone <M>|none --touches a,b [--paths globs]",
		"Validate the id and write the change.md skeleton and state.json (draft)",
		`Create development/changes/<id>/ with a change.md skeleton (frontmatter id, milestone,
touches, paths; # title; ## Why, ## Out of scope (pre-filled with the milestone's Out of
scope bullets), ## Delta, ## Tasks, ## Notes) and state.json with status draft, and
commit both as "apex: <id> draft" (trailer "Change: <id>"). Filling change.md in is the
author's own commit. A change is planned on the base branch: refused while HEAD is on a
change branch, change/<other> (exit 1, code branch.change-branch; `+"`git switch <base>`"+`).

<id> is a verb-led kebab slug, ^[a-z][a-z0-9-]{2,39}$, unique across active AND archived
changes (development/archive/, and an older ape's development/changes/archive/).
--milestone none makes the change a fix (trial decision D40): what apex-change writes for
a person's request outside the roadmap. It needs no roadmap entry, inherits no Out of
scope, is approved on its own (`+"`ape change approve <id> --by <who>`"+`) instead of through a
milestone, and counts toward no milestone; it may touch capabilities and carry a delta
like any change.
--touches lists the capabilities the change adds to or modifies (kebab slugs; new ones
are fine — a capability is born from its first change). --paths are globs used for
overlap detection. Overlap (shared touches or paths) with another unarchived change
is a warning, not a failure.

Then fill change.md and run `+"`ape change check <id>`"+`.

Exit codes: 0 ok · 1 id taken (incl. archive), milestone not in roadmap, or HEAD on a
change branch · 2 usage.`,
		"  ape change new add-tenant-delete --milestone M2 --touches tenants --paths 'internal/api/**'\n"+
			"  ape change new fix-login-redirect --milestone none --touches auth",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			if milestone == "" {
				return v1Fail(cmd, &f, &apexv1.UsageError{Code: "usage.milestone", Msg: "--milestone is required (e.g. M1, or none for a fix)"}, nil)
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
	cmd.Flags().StringVar(&milestone, "milestone", "", "milestone id from roadmap.md, or none for a fix (required)")
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

  - frontmatter id = folder, milestone in roadmap.md (or "none": a fix, D40, approved
    with `+"`ape change approve`"+`), touches non-empty kebab slugs, decisions exist in
    decisions/, paths are valid globs
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

func newChangeApproveCmd() *cobra.Command {
	var f v1Flags
	var by string
	cmd := v1ChangeLeaf(
		"approve <id> --by <who>",
		"Approve a fix (a change with milestone: none), so it can start",
		`Record a person approving a fix (trial decision D40): a change with "milestone: none",
written by apex-change for a person's request outside the roadmap. A milestone change is
approved through its milestone (`+"`ape milestone approve <M> --by <who>`"+`); approve refuses one
(exit 1, change.approve-milestone). The approval is what `+"`ape change start`"+` of a fix needs
in place of an approved milestone (else exit 1, change.not-approved).

  - the fix must pass `+"`ape change check <id>`"+` and not be started yet (status draft, no
    change/<id> branch; else exit 1, change.wrong-status)
  - recorded in state.json as approved: {by, at, plan_digest} and committed alone on the
    base branch as "apex: <id> approved" (trailer "Change: <id>"); refused while HEAD is
    on a change branch (exit 1, branch.change-branch), like change new
  - the approval is of the plan change.md has now: a fix whose plan (change.md outside
    ## Why and ## Notes) changed since is refused at start until approved again
  - re-approving the same plan is a no-op (exit 0, JSON "already": true, nothing written)

Fixes form a queue: planned and approved between runs (or `+"`ape run stop`"+`, plan, approve,
restart), never overlapping a run (D45). An approved fix runs ahead of milestone work, at
the orchestrator's next change boundary; with no milestone run active,
`+"`ape task apex-orchestrator --args <id>`"+` runs it alone. `+"`ape status`"+` lists approved
fixes first (JSON "fixes", changes[].lane "fix", changes[].approved) and points next at the
first. A fix counts toward no milestone gate and no milestone's spend or budget.

Exit codes: 0 approved (or already) · 1 not a fix, check red, started, or HEAD on a change
branch · 2 usage (no --by).`,
		"  ape change approve fix-login-redirect --by diego",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			res, err := p.ApproveFix(cmd.Context(), id, by)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				if res.Already {
					fmt.Fprintf(w, "fix %s: already approved by %s at %s (nothing written)\n", id, res.Approved.By, res.Approved.At)
					return
				}
				fmt.Fprintf(w, "fix %s: approved by %s — it runs ahead of milestone work: `ape task apex-orchestrator --args %s`, or the orchestrator's next change boundary\n",
					id, res.Approved.By, id)
			})
		})
	cmd.Flags().StringVar(&by, "by", "", "who approves (required)")
	return cmd
}

func newChangeStartCmd() *cobra.Command {
	var f v1Flags
	cmd := v1ChangeLeaf(
		"start <id>",
		"Check green + approved → create and check out change/<id>, record the baseline, set in-progress",
		`Start a draft (or resume a blocked) change:

  1. `+"`ape change check <id>`"+` must be green, and its milestone approved — or, for a fix
     (milestone: none, D40), the fix approved for the plan it has
     (`+"`ape change approve <id> --by <who>`"+`; else exit 1, change.not-approved)
  2. refused with exit 3 if another active change (in-progress, verified, reviewed —
     not blocked; read from its own branch) also ADDs, MODIFIES or REMOVES one of the
     same requirements
  3. creates the branch change/<id> at HEAD and checks it out (trial decision D38): the
     tree must be clean (ignored files aside), HEAD on a branch (not detached, not another
     change's change/<other>); records that branch as base_branch, and base_sha (HEAD)
     and started_at (both kept when resuming a blocked change). A refused start leaves no
     branch and HEAD where it was. Refused while that branch is behind its upstream
     (exit 1, base.behind-upstream: pull; compared with the local remote-tracking ref,
     never fetched; no upstream, no check — D45: one operator per checkout, local trunk)
  4. runs every check verify will run once, as the baseline (which were already green):
     the command tasks and the effective spec's scenario checks; from the project root
     with sh -c; output under _apex/state/logs/<id>/. With "regression: <cmd>" in
     _apex/config.yaml (the project's own test suite) it runs that too, the same way, and
     records it in state.json (regression_baseline: cmd, exit, output sha256, sha); a red
     regression baseline is recorded, it does not block start
  5. sets in-progress, freezes the plan (SHA-256 of change.md outside ## Why and ## Notes), fingerprints
     the spec block of every requirement it MODIFIES or REMOVES, commits state.json
     ("apex: <id> in-progress") on change/<id>, and resets the attempts count since start
     (_apex/state/attempts.json)

All of the change's work — code commits, ape's status commits — then happens on
change/<id>. Start on a change whose branch exists, from another branch (the base, say),
checks change/<id> out first (clean tree); when the change is started and nothing needs
re-baselining it only resumes there (JSON "resumed": true). Re-start keeps base_branch.
A blocked change is re-planned on its branch (change.md there), then started again.

Resume after the base moved (D40): start on a change that has its branch first checks
whether the base branch moved ahead of base_sha (a fix or another change landed there).
If so ape catches the change up — the one path start, verify and archive share:
  - start only: refused while the base branch is behind its upstream (exit 1,
    base.behind-upstream: pull)
  - the tree must be clean (uncommitted files in the change's own folder are committed on
    its branch; anything else: exit 1, tree.dirty)
  - `+"`git rebase <base_branch>`"+` on change/<id>. A conflict (H8, D47 Q3) aborts the rebase;
    ape takes the baseline below at the base tip, keeps the old tip as
    refs/apex/pre-rebase/<id>/<n>, recreates change/<id> at the base tip with the change's
    folder as the old tip had it and the state recorded (status in-progress, the verify
    stale, rebases[] with old_tip_ref and conflicts), committed as "apex: <id> conflicted
    rebasing onto <base>"; then it cherry-picks the branch's net diff (merge-base..old
    tip) --no-commit, which leaves conflict markers and unmerged paths in the tree with
    HEAD on change/<id>: exit 3, change.conflicted, the files listed. apex-apply resolves
    them (edit, `+"`git add`"+`, commit with trailer "Change: <id>"); verify refuses until no
    unmerged path or marker remains (exit 1, change.unresolved-conflict); verify and the
    review then run again. When apex-apply cannot resolve them it blocks the change, and
    the orchestrator re-plans it (D43). A net diff that applies cleanly after all is
    committed as "apex: <id> re-applied onto <base> as one commit", and the catch-up ends
    as a clean one
  - the baseline (task checks, effective-spec scenarios, regression command) is re-run
    at the NEW BASE TIP, in place — the checkout detached there, then switched back, so
    ignored files (dependencies, build caches) are present as usual — never at the
    rebased HEAD, which holds the change's code: a new test of the change is red
    there; it replaces the old baseline, and green-at-baseline is judged against it
  - recorded in state.json (rebases: [{at, from, to, head, rebased}]; base_sha = the base
    tip; the last verify kept with stale: true and stale_reason), a verified or reviewed
    change goes back to in-progress, and the state is committed on change/<id> as
    "apex: <id> rebased onto <base>"
JSON "catch_up": {base_branch, from, to, rebased, baseline, regression, verify_stale,
status}. The model never rebases: ape does, here, at verify and at archive.

Re-start: once the plan is frozen, verify and archive refuse while change.md differs from
it anywhere but ## Why and ## Notes (frontmatter, title, Out of scope, Delta, Tasks, …). Running start again on an in-progress, verified or reviewed change
re-baselines: recorded under "replans" in state.json, verify and review cleared, new or
changed checks baselined now (unchanged ones keep their original baseline, and so does the
regression command unless it changed), fingerprints re-taken. It applies when the plan
changed, when a spec block the change modifies or removes was changed by another change
since start, or when the regression command has no baseline for it (configured or changed
since start); otherwise it is refused.

Exit codes: 0 started (or resumed) · 1 check red, milestone (or fix) not approved, wrong
status, dirty tree, detached HEAD, HEAD on another change's branch, the base behind its
upstream, or git failed · 2 usage ·
3 requirement conflict with another active change, or the change does not rebase onto
its moved base.`,
		"  ape change start add-tenant-delete",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			res, err := p.Start(cmd.Context(), id, f.runOptions())
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				printCatchUp(w, id, res.CatchUp)
				if res.Resumed {
					fmt.Fprintf(w, "change %s: %s, resumed on %s (base %s)\n", id, res.Status, res.Branch, res.BaseBranch)
					return
				}
				on := ""
				if res.Branch != "" {
					on = " on " + res.Branch
					if res.BaseBranch != "" {
						on += " (base " + res.BaseBranch + ")"
					}
				}
				fmt.Fprintf(w, "change %s: in-progress%s from %.7s; baseline:\n", id, on, res.BaseSHA)
				printChecks(w, res.Baseline, false)
				if r := res.Regression; r != nil {
					note := "green"
					if r.Exit != 0 {
						note = fmt.Sprintf("red (exit %d) — recorded: verify fails only if it is newly red; roadmap the fix (apex-plan)", r.Exit)
					}
					fmt.Fprintf(w, "  regression %s  %s\n", note, r.Cmd)
				}
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
for scenario checks) are recorded in state.json; output under _apex/state/logs/<id>/.
What a check leaves in the tree outside development/ — files it created, tracked files it
changed — is undone after it and reported (JSON "stray_files"); files that were there
before it ran are never touched.
With "regression: <cmd>" in _apex/config.yaml, the regression command runs too (recorded
in the results with kind "regression") and is judged against the change's regression
baseline (JSON "regression": {cmd, exit, baseline_exit, pre_existing_red, failed}): it
fails verify only when it is newly red — green at baseline, red now — or when the change
has no baseline for this command (re-start records one). Red at baseline and still red
is reported (pre_existing_red), not failed: the planner roadmaps it.
The working tree must hold no uncommitted changes outside development/ — commit first
(trailer "Change: <id>"): a verify belongs to a sha. HEAD must be on the change's branch,
change/<id> (D38; else exit 1, code change.wrong-branch).
When the base branch moved ahead of base_sha, verify first catches the change up exactly
as `+"`ape change start --help`"+` describes (D40): rebase onto the base (a conflict is left on
the branch: exit 3, change.conflicted), the baseline re-run at the new base tip, the old verify
marked stale, a verified/reviewed change back to in-progress, "apex: <id> rebased onto
<base>" committed; then it verifies the rebased code (JSON "catch_up"). A reviewed change
caught up here needs a new review. Refused while a conflicted catch-up is unresolved —
unmerged paths, or conflict markers in a file the rebase conflicted in (exit 1,
change.unresolved-conflict: resolve, `+"`git add`"+`, commit).

All checks exit 0 → status verified (a reviewed change whose code has not moved
since its review stays reviewed), state.json committed. Any red → the failing checks' last
lines are printed. Green at baseline: the change's own checks (tasks, delta scenarios) that
already passed when they were baselined, before the code, are listed (JSON
"green_at_baseline", also recorded in state.json) for review to justify or flag.
On red: every red verify is an attempt, counted in the git-ignored
_apex/state/attempts.json (JSON "attempts": since the last start or re-start, which reset
it; "attempts_total": never reset; a green verify changes neither). A red verify of an
in-progress change writes nothing to state.json and commits nothing (trial decision D5,
restored by D38): it keeps its last verify record. A verified/reviewed one goes back to
in-progress with this verify recorded, a status change committed as "apex: <id>
in-progress" on change/<id>. The red verify that brings attempts to the cap
(_apex/config.yaml limits.attempts, default 5) blocks the change instead: status blocked,
blocked_reason "attempts exceeded: <n> red verifies — <failing checks>", committed as
"apex: <id> blocked", exit 1 with code change.attempts-exceeded. Re-plan, then
`+"`ape change start <id>`"+` resumes it with a fresh count.
Refused while change.md differs from the plan frozen at start outside ## Why and ## Notes
(re-start first). human-review tasks are never run; those not yet accepted are listed
as pending.

Exit codes: 0 all command checks green · 1 a check is red (the attempts cap reached
included), uncommitted code, an unresolved conflict, or wrong status · 2 usage · 3 the change
does not rebase onto its moved base (the conflict is left on its branch).`,
		"  ape change verify add-tenant-delete",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			res, err := p.Verify(cmd.Context(), id, f.runOptions())
			if err != nil && res == nil {
				return v1Fail(cmd, &f, err, nil)
			}
			human := func(w io.Writer) {
				printCatchUp(w, id, res.CatchUp)
				fmt.Fprintf(w, "change %s at %.7s: %s\n", id, res.SHA, res.Status)
				printChecks(w, res.Results, true)
				if len(res.PendingHuman) > 0 {
					fmt.Fprintf(w, "human-review pending: %s\n", strings.Join(res.PendingHuman, ", "))
				}
				printGreenAtBaseline(w, res.GreenAtBaseline)
				printStrays(w, res.StrayFiles)
			}
			if err != nil {
				// The attempts cap blocked the change: the red results first.
				if !f.json {
					human(cmd.OutOrStdout())
				}
				return v1Fail(cmd, &f, err, res)
			}
			if !res.Green {
				if !f.json {
					human(cmd.OutOrStdout())
				}
				msg := fmt.Sprintf("change %s: %d command check(s) red, attempt %d of %d — fix the code (never the check) and verify again, or `ape change block %s --reason ...`",
					id, len(res.Failing()), res.Attempts, res.AttemptsCap, id)
				if r := res.Regression; r != nil && r.Failed && r.BaselineExit == nil {
					msg = fmt.Sprintf("change %s: %d command check(s) red, the regression command among them with no baseline to judge it by — `ape change start %s` records one (red is fine there); fix the code for the rest",
						id, len(res.Failing()), id)
				}
				return v1Fail(cmd, &f, &apexv1.FailError{Code: "verify.red", Msg: msg}, res)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, human)
		})
	addV1TimeoutFlag(cmd, &f)
	return cmd
}

// printCatchUp reports a catch-up with a moved base branch (D40): the
// rebase, and the baseline re-taken at the base tip.
func printCatchUp(w io.Writer, id string, cu *apexv1.CatchUp) {
	if cu == nil {
		return
	}
	how := "rebased " + apexv1.ChangeBranch(id) + " onto"
	if !cu.Rebased {
		how = apexv1.ChangeBranch(id) + " already holds"
	}
	fmt.Fprintf(w, "%s moved (%.7s..%.7s): %s it; baseline re-taken at %.7s:\n", cu.BaseBranch, cu.From, cu.To, how, cu.To)
	printChecks(w, cu.Baseline, false)
	if r := cu.Regression; r != nil {
		fmt.Fprintf(w, "  regression exit %d at the base  %s\n", r.Exit, r.Cmd)
	}
	if cu.VerifyStale {
		fmt.Fprintf(w, "the last verify is stale (kept for the record); %s is %s\n", id, cu.Status)
	}
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
	var verdict, findings, model, effort string
	var runSession bool
	var wait time.Duration
	cmd := v1ChangeLeaf(
		"review <id> (--verdict pass|block --findings <file> | --run [--wait 9m] [--model M])",
		"Record a review verdict, or run the review in its own fresh Claude Code session",
		`Record the review of a verified change. --findings names the review file (normally
development/changes/<id>/review.md); it must exist and is recorded by path.

  pass   needs a green verify whose code has not moved since → status reviewed
  block  → status in-progress (fix, verify, review again)

HEAD must be on the change's branch, change/<id> (D38; else exit 1, code
change.wrong-branch): the verdict is committed there.

--run reviews the change in its own Claude Code session — a separate process with an
empty context and its own hooks, never a sub-agent of the session that built it:
ape starts `+"`ape task apex-review --args <id>`"+` detached (survives the caller's shell),
and that session writes review.md and records the verdict with --verdict/--findings.
It is the one sanctioned exception to the nesting rule: allowed from inside an ape
session, never from inside a review session (depth 1), skill fixed to apex-review.
  - on the change's branch: --run needs HEAD on change/<id>, the session runs in this
    checkout and records its verdict there; a session that leaves HEAD elsewhere
    has its verdict voided and the branch checked out again
  - read-only: the session runs with APE_REVIEW=<id>, and `+"`ape hook pre-tool`"+` denies
    every edit inside the project except changes/<id>/review.md (outside the project,
    e.g. a throwaway copy to break, is allowed)
  - post-run assertion: a session that committed or left anything but review.md (and
    the state.json ape commits) has its verdict voided (recorded under review_voided)
  - --wait (default 9m, under the Bash tool's 10-minute cap) bounds one call; exit 75
    means still running: call `+"`ape change review <id> --run`"+` again to keep waiting —
    it attaches to the running session, it never starts a second one
  - the session's cost is its own ape task manifest (ape status sums it)
  - model and effort: --model/--effort; else _apex/config.yaml models.apex-review
    ("apex-review: sonnet" or "apex-review: {model: sonnet, effort: high}"; a config
    model without an effort does not take the asking session's effort); else the
    model and effort of the ape session that asked (so a review runs like the arm
    that requested it); else, for the effort, _apex/config.yaml effort:; else claude's
    defaults. The effort is passed as claude --effort. The review session's manifest
    records each and its source under review_launch (and, the same, under launch).

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
				return runReviewSession(cmd, &f, p, id, model, effort, wait)
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
	cmd.Flags().StringVar(&model, "model", "", "--run: model for the review session (default: models.apex-review in _apex/config.yaml, else the asking session's)")
	cmd.Flags().StringVar(&effort, "effort", "", "--run: effort for the review session (default: models.apex-review's effort, else the asking session's)")
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
HEAD must be on the change's branch, change/<id> (D38; else exit 1, code
change.wrong-branch): the acceptance is committed there.

Exit codes: 0 recorded · 1 the task has a command check, or HEAD not on change/<id> ·
2 usage (no such task).`,
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
`+"`ape change start <id>`"+` resumes it. A started change is blocked on its branch,
change/<id> (D38; else exit 1, code change.wrong-branch), where it stays — re-plan it
there and start it again, or abandon it; a change with no branch (a draft) is blocked on
the branch HEAD is on, unless that is another change's.

Exit codes: 0 blocked · 1 no such change, archived, or the wrong branch · 2 usage.`,
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
		`Drop a change: its folder moves to development/archive/<YYYY-MM-DD>-<id>/ with
status abandoned and the reason recorded in state.json; its delta is NOT merged into
specs/. Committed as "apex: <id> abandoned" with trailer "Change: <id>". The id stays
taken. Abandon a blocked change only after planning its replacement, or with the reason
none is needed.

A started change (D38): abandon runs from its branch change/<id> — then it switches to the
base_branch — or from any other non-change branch; the tree must be clean (uncommitted
files inside the change's own folder are committed on its branch first). The archived
folder holds the change's files as its branch last had them, the move is committed on the
base branch, change/<id> is deleted and its last commit recorded as branch_tip in the
archived state.json. None of the change's code reaches the base branch. A draft never
started (no branch) is abandoned on the branch HEAD is on, as before.

Exit codes: 0 abandoned · 1 no such change, archived, HEAD on another change's branch,
dirty tree, the base branch checked out in another worktree (git.base-checked-out), or git
failed · 2 usage · 3 the archive folder already exists.`,
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
				if res.BranchTip != "" {
					fmt.Fprintf(w, "; deleted %s at %.7s", apexv1.ChangeBranch(id), res.BranchTip)
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
		"Catch change/<id> up with its base, squash-merge it with the delta and the archive move as one commit",
		`Archive a reviewed change onto its base branch (trial decision D38):

  1. needs status reviewed (review pass) and the plan frozen at start unchanged; human-
     review tasks must be accepted, unless --allow-pending-human (recorded)
  2. runs with HEAD on change/<id>, or on another non-change branch (it switches to
     change/<id>); the tree must be clean — uncommitted files inside the change's own
     folder (review.md a review left) are first committed on change/<id> as
     "apex: <id> notes"; anything else refuses (code tree.dirty)
  3. refused if code changed on change/<id> since the review (review.stale); refused up
     front while the base branch is behind its upstream (base.behind-upstream: pull;
     the local remote-tracking ref, never fetched — D45)
  4. when the base branch moved ahead of base_sha, catches the change up — the one path
     start and verify share (D40, `+"`ape change start --help`"+`): rebases change/<id> onto the
     base (on a conflict nothing reaches the base: the conflict is left on change/<id>
     for apex-apply, the change back to in-progress, and archive exits 3,
     change.conflicted), re-runs the baseline at the new
     base tip and marks the verify stale; here the change stays reviewed
  5. re-runs verify when the code moved since it or it is stale — the base had moved (red
     → the change goes back to in-progress on its branch, exit 1, counted as an attempt;
     a green one keeps it reviewed: the review judged the change's own code)
  6. applies the delta to development/specs/: ADDED appends, MODIFIED replaces, REMOVED
     deletes; a new capability gets specs/<cap>/spec.md (Purpose from the change's Why).
     Any conflict — including a MODIFIED that would drop a scenario the current spec has,
     or change its check, or a MODIFIED/REMOVED requirement whose spec block changed since
     start (another change archived first: re-plan and re-start) — → exit 3 and nothing
     reaches the base branch
  7. switches to the base branch, `+"`git merge --squash change/<id>`"+`, writes the specs,
     moves the folder to development/archive/<YYYY-MM-DD>-<id>/ (status archived,
     branch_tip = change/<id>'s last commit) and commits everything as ONE commit —
     subject the change's # title, trailer "Change: <id>" (hooks skipped: every code
     commit on the branch already ran them); then deletes change/<id>. If anything fails
     on the base branch, it is reset to where it was and change/<id> checked out again.

The base branch gets exactly one commit per archived change; ape's status commits stay on
the change branch, squashed away.

Exit codes: 0 archived (prints the commit) · 1 not reviewed, re-verify red, stale review,
pending human-review, dirty tree, no change branch, the base behind its upstream
(base.behind-upstream), the base branch checked out in another
worktree (git.base-checked-out: stop, do not re-plan), or git failed · 2 usage · 3 the
rebase conflicts or the delta no longer applies.`,
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
				if res.Rebased {
					fmt.Fprintf(w, "rebased %s onto %s", apexv1.ChangeBranch(id), res.BaseBranch)
					if res.Reverified {
						fmt.Fprint(w, " and re-verified green")
					}
					fmt.Fprintln(w)
				}
				fmt.Fprintf(w, "archived %s → %s\ncommit %.7s %s (on %s; %s deleted)\n", id, res.ArchivedTo, res.Commit, res.Subject, res.BaseBranch, apexv1.ChangeBranch(id))
				if res.AllowPendingHuman {
					fmt.Fprintf(w, "recorded: archived with human-review pending (%s)\n", strings.Join(res.PendingHuman, ", "))
				}
			})
		})
	cmd.Flags().BoolVar(&allowPending, "allow-pending-human", false, "archive with human-review tasks unaccepted (recorded in state.json)")
	addV1TimeoutFlag(cmd, &f)
	return cmd
}
