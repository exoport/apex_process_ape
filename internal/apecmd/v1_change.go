package apecmd

import (
	"context"
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
		Short: "The unit of change: new, check, approve (fixes), start, verify, review, accept, block, answer, archive, tool",
		Long: `A change is development/changes/<id>/change.md (written by the model) plus state.json
(written only by ape). Its lifecycle:

  new → draft ──start──▶ in-progress ──verify green──▶ verified ──review pass──▶ reviewed ──archive──▶ archived
                              ▲   │                        │                         │
                              └───┴── verify red ◀─────────┴──── review block ◀──────┘
  any status but archived ──block──▶ blocked ──start──▶ in-progress
  blocked ──answer (a person)──▶ answered ──re-plan, start──▶ in-progress
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
  - start, verify, review and archive run the change's checks, and need HEAD on
    change/<id> (start and archive switch to it; verify and review refuse elsewhere with
    exit 1, code change.wrong-branch: ` + "`git switch change/<id>`" + `)
  - records from any branch (D51 Q17): check, diff, approve, accept, block, answer and
    abandon find a change wherever its records live — a started change's on change/<id>,
    a change with no branch of its own (a draft, an approved fix not started) on the base
    branch, read from the base's tip when HEAD is on another change's branch (that
    branch's copy is the one it was cut with) — and commit there, without a checkout
    when HEAD is elsewhere: HEAD, the index and the working tree stay as they were
    (refused when that branch is checked out in another worktree: git.base-checked-out).
    ` + "`ape status`" + ` lists the changes the same way, so what it lists these find
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

One checkout, one operator (D45): archive and abandon switch to the base branch (abandon
from another change's branch commits on it without a checkout), so they refuse when another
worktree has it checked out (exit 1, git.base-checked-out, naming that
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

Provenance (H14): start, verify and archive first walk the history since the v1 epoch
(the newest "apex: project init" or "apex: lift" commit) and refuse (exit 1, code
change.provenance, action person, findings spec.edited / state.forged) when a commit wrote
development/specs/** outside an archive squash (a "Change: <id>" trailer of an archived
change) or a state.json outside an "apex: …" commit or a squash. A person reverts the
commit (git revert), which clears it.

Notifier (H20): a subcommand that leaves the change blocked (block, verify at its attempts
limit, a rebase conflict) runs _apex/config.yaml's optional notify: command with
APE_EVENT=block, APE_CHANGE=<id> and APE_REASON=<the blocked reason>; best effort, it never
changes the outcome (see ape task --help).

Exit codes for every subcommand: 0 ok · 1 check failed (one-line remediation) · 2 usage ·
3 conflict (another change claims the same requirement or the delta no longer applies:
nothing written; or the change does not rebase onto its moved base: the conflict is left
on its branch).`,
	}
	cmd.AddCommand(
		newChangeNewCmd(), newChangeCheckCmd(), newChangeApproveCmd(), newChangeStartCmd(), newChangeVerifyCmd(),
		newChangeReviewCmd(), newChangeAcceptCmd(), newChangeBlockCmd(), newChangeAnswerCmd(), newChangeArchiveCmd(),
		newChangeAbandonCmd(), newChangeDiffCmd(), newChangeToolCmd(),
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

// provenanceVerbs are the change verbs that check provenance first (H14).
var provenanceVerbs = map[string]bool{"start": true, "verify": true, "archive": true}

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
			// H14: start, verify and archive refuse on a history since the
			// v1 epoch that wrote a spec or a state.json around ape.
			if provenanceVerbs[cmd.Name()] {
				if err := p.Provenance(cmd.Context()); err != nil {
					return v1Fail(cmd, f, err, nil)
				}
			}
			// H20: a change this command blocked (block, an attempts limit,
			// a rebase conflict) runs the project's notifier — where the
			// body ran: a long verb's detached worker notifies, not the
			// call that waited for it.
			before, _ := p.ChangeStatus(cmd.Context(), args[0])
			err = run(cmd, p, args[0])
			if f.long && !f.ranHere {
				return err
			}
			ctx := context.WithoutCancel(cmd.Context())
			if after, reason := p.ChangeStatus(ctx, args[0]); after == apexv1.StatusBlocked && before != apexv1.StatusBlocked {
				p.Notify(ctx, apexv1.NotifyBlock, args[0], reason, cmd.ErrOrStderr())
			}
			return err
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

Consecutive blocks (H19, D49): every block of a milestone's change (`+"`ape change block`"+`, a
limit reached at verify or start, a rebase conflict) counts toward its milestone in
_apex/state/milestones.json (consecutive_blocks), committed on the base branch from
whichever branch as "apex: <M> consecutive blocks <n> (<id>)"; an archived change of the
milestone sets it back to 0, inside the archive's commit. At limits.consecutive_blocks
(default 2), change new and change start of the milestone's changes and
`+"`ape task apex-orchestrator`"+` for it refuse with exit 1, code milestone.blocks-exceeded,
action stop, and `+"`ape status --json`"+` reports it under milestone.diagnostics. A person deals
with the blocked changes (answer or abandon them) and resets the count by approving the
milestone again: `+"`ape milestone approve <M> --by <who>`"+`. Fixes count toward no milestone.

Exit codes: 0 ok · 1 id taken (incl. archive), milestone not in roadmap, the milestone past
limits.consecutive_blocks, or HEAD on a change branch · 2 usage.`,
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
    decisions/, paths are valid globs; type: (the archive commit's {type}, D47) one of
    feat, fix, perf, refactor, docs, test, build, ci, chore — required only when
    _apex/config.yaml commit.subject uses {type} (change.no-type; change.bad-type);
    review_tools: [<name>, …] names tools.review entries of _apex/config.yaml whose
    when: the change matches (the orchestrator runs each before the review; else
    tools.unknown, D74); any other key must start with x-
  - # title; ## Why and ## Out of scope non-empty; ## Delta; ## Tasks
  - change.md without its ## Notes section ≤ caps.change_md (D89; change.too-big) and
    design.md ≤ caps.design_md (16 KB each by default; _apex/config.yaml caps:, see
    `+"`ape project check --help`"+`) ("split this change"); ## Notes is bounded only by
    the 48 KB file ceiling on the whole change.md, whatever the cap (change.over-ceiling:
    ## Notes is for working notes — move bulk to the change's assets/)
  - every spec the delta touches stays ≤ caps.spec (24 KB) once merged — the in-memory merge
    `+"`ape change diff`"+` shows and archive makes (else change.spec-too-big: split the
    capability first; start runs this check too, so it fails before any work, H18)
  - ## Delta: "### ADDED|MODIFIED|REMOVED [<capability>] Requirement: <name>" blocks, or
    the single line "none — <reason>". The capability must be in touches. ADDED names must
    not exist in specs/<capability>/spec.md yet; MODIFIED/REMOVED must name an existing
    heading exactly. ADDED/MODIFIED carry ≥ 1 "#### Scenario:" with exactly one
    "- check: <command> | human-review"; REMOVED carries "Reason:". A MODIFIED block
    keeps every scenario of the requirement it replaces, each with the same check
    (it may add scenarios); changed behaviour is REMOVED + ADDED under a new name.
    A name that nearly matches an existing heading (case, spacing, a typo) gets a
    "did you mean"; an ADDED that nearly matches one is refused. An ADDED block of a
    new capability may carry its "Purpose:": that line and every line after it up to a
    blank line, heading or fenced code, kept whole as the new spec's Purpose (never in
    the requirement)
  - nothing archive would drop: no prose before the first delta block, nothing but
    "Reason:" in a REMOVED block
  - a delta that REMOVES every requirement of a capability needs "retire: [<cap>]" in
    the frontmatter (archive then deletes its spec); retire: lists only such capabilities
  - ## Tasks: "- T<n> <what> — check: <command> | human-review", or "- T<n> <what> —
    regression: <command>" for a check that may already pass before the change (it runs
    like any check task; `+"`ape change start`"+` lets it be green at baseline); no checkboxes,
    unique ids. A design-tool task names its tool before the check:
    "- T<n> <what> — tool: <name> — check: <command> | human-review", <name> a
    tools.design entry of _apex/config.yaml (else tools.unknown, D74; see
    `+"`ape plugins --help`"+`); ape status --json lists it under changes[].tools

The change is checked as the branch holding its records has it, against that branch's
specs and roadmap (D51 Q17; `+"`ape change --help`"+`): HEAD needs to be on no particular branch.

Exit codes: 0 green · 1 findings · 2 usage.`,
		"  ape change check add-tenant-delete",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			fs := p.CheckChangeAnywhere(cmd.Context(), id)
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
    base branch as "apex: <id> approved" (trailer "Change: <id>"), from any branch (D51
    Q17): with HEAD on a change's branch, the fix is read and checked as the base holds
    it and the approval committed there without a checkout (refused when the base is
    checked out in another worktree: git.base-checked-out)
  - the approval is of the plan change.md has now: a fix whose plan (change.md outside
    ## Why and ## Notes) changed since is refused at start until approved again
  - re-approving the same plan is a no-op (exit 0, JSON "already": true, nothing written)
  - only a person approves (D47 Q6): refused in a model session — APE_SESSION or
    CLAUDECODE set, a `+"`!`"+` line inside Claude Code included — with exit 2, code
    session.person-only; run it from a plain terminal

Fixes form a queue: planned and approved between runs (or `+"`ape run stop`"+`, plan, approve,
restart), never overlapping a run (D45). An approved fix runs ahead of milestone work, at
the orchestrator's next change boundary; with no milestone run active,
`+"`ape task apex-orchestrator --args <id>`"+` runs it alone. `+"`ape status`"+` lists approved
fixes first (JSON "fixes", changes[].lane "fix", changes[].approved) and points next at the
first. A fix counts toward no milestone gate and no milestone's spend or budget.

Exit codes: 0 approved (or already) · 1 not a fix, check red, started, or the base branch
checked out in another worktree · 2 usage (no --by), or a model session.`,
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
		`Start a draft (or resume a blocked or answered) change:

  1. `+"`ape change check <id>`"+` must be green, and its milestone approved — through ape: the
     approval record in milestones.json, not the Status line alone (H2; else exit 1,
     milestone.status-unrecorded: a person runs `+"`ape milestone approve <M> --by <who>`"+`) — or, for a fix
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
     the command tasks and the effective spec's scenario checks of the capabilities the
     change touches (plus any scenario sharing a task's command — see ape change verify
     --help); from the project root with sh -c; output under _apex/state/logs/<id>/. With
     "regression: <cmd>" in _apex/config.yaml (the project's own check command; with tiers,
     "regression: {change: <cmd>, milestone: <cmd>}", its change tier) it runs that too,
     the same way, and records it in state.json (regression_baseline: cmd, exit, output
     sha256, sha); a red regression baseline is recorded, it does not block start.
     Vacuous checks (backlog 43): when the change has an ADDED or MODIFIED delta, a task
     check that already passes here, before any of the change's work, proves nothing
     about it — start refuses (exit 1, change.vacuous-check, nothing written, no branch
     left): rewrite the check to fail until the change is done, or write a check meant to
     stay green as "- T<n> <what> — regression: <command>". Not judged for Delta: none,
     a REMOVED-only delta, or a task a re-plan adds after the change has work
  5. sets in-progress, freezes the plan (SHA-256 of change.md outside ## Why and ## Notes), fingerprints
     the spec block of every requirement it MODIFIES or REMOVES, commits state.json
     ("apex: <id> in-progress") on change/<id>, and resets the attempts count since start
     (_apex/state/attempts.json)

All of the change's work — work commits, ape's status commits — then happens on
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
    rebased HEAD, which holds the change's work: a new check of the change is red
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

Loop limits (H19): a re-plan past limits.replans (default 3) since a person last answered
the change blocks it instead (reason "replans exceeded: …", exit 1, code
change.replans-exceeded, action person; the refused re-plan is not recorded). A change of a
milestone whose changes were blocked limits.consecutive_blocks (default 2) times in a row is
not started (exit 1, code milestone.blocks-exceeded, action stop; see `+"`ape change new --help`"+`).

Exit codes: 0 started (or resumed) · 1 check red, milestone (or fix) not approved, a vacuous
task check, wrong status, dirty tree, detached HEAD, HEAD on another change's branch, the base
behind its upstream, or git failed · 2 usage ·
3 requirement conflict with another active change, or the change does not rebase onto
its moved base.`,
		"  ape change start add-tenant-delete",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			return runLong(cmd, &f, p, id, func(ctx context.Context, opt apexv1.RunOptions) renderFn {
				res, err := p.Start(ctx, id, opt)
				return func(cmd *cobra.Command, f *v1Flags) error { return renderStart(cmd, f, id, res, err) }
			})
		})
	addV1TimeoutFlag(cmd, &f)
	addV1WaitFlags(cmd, &f)
	return cmd
}

// renderStart prints a start's outcome.
func renderStart(cmd *cobra.Command, f *v1Flags, id string, res *apexv1.StartResult, err error) error {
	if err != nil {
		return v1Fail(cmd, f, err, nil)
	}
	return v1Emit(cmd.OutOrStdout(), f, res, func(w io.Writer) {
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
			switch {
			case r.Reason == apexv1.ReasonUnrun:
				note = "unrun (" + r.Detail + ") — recorded; verify fails while it stays unrun"
			case r.Exit != 0:
				note = fmt.Sprintf("red (%s) — recorded: verify fails only if it is newly red; roadmap the fix (apex-plan)", r.Outcome())
			}
			fmt.Fprintf(w, "  regression %s  %s\n", note, r.Cmd)
		}
	})
}

func newChangeVerifyCmd() *cobra.Command {
	var f v1Flags
	cmd := v1ChangeLeaf(
		"verify <id>",
		"Run the task checks and the effective spec at HEAD; verified iff all exit 0",
		`Run, at HEAD, every task's command check and the scenario checks of the effective
spec — the spec as it will be once this change is archived: each current requirement the
change does not MODIFY or REMOVE (regression), plus the scenarios of its own ADDED and
MODIFIED blocks — of the capabilities the change touches (frontmatter touches:), plus any
other scenario whose check command is identical to a task's (H10). The rest of the spec
is the milestone gate's to run (`+"`ape milestone gate`"+` runs all of it). human-review checks
are not run. A command shared by several checks runs once. Each check runs under
_apex/config.yaml timeouts.check (default idle 15m, max 2h), the regression command under
timeouts.regression (idle 30m, max 2h): a command that makes no progress — no output, no
CPU time used by its process group — for idle, or still runs at max, is killed with its
whole process group and is red, exit 124, reason idle or ceiling (its last output lines
kept), never retried; --timeout sets max for both (see `+"`ape project init --help`"+`).
Ran nothing is not green (H11): a check that exits 0 but whose output says it ran
nothing (_apex/config.yaml checks.vacuous_output; default "no tests to run", "running 0
tests", "Ran 0 tests", "0 passing", "collected 0 items") is red with reason "vacuous"
(exit -2); a check whose requirement (requires: {NAME: env|cmd}) is missing — it mentions
$NAME and NAME is unset, or runs NAME and NAME is not on PATH — is not run and is red with
reason "unrun" (exit -3). JSON results carry "reason" and "detail" for both.
Flaky checks (H12): with "retries: <n>" in _apex/config.yaml a red check runs again up
to n times; a pass on a retry is green, recorded "flaky": true with "tries" (the failed
runs' output beside its log as <name>.try<k>.log), and listed by `+"`ape status`"+`. Results (sha, exit, seconds, output sha256; kind, capability, requirement, scenario
for scenario checks) are recorded in state.json; output under _apex/state/logs/<id>/.
What a check leaves in the tree outside development/ — files it created, tracked files it
changed — is undone after it and reported (JSON "stray_files"); files that were there
before it ran are never touched.
With "regression: <cmd>" in _apex/config.yaml (with tiers, its change tier), the
regression command runs too (recorded in the results with kind "regression") and is judged against the change's regression
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
<base>" committed; then it verifies the rebased work (JSON "catch_up"). A reviewed change
caught up here needs a new review. Refused while a conflicted catch-up is unresolved —
unmerged paths, or conflict markers in a file the rebase conflicted in (exit 1,
change.unresolved-conflict: resolve, `+"`git add`"+`, commit).

All checks exit 0 → status verified (a reviewed change whose work has not moved
since its review stays reviewed), state.json committed. Any red → the failing checks' last
lines are printed. Green at baseline: the change's own checks (tasks, delta scenarios) that
already passed when they were baselined, before the work, are listed (JSON
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
Re-planning does not reset limits.attempts_total (default 15, H19): the red verify that
brings the change's red verifies across all its starts — since a person last answered it
(`+"`ape change answer`"+`) — to it blocks the change (reason "attempts_total exceeded: …"),
exit 1 with code change.attempts-total-exceeded, action person: re-planning did not converge; a person answers, abandons it, or raises the
limit.
Every block of a milestone's change — here, by `+"`ape change block`"+`, by start's replans cap,
or by a rebase conflict — also counts toward the milestone's consecutive blocks, committed
on the base branch as "apex: <M> consecutive blocks <n> (<id>)" (see `+"`ape change new --help`"+`).
Refused while change.md differs from the plan frozen at start outside ## Why and ## Notes
(re-start first). human-review tasks are never run; those not yet accepted are listed
as pending.

Exit codes: 0 all command checks green · 1 a check is red (the attempts cap reached
included), uncommitted work, an unresolved conflict, or wrong status · 2 usage · 3 the change
does not rebase onto its moved base (the conflict is left on its branch).`,
		"  ape change verify add-tenant-delete",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			return runLong(cmd, &f, p, id, func(ctx context.Context, opt apexv1.RunOptions) renderFn {
				res, err := p.Verify(ctx, id, opt)
				return func(cmd *cobra.Command, f *v1Flags) error { return renderVerify(cmd, f, id, res, err) }
			})
		})
	addV1TimeoutFlag(cmd, &f)
	addV1WaitFlags(cmd, &f)
	return cmd
}

// renderVerify prints a verify's outcome.
func renderVerify(cmd *cobra.Command, f *v1Flags, id string, res *apexv1.VerifyResult, err error) error {
	if err != nil && res == nil {
		return v1Fail(cmd, f, err, nil)
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
		return v1Fail(cmd, f, err, res)
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
		return v1Fail(cmd, f, &apexv1.FailError{Code: "verify.red", Msg: msg}, res)
	}
	return v1Emit(cmd.OutOrStdout(), f, res, human)
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

  pass   needs a green verify whose work has not moved since → status reviewed
  block  → status in-progress (fix, verify, review again)

HEAD must be on the change's branch, change/<id> (D38; else exit 1, code
change.wrong-branch): the verdict is committed there.

--run reviews the change in its own Claude Code session — a separate process with an
empty context and its own hooks, never a sub-agent of the session that did the work:
ape starts `+"`ape task apex-review --args <id>`"+` detached (survives the caller's shell),
and that session writes review.md and records the verdict with --verdict/--findings.
It counts as starting apex-review under the nesting rule (D62; see ape task --help):
from a terminal (depth 0) always; from inside an ape session only from ape's own
apex-orchestrator run, as its one child (exit 3, session.child-running, while
another child runs, checked before the change is read); from any other session — apex-apply a person ran with ape task,
say — refused (exit 2, session.depth); never from inside a review session. The
review session is depth 2 under an orchestrator, its manifest records depth and
parent_run, and it is stopped when that orchestrator's run ends.
  - which changes: a verified one; or a reviewed one whose review is stale — the work
    changed since the review's sha, what archive refuses as review.stale (D51 Q16):
    --run first verifies it again in-process, as `+"`ape change verify <id>`"+` would
    (JSON "verify"; a red verify is returned as its error, verify.red, and no session
    starts), then reviews it as a verified change. A reviewed change whose review is
    fresh is refused (change.wrong-status): archive it.
  - on the change's branch: --run needs HEAD on change/<id>, the session runs in this
    checkout and records its verdict there; a session that leaves HEAD elsewhere
    has its verdict voided and the branch checked out again
  - read-only: the session runs with APE_REVIEW=<id>, and `+"`ape hook pre-tool`"+` denies
    every edit inside the project except changes/<id>/review.md (outside the project,
    e.g. a throwaway copy to break, is allowed)
  - post-run assertion: a session that committed or left anything but review.md, the
    review tools' reports changes/<id>/assets/review-<name>.md (written before --run,
    committed with review.md; D74) and the state.json ape commits has its verdict
    voided (recorded under review_voided). A commit touching only development/ (assets/
    included) after the verify or the review leaves both fresh
  - --wait (default 9m, at most 9m30s, under the Bash tool's 10-minute cap; 0 returns
    at once) bounds one call; exit 75 means still running: call
    `+"`ape change review <id> --run`"+` again to keep waiting — it attaches to the running
    session, it never starts a second one. The --json payload of a 75 has the fields every
    long verb's has (`+"`ape change verify --help`"+`): "running": true, "verb", "scope",
    "since", "log"
  - the session's cost is its own ape task manifest (ape status sums it)
  - model and effort: --model/--effort; else _apex/config.yaml models.apex-review
    ("apex-review: sonnet" or "apex-review: {model: sonnet, effort: high}"; a config
    model without an effort does not take the asking session's effort); else the
    model and effort of the ape session that asked (so a review runs like the arm
    that requested it); else, for the effort, _apex/config.yaml effort:; else claude's
    defaults. The effort is passed as claude --effort. The review session's manifest
    records each and its source under review_launch (and, the same, under launch).

Exit codes: 0 recorded / review passed · 1 not verified, the work changed since the verify,
--run: a fresh review, or a red re-verify of a stale one, review block (--run: findings
in JSON "outcome"), verdict voided, or no verdict (review.no-verdict; when the session's
last Stop still had background work running, the message says "ended with live
background work: <tasks>" and JSON outcome.background lists them) · 2 usage
(bad verdict, findings file missing, --run inside a review session, or the nesting
rule: session.depth) · 3 --run: the orchestrator's child session is running
(session.child-running) · 75 --run: still running, call again.`,
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
	cmd.Flags().DurationVar(&wait, "wait", defaultReviewWait, "--run: how long this call waits for the session (at most 9m30s; 0 returns at once); exit 75 when it is still running")
	cmd.Flags().StringVar(&model, "model", "", "--run: model for the review session (default: models.apex-review in _apex/config.yaml, else the asking session's)")
	cmd.Flags().StringVar(&effort, "effort", "", "--run: effort for the review session (default: models.apex-review's effort, else the asking session's)")
	return cmd
}

func newChangeAcceptCmd() *cobra.Command {
	var f v1Flags
	var task, posTask, by, verdict, reason string
	var evidence []string
	var delegate bool
	var wait time.Duration
	cmd := v1ChangeLeaf(
		"accept <id> [T<n>] (--task <T> --by <who> [--evidence <path|url>]... | --delegate [--wait 9m] | --verdict pass|fail --reason \"<text>\" [--evidence <path|url>]...)",
		"Accept a human-review task, in flight or archived — a person's act, or a delegate's (D75)",
		`Record that a person accepted a human-review task (one whose check is "human-review"),
in state.json's accepted: [{task, by, at, evidence}]. Archive requires every human-review
task accepted, unless a person archives with --allow-pending-human. The task is the
second argument (T<n>) or --task.

  - a change in flight: committed on its branch, change/<id> (D38) — from any branch
    (D51 Q17): without a checkout when HEAD is elsewhere (refused when that branch is
    checked out in another worktree: git.base-checked-out). A draft (no branch) is
    refused (change.wrong-branch: start it first)
  - a reviewed change with HEAD off change/<id> — on the base branch, say, where a
    person commits the evidence (D52 Q18): recorded on its base branch, in
    development/changes/<id>/accepted.json ({"accepted": [{task, by, at, evidence}]}),
    committed there as "apex: <id> accepted <T>" (trailer "Change: <id>"; without a
    checkout when HEAD is elsewhere). --evidence paths are resolved at the base's tip:
    the file must be committed there (else exit 2, usage.evidence), and that content's
    SHA-256 is recorded. The record never makes the review stale, and a base moved only
    by it (and ape's other records) is not a move archive rebases for. Every reader
    counts it — archive's pending-human check, `+"`ape status`"+` awaiting_human and
    pending_human, milestone close — and archive folds it into the archived state.json
    and drops the file with the folder. With HEAD on change/<id> it is recorded there,
    as before
  - an archived change (H4): its archived state.json, under
    development/archive/<date>-<id>/, is updated and committed on the base branch it was
    archived into — "apex: <id> accepted <T> (archived)", trailer "Change: <id>" — from any
    branch, without a checkout when HEAD is elsewhere (refused when that branch is checked
    out in another worktree: git.base-checked-out). `+"`ape status`"+` lists archived changes
    with unaccepted human-review tasks until then (JSON awaiting_human)
  - --evidence <path|url>, repeatable: what the acceptance rests on. A URL
    (scheme://…) is recorded as given; a path — relative to the project root — must be a
    regular file in the repository, recorded with its SHA-256 (else exit 2,
    usage.evidence)

Accepting a task again replaces its earlier acceptance.

--delegate: autonomous acceptance (D75, D76). Off by default (D78: project init writes no
human_review block; without one, delegate.off, action person). Only when
_apex/config.yaml delegates it:

    human_review:
      delegate: apex-accept                  # the default when the block is present
      args: "--task {task} --change {id}"    # the default; placeholders {id}, {task},
                                             # {assets}, {change} (D77)
      model: sonnet                          # optional, else models.<delegate>
      effort: high                           # optional, else models.<delegate>'s

  - the task must be awaiting a person: the change reviewed (or archived) and the task
    not accepted (else delegate.not-awaiting); human_review is under the config.changed
    guard, and
    --delegate refuses while a commit no person accepted changed it, or while the
    working tree's block differs from HEAD's (delegate.unaccepted-config, action person):
    a model cannot add the block and accept its own work. Not person-only: that is the
    point
  - ape starts `+"`ape task <delegate> --args \"<args filled>\"`"+` detached, as review --run
    does, with APE_DELEGATE=<id>/<T> (and APE_DELEGATE_PARENT=<the asking session>). Model
    and effort: human_review's, else models.<delegate>'s, else the asking session's, else
    _apex/config.yaml effort:, else claude's
  - the delegate inspects the task's artefact read-only and records its verdict with
    --verdict (below); `+"`ape hook pre-tool`"+` denies it every edit inside the project but
    an evidence note, changes/<id>/assets/accept-* (the archive folder's, for an
    archived change)
  - when the session ends ape settles it. A session that committed, switched branch or
    left any file other than its accept-* notes has its verdict voided (delegate.voided,
    action person; recorded in the verdict file, shown by ape status). On a pass ape
    records the acceptance exactly as a person's (the same record and branch rules as
    above), by "delegate:<skill>" — never a person — with "run": <run-id>; its evidence
    is the verdict's --evidence, else the verdict file; its accept-* notes are committed
    with the record, which carries the trailer
    "Accepted-By: delegate:<skill> <T> run <run-id>" (and so does the archive squash).
    On a fail nothing is accepted: the task stays awaiting_human, ape status shows the
    verdict (JSON changes[].delegate.<T>: {verdict, reason, run, at}), exit 1
    delegate.fail (action person). A session that records no verdict is a fail, reason
    "no verdict" (D78; its last message is never read for one), or, when its last Stop
    still had background work running, reason "ended with live background work:
    <tasks>" (D88; JSON outcome.background lists them)
  - no automatic retry on the same artefact (D78): after a fail, --delegate for that task
    refuses (delegate.already-failed, action person) until the change's tip moves
    (change/<id>'s tip; the archive folder's content once archived)
  - --wait (default 9m) bounds one call; exit 75 means still running: call
    `+"`ape change accept <id> <T> --delegate`"+` again — it attaches to the running session
  - JSON: {id, task, skill, args, running, detached_id, log, task_exit, launch,
    outcome: {verdict, reason, evidence, notes, violations, voided, accepted,
    verdict_file, background}}

--verdict pass|fail --reason "<text>" [--evidence <path|url>]...: the delegate session
records its verdict — only the session --delegate started for that change and task
(APE_DELEGATE=<id>/<T>, its run in progress; elsewhere exit 2, delegate.not-in-session).
It writes _apex/state/runs/delegate/<run-id>.json {change, task, skill, run, verdict,
reason, evidence, at, change_tip} — ignored state, never a commit. ape, not the session,
records the acceptance. --evidence (D78): a URL as given; the session's own
assets/accept-* note as it is on disk (ape commits it with the acceptance); any other
path must be committed at change/<id>'s tip while the change is unarchived — where its
design artefacts live, whatever branch HEAD is on — and a file in the working tree once
it is archived; each recorded with its SHA-256 (else exit 2, usage.evidence). With no
--evidence the acceptance rests on the verdict file, recorded by its path and SHA-256.

Exit codes: 0 recorded · 1 the task has a command check, a draft, or the branch it is
recorded on is checked out elsewhere; --delegate: off, config not accepted, not
awaiting, already failed at this tip, fail (no verdict included), voided · 2 usage (no such task, bad evidence, --verdict
outside its session) · 75 --delegate: still running, call again.`,
		"  ape change accept add-tenant-delete --task T2 --by diego\n"+
			"  ape change accept add-tenant-delete T2 --by diego --evidence docs/review/tenant-delete.png --evidence https://ci.example.com/run/812\n"+
			"  ape change accept add-tenant-delete T2 --delegate --wait 9m --json\n"+
			"  ape change accept add-tenant-delete T2 --verdict pass --reason \"matches the task\" --evidence development/changes/add-tenant-delete/assets/accept-T2.md",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			switch {
			case posTask != "" && task != "" && posTask != task:
				return v1Fail(cmd, &f, &apexv1.UsageError{Code: "usage.task", Msg: "the task is given twice (" + posTask + ", --task " + task + "): give it once"}, nil)
			case posTask != "":
				task = posTask
			}
			if task == "" {
				return v1Fail(cmd, &f, &apexv1.UsageError{Code: "usage.task", Msg: "the task is required: `ape change accept <id> T<n> …` (or --task)"}, nil)
			}
			switch {
			case delegate && (verdict != "" || by != "" || len(evidence) > 0 || reason != ""):
				return v1Fail(cmd, &f, &apexv1.UsageError{Code: "usage.delegate", Msg: "--delegate starts the delegate session, which records its own verdict and evidence: drop --verdict, --reason, --by and --evidence"}, nil)
			case verdict != "" && by != "":
				return v1Fail(cmd, &f, &apexv1.UsageError{Code: "usage.delegate", Msg: "--verdict is the delegate's, --by a person's: one or the other"}, nil)
			case reason != "" && verdict == "":
				return v1Fail(cmd, &f, &apexv1.UsageError{Code: "usage.delegate", Msg: "--reason goes with --verdict (the delegate's)"}, nil)
			case delegate:
				return runDelegateSession(cmd, &f, p, id, task, wait)
			case verdict != "":
				return recordDelegateVerdict(cmd, &f, p, id, task, verdict, reason, evidence)
			}
			st, err := p.Accept(cmd.Context(), id, task, by, evidence...)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, st, func(w io.Writer) {
				fmt.Fprintf(w, "change %s: %s accepted by %s\n", id, task, by)
			})
		})
	cmd.Args = cobra.RangeArgs(1, 2)
	cmd.PreRunE = func(_ *cobra.Command, args []string) error {
		if len(args) == 2 {
			posTask = args[1]
		}
		return nil
	}
	cmd.Flags().StringVar(&task, "task", "", "task id, e.g. T2 (or the second argument)")
	cmd.Flags().StringVar(&by, "by", "", "who accepts (required for a person's acceptance)")
	cmd.Flags().StringArrayVar(&evidence, "evidence", nil, "a file in the repository or a URL the acceptance (or --verdict) rests on (repeatable)")
	cmd.Flags().BoolVar(&delegate, "delegate", false, "accept through _apex/config.yaml human_review.delegate, in its own detached session (D75)")
	cmd.Flags().DurationVar(&wait, "wait", defaultReviewWait, "--delegate: how long this call waits for the session (exit 75 when it is still running)")
	cmd.Flags().StringVar(&verdict, "verdict", "", "pass or fail: the delegate session's verdict (only inside that session)")
	cmd.Flags().StringVar(&reason, "reason", "", "--verdict: why")
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
`+"`ape change start <id>`"+` resumes it. Recorded where the change's records live, from any
branch (D51 Q17; `+"`ape change --help`"+`): a started change on its branch, change/<id>
(D38), where it stays — re-plan it there and start it again, or abandon it; a change with
no branch (a draft, an approved fix) on the base branch. Without a checkout when HEAD is
elsewhere. A milestone change's block is counted in milestones.json on the base branch
(limits.consecutive_blocks).

Exit codes: 0 blocked · 1 no such change, archived, or the branch it is recorded on is
checked out in another worktree · 2 usage.`,
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

func newChangeAnswerCmd() *cobra.Command {
	var f v1Flags
	var text, by string
	cmd := v1ChangeLeaf(
		`answer <id> --text "<answer>" --by <who>`,
		"A person answers a blocked change: status answered, the answer recorded",
		`Record a person's answer to what blocks a change (H9, D47): status answered, and
answer: {by, at, text} in its state.json (also in `+"`ape status --json`"+`, changes[].answer).
Only for a blocked change (else exit 1, change.wrong-status). Committed as
"apex: <id> answered" (trailer "Change: <id>") where the change's state lives — its branch
change/<id> when it has one, else the branch the draft was planned on — from any branch:
without a checkout when HEAD is elsewhere (refused when that branch is checked out in
another worktree: git.base-checked-out).

The orchestrator then has apex-change re-plan the change with the answer as the hint and
starts it again: `+"`ape change start`"+` takes an answered change as it takes a blocked one (a
re-plan is recorded as a re-start). The answer also restarts the counts limits.replans and
limits.attempts_total judge (`+"`ape change verify --help`"+`).

Only a person answers (D47 Q6): refused in a model session — APE_SESSION or CLAUDECODE set,
a `+"`!`"+` line inside Claude Code included — with exit 2, session.person-only.

Exit codes: 0 answered · 1 not blocked, no such change, or the branch is checked out in
another worktree · 2 usage (no --text or --by), or a model session.`,
		`  ape change answer add-tenant-delete --text "soft delete; keep rows 30 days" --by diego`,
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			st, err := p.AnswerChange(cmd.Context(), id, text, by)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, st, func(w io.Writer) {
				fmt.Fprintf(w, "change %s: answered by %s — the orchestrator re-plans it with the answer, then `ape change start %s`\n", id, st.Answer.By, id)
			})
		})
	cmd.Flags().StringVar(&text, "text", "", "the answer (required)")
	cmd.Flags().StringVar(&by, "by", "", "who answers (required)")
	return cmd
}

func newChangeDiffCmd() *cobra.Command {
	var f v1Flags
	return v1ChangeLeaf(
		"diff <id>",
		"Show the specs as archive would leave them (unified diff per capability)",
		`Merge the change's delta into the current specs in memory — the same merge archive
makes, nothing written — and print a unified diff per capability it touches (a new
capability diffs from /dev/null). The change and the specs are read as the branch holding
its records has them, from any branch (D51 Q17). For review and for handoff: what the living spec will
say once this change is archived. JSON: {id, specs: [{capability, path, created, added,
modified, removed, diff}], delta_none?}.

Exit codes: 0 · 1 no such change · 2 usage · 3 the delta no longer applies to the specs
(conflicts listed).`,
		"  ape change diff add-tenant-delete\n  ape change diff add-tenant-delete --json",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			res, err := p.PreviewDeltaAnywhere(cmd.Context(), id)
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
files inside the change's own folder are committed on its branch first). From another
change's branch (D51 Q17) nothing is switched: the move is committed on the base branch
without a checkout, the folder taken as the branch holding its records has it (its own
branch, or the base for a draft), and the working tree is not touched. The archived
folder holds the change's files as its branch last had them, the move is committed on the
base branch, change/<id> is deleted and its last commit recorded as branch_tip in the
archived state.json, kept reachable as refs/apex/abandoned/<id> (tip_ref; H16, local). None of the change's work reaches the base branch. A draft never
started (no branch) is abandoned on the branch HEAD is on, as before.

Abandon never refuses for size (D89): no caps.change_md, file ceiling or caps: check — it
moves the folder as used and never trims or edits it.

Exit codes: 0 abandoned · 1 no such change, archived, dirty tree, change/<id> checked out in
another worktree, the base branch checked out in another worktree (git.base-checked-out), or git
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

  1. needs status reviewed (review pass), the plan frozen at start unchanged, and change.md
     without ## Notes within caps.change_md (default 16 KB, D69, D89 — change.too-big: the
     plan is frozen at start, so only ## Why can have grown; move what it gained since
     start to ## Notes, commit, archive again), the whole change.md within the 48 KB file
     ceiling (change.over-ceiling: move bulk to the change's assets/), and no
     caps.over-ceiling (caps: holding a value above its ceiling); human-
     review tasks must be accepted, unless --allow-pending-human (recorded) — a person's
     flag only (D45): refused in a model session (APE_SESSION or CLAUDECODE set) with exit
     2, session.person-only; the orchestrator leaves such a change reviewed and moves on.
     An archived change's unaccepted tasks stay in `+"`ape status`"+` and stay acceptable
     (`+"`ape change accept --help`"+`)
  2. runs with HEAD on change/<id>, or on another non-change branch (it switches to
     change/<id>); the tree must be clean — uncommitted files inside the change's own
     folder (review.md a review left) are first committed on change/<id> as
     "apex: <id> notes"; anything else refuses (code tree.dirty)
  3. refused if the work changed on change/<id> since the review (review.stale); refused up
     front while the base branch is behind its upstream (base.behind-upstream: pull;
     the local remote-tracking ref, never fetched — D45)
  4. when the base branch moved ahead of base_sha, catches the change up — the one path
     start and verify share (D40, `+"`ape change start --help`"+`): rebases change/<id> onto the
     base (on a conflict nothing reaches the base: the conflict is left on change/<id>
     for apex-apply, the change back to in-progress, and archive exits 3,
     change.conflicted), re-runs the baseline at the new
     base tip and marks the verify stale; here the change stays reviewed
  5. re-runs verify when the work moved since it or it is stale — the base had moved (red
     → the change goes back to in-progress on its branch, exit 1, counted as an attempt;
     a green one keeps it reviewed: the review judged the change's own work)
  6. applies the delta to development/specs/: ADDED appends, MODIFIED replaces, REMOVED
     deletes; a new capability gets specs/<cap>/spec.md (Purpose from the change's Why).
     Any conflict — including a MODIFIED that would drop a scenario the current spec has,
     or change its check, or a MODIFIED/REMOVED requirement whose spec block changed since
     start (another change archived first: re-plan and re-start) — → exit 3 and nothing
     reaches the base branch
  7. switches to the base branch, `+"`git merge --squash change/<id>`"+`, writes the specs,
     moves the folder, exactly as used — archive never trims it or edits change.md —
     to development/archive/<YYYY-MM-DD>-<id>/ (status archived,
     branch_tip = change/<id>'s last commit) — with "evidence: archive" in
     _apex/config.yaml (H23; default off) also its evidence/: one file per check of the
     final verify (command, exit, duration, output sha256, and the output from the log,
     capped at caps.evidence_log, default 1 MiB: head and tail kept, the full log named by
     path and sha256) — and
     commits everything as ONE commit (H15):
     subject from _apex/config.yaml commit.subject — placeholders {type} (change.md's
     type:), {title} (its # title) and {id}; default "{title}" — and a body holding the
     branch commits' own subjects (not ape's "apex:" ones) and every trailer of theirs but
     "Change:", deduplicated, then "Change: <id>". The project's git hooks run on this
     commit (commit.hooks: run, the default; skip opts out) — ape's status commits keep
     skipping them. A hook that refuses rolls archive back: exit 1, archive.hook-failed,
     the hook output's tail in the message. Then keeps change/<id>'s tip as
     refs/apex/archive/<id> (H16: recorded as tip_ref in the archived state.json, so
     branch_tip and the verify and review shas survive git gc; local, never pushed by
     ape) and deletes change/<id>. If anything fails
     on the base branch, it is reset to where it was and change/<id> checked out again.

The base branch gets exactly one commit per archived change; ape's status commits stay on
the change branch, squashed away.

A crash mid-archive (H28): archive keeps a journal, _apex/state/archive-journal.json
(ignored; base, its sha before, the branch and its tip, the step), from before the
catch-up until it returns. A journal that outlives its process is rolled back by the next
v1 command that writes (start, verify, review, accept, block, archive, abandon, change /
milestone / decision new, approve, gate, close, decision accept): the base branch back at
its pre-archive sha (a squash commit undone, a half squash dropped — cleaning only the
paths archive writes), change/<id> at its recorded tip and checked out. That command then
exits 1, archive.interrupted (retry), saying what was rolled back; archive again.
`+"`ape status`"+` reports the journal (JSON "archive_journal") and changes nothing.

Exit codes: 0 archived (prints the commit) · 1 not reviewed, re-verify red, stale review, a
git hook refused the squash,
pending human-review, change.md over its cap or the file ceiling, dirty tree, no change
branch, the base behind its upstream
(base.behind-upstream), the base branch checked out in another
worktree (git.base-checked-out: stop, do not re-plan), or git failed · 2 usage, or
--allow-pending-human from a model session · 3 the
rebase conflicts or the delta no longer applies.`,
		"  ape change archive add-tenant-delete",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			return runLong(cmd, &f, p, id, func(ctx context.Context, opt apexv1.RunOptions) renderFn {
				res, err := p.Archive(ctx, id, allowPending, opt)
				return func(cmd *cobra.Command, f *v1Flags) error { return renderArchive(cmd, f, id, res, err) }
			})
		})
	cmd.Flags().BoolVar(&allowPending, "allow-pending-human", false, "archive with human-review tasks unaccepted (recorded in state.json; a person only)")
	addV1TimeoutFlag(cmd, &f)
	addV1WaitFlags(cmd, &f)
	return cmd
}

// renderArchive prints an archive's outcome.
func renderArchive(cmd *cobra.Command, f *v1Flags, id string, res *apexv1.ArchiveResult, err error) error {
	if err != nil {
		if res != nil && res.Verify != nil && !f.json {
			printChecks(cmd.OutOrStdout(), res.Verify.Results, true)
		}
		return v1Fail(cmd, f, err, res)
	}
	return v1Emit(cmd.OutOrStdout(), f, res, func(w io.Writer) {
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
}
