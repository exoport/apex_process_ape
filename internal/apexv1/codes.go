package apexv1

// Codes set in more than one place.
const (
	CodeWrongStatus          = "change.wrong-status"
	CodeCommitFailed         = "git.commit-failed"
	CodeMilestoneNotFound    = "milestone.not-found"
	CodeMilestoneNotApproved = "milestone.not-approved"
	CodeFolderExists         = "archive.folder-exists"
	CodeDecisionNotFound     = "decision.not-found"
	CodeDeltaConflict        = "archive.delta-conflict"
	CodeGitStatusFailed      = "git.status-failed"
	// CodeWrongBranch, CodeChangeBranch and CodeBranchFailed are the
	// branch rules' codes (D38).
	CodeWrongBranch  = "change.wrong-branch"
	CodeChangeBranch = "branch.change-branch"
	CodeBranchFailed = "git.branch-failed"
	CodeCheckFailed  = "change.check-failed"
)

// CodeDoc documents one diagnostic code.
type CodeDoc struct {
	Code string `json:"code"`
	Exit int    `json:"exit"`
	// Action is what the orchestrator does next (D47), so it branches on
	// one field instead of carrying code tables: one of Actions.
	Action  string `json:"action"`
	Meaning string `json:"meaning"`
}

// The diagnostic actions (D47). retry: run the same ape command again;
// apply: hand the change to apex-apply (code work, or re-start); review:
// run the review again; replan: apex-change re-plans the change (or plans
// the fix a red gate needs); stop: the run stops (a bug, the environment,
// or nothing to do); person: a person must act — the orchestrator lists it
// and moves on to other work, or stops when there is none.
const (
	ActionRetry  = "retry"
	ActionApply  = "apply"
	ActionReview = "review"
	ActionReplan = "replan"
	ActionStop   = "stop"
	ActionPerson = "person"
)

// Actions is the closed set of CodeDoc.Action values.
var Actions = []string{ActionRetry, ActionApply, ActionReview, ActionReplan, ActionStop, ActionPerson}

// ActionFor is the published action of a code; stop for an unknown code.
func ActionFor(code string) string {
	for _, c := range DiagnosticCodes {
		if c.Code == code {
			return c.Action
		}
	}
	return ActionStop
}

// DiagnosticCodes is the published list of every code a v1 command can
// put in a --json diagnostic (item 13). Exit is the process status that
// code comes with: 1 a check failed, 2 usage, 3 conflict. Finding codes
// (from `project check` and `change check`) come with exit 1. A test
// fails when code and list disagree.
var DiagnosticCodes = []CodeDoc{
	// usage (exit 2)
	{"usage.by", 2, "stop", "--by is required: who approves or accepts"},
	{"usage.change-id", 2, "stop", "the change id is not ^[a-z][a-z0-9-]{2,39}$"},
	{"usage.decision-slug", 2, "stop", "the decision slug is not kebab-case"},
	{"usage.findings", 2, "stop", "review: --findings is required without --run"},
	{"usage.findings-missing", 2, "stop", "review: the --findings file does not exist"},
	{"usage.gate", 2, "stop", "milestone new: --gate is required"},
	{"usage.milestone", 2, "stop", "change new: --milestone is required"},
	{"usage.no-such-task", 2, "stop", "accept: the change has no such task"},
	{"usage.one-line", 2, "stop", "milestone new: title and gate must be one line each"},
	{"usage.paths", 2, "stop", "change new: a --paths entry is not a valid glob"},
	{"usage.reason", 2, "stop", "block/abandon: --reason is required"},
	{"usage.review-run", 2, "stop", "review: --run takes no --verdict/--findings"},
	{"usage.task", 2, "stop", "accept: --task is required"},
	{"usage.title", 2, "stop", "milestone new: a title is required"},
	{"usage.touches", 2, "stop", "change new: --touches is empty or holds a non-kebab-case slug"},
	{"usage.verdict", 2, "stop", "review: --verdict is not pass or block"},
	{"review.nested", 2, "stop", "review --run inside a review session (depth 1)"},
	{"project.not-initialized", 2, "person", "no development folder: run `ape project init`"},
	{"session.person-only", 2, "person", "--allow-pending-human or --accept-red from a model session (APE_SESSION or CLAUDECODE set): only a person passes them, from a terminal (D45)"},
	{"usage.unclassified", 2, "stop", "a usage error without a code (a bug: report it)"},

	// failures (exit 1)
	{"accept.not-human-review", 1, "stop", "accept: the task has a command check; verify runs it"},
	{"archive.pending-human", 1, "person", "archive: human-review tasks are not accepted"},
	{"branch.change-branch", 1, "stop", "change new, milestone new/approve/gate/close, decision accept: HEAD is on a change branch; their records belong on the base branch (git switch <base>)"},
	{"branch.detached", 1, "stop", "start: HEAD is detached; a change branch is created from a branch, recorded as its base"},
	{"change.no-branch", 1, "person", "archive: the change has no change/<id> branch with a recorded base_branch (started before D38)"},
	{"change.wrong-branch", 1, "stop", "the command records a started change's state, and HEAD is not on its branch change/<id> (git switch change/<id>), or is on another change's branch"},
	{CodeBaseCheckedOut, 1, "stop", "the branch a record belongs on (the base branch, or a change's) is checked out in another worktree: ape does not commit behind that worktree's index; the message names the worktree — run the command there, or switch it off the branch"},
	{CodeRefMoved, 1, "retry", "the branch moved while ape wrote its record there without a checkout; nothing was written"},
	{"git.branch-failed", 1, "stop", "creating, switching to, rebasing or deleting a branch failed, or the base branch is missing"},
	{"archive.spec-invalid", 1, "replan", "archive: the merged spec would fail project check (e.g. over 20 KB)"},
	{"change.archived", 1, "stop", "the change is already archived"},
	{"change.attempts-exceeded", 1, "replan", "verify: the red verify that reaches limits.attempts since the change started; the change is now blocked"},
	{"change.approve-milestone", 1, "stop", "change approve: the change belongs to a milestone, which approves it (ape milestone approve); only a fix (milestone: none) is approved on its own"},
	{CodeCheckFailed, 1, "replan", "change.md fails `ape change check` (findings follow)"},
	{"change.exists", 1, "replan", "change new: a change with that id exists"},
	{"change.id-archived", 1, "replan", "change new: the id was used by an archived change"},
	{"change.no-change-md", 1, "replan", "the change folder has no change.md"},
	{"change.no-state", 1, "replan", "the change folder has no state.json (not made by `ape change new`)"},
	{"change.not-found", 1, "stop", "no such change"},
	{CodeNotApproved, 1, "person", "start of a fix (milestone: none) no person approved, or whose plan changed since the approval: ape change approve <id> --by <who>"},
	{"change.nothing-to-rebaseline", 1, "stop", "start on a started change whose plan, modified spec and regression command did not change"},
	{"change.plan-drift", 1, "apply", "## Tasks or ## Delta changed since start: re-start to re-baseline, or restore"},
	{"change.state-unreadable", 1, "person", "state.json does not parse"},
	{CodeWrongStatus, 1, "stop", "the command does not apply to the change's status"},
	{"decision.slug-taken", 1, "replan", "decision new: an ADR already uses the slug"},
	{CodeDecisionNotFound, 1, "replan", "no such ADR"},
	{"decision.not-supersedable", 1, "replan", "decision new --supersedes: the ADR is already superseded"},
	{"decision.not-proposed", 1, "person", "decision accept: the ADR is not proposed"},
	{"decision.malformed", 1, "person", "decision accept: the ADR fails its shape (findings follow)"},
	{"decision.write-failed", 1, "stop", "decision new: the ADR file could not be created"},
	{"gate.red", 1, "replan", "the milestone gate (command, a spec scenario, or a newly red regression command) is red"},
	{CodeCommitFailed, 1, "stop", "ape wrote its record but could not commit it (identity, lock)"},
	{"git.not-a-repo", 1, "stop", "archive needs a git repository"},
	{CodeGitStatusFailed, 1, "stop", "git status could not be read"},
	{"ape.below-floor", 1, "person", "ape task: the installed framework's _apex/ape-commands.yaml declares a min_ape_version above this ape (H1); no session starts — a person updates ape (the message carries the command)"},
	{"milestone.budget-exceeded", 1, "person", "ape task: the current milestone's spend is at its enforced budget (limits.budget: enforce); no session starts"},
	{"milestone.changes-in-flight", 1, "apply", "close: changes of the milestone are in-progress, verified or reviewed"},
	{"milestone.done", 1, "stop", "approve: the milestone is already done"},
	{"milestone.malformed", 1, "person", "approve: the milestone lacks a Gate or Goal (findings follow)"},
	{"milestone.no-gate", 1, "person", "the milestone has no Gate: line"},
	{CodeMilestoneNotApproved, 1, "person", "the milestone is not approved"},
	{CodeMilestoneNotFound, 1, "person", "no such milestone in roadmap.md"},
	{"project.check-failed", 1, "person", "`ape project check` found problems (findings follow)"},
	{"review.block", 1, "apply", "review --run: the review session's verdict is block"},
	{"review.no-verdict", 1, "retry", "review --run: the session ended without recording a verdict"},
	{"review.stale", 1, "review", "archive: code changed since the review"},
	{"review.start-failed", 1, "retry", "review --run: the review session could not be started"},
	{"review.voided", 1, "retry", "review --run: the session changed more than review.md; verdict voided"},
	{"roadmap.missing", 1, "person", "no roadmap.md"},
	{"settings.invalid-json", 1, "person", "init: .claude/settings.json is not valid JSON"},
	{"plugins.missing", 1, "stop", "a plugin _apex/config.yaml lists is not installed at project scope, or not at its ref, and installing or updating it failed (the message carries the command to run)"},
	{"tree.dirty", 1, "apply", "uncommitted changes where the command needs a clean tree: code outside the development folder (verify), or anything before a branch switch or a rebase onto a moved base"},
	{"verify.missing", 1, "apply", "no green verify to review"},
	{"verify.red", 1, "apply", "a task check, an effective-spec scenario check, or a newly red regression command is red"},
	{"verify.stale", 1, "apply", "code changed since the last verify"},
	{"error.unclassified", 1, "stop", "a failure without a code (a bug: report it)"},

	// conflicts (exit 3)
	{CodeRebaseConflict, 3, "replan", "start, verify, archive: the base branch moved and change/<id> does not rebase onto it (D40); the rebase was aborted, nothing else written, the change is blocked with the conflicting files; the orchestrator abandons it and apex-change re-plans a replacement from the current base (D43)"},
	{"archive.base-changed", 3, "replan", "a requirement this change modifies or removes changed in the spec since start (another branch); nothing written"},
	{CodeDeltaConflict, 3, "replan", "the delta no longer applies to the current specs; nothing written"},
	{CodeFolderExists, 3, "stop", "the archive folder already exists; nothing written"},
	{"change.requirement-conflict", 3, "replan", "another active change claims the same requirement"},
	{"conflict.unclassified", 3, "stop", "a conflict without a code (a bug: report it)"},

	// findings (exit 1, from project check / change check)
	{"file.missing", 1, "person", "a required file is missing"},
	{"layout.old-archive", 1, "person", "development/changes/archive/ exists (an older ape's archive): move its folders to development/archive/"},
	{"layout.old-state", 1, "person", "development/.apex/ exists (an older ape's state): move milestones.json and decisions.json to _apex/state/, delete it"},
	{"project.too-long", 1, "person", "project.md over 150 lines"},
	{"project.too-big", 1, "person", "project.md over 8 KB"},
	{"project.no-title", 1, "person", "project.md has no `# <name>`"},
	{"project.missing-section", 1, "person", "project.md lacks a required ## section"},
	{"project.empty-section", 1, "person", "a required project.md section is empty"},
	{"agents.too-long", 1, "person", "AGENTS.md over 120 lines"},
	{"claude-md.no-import", 1, "person", "CLAUDE.md lacks the line @AGENTS.md"},
	{"roadmap.title", 1, "person", "roadmap.md does not start with # Roadmap"},
	{"roadmap.no-milestones", 1, "person", "roadmap.md has no milestone"},
	{"roadmap.bad-heading", 1, "person", "a roadmap section is not ## M<n> — <title>"},
	{"roadmap.duplicate-milestone", 1, "person", "a milestone id appears twice"},
	{"roadmap.no-status", 1, "person", "a milestone has no Status: line"},
	{"roadmap.bad-status", 1, "person", "a milestone Status: is not one ape writes"},
	{"roadmap.no-gate", 1, "person", "a milestone has no Gate:"},
	{"roadmap.no-goal", 1, "person", "a milestone has no Goal:"},
	{"roadmap.bad-budget", 1, "person", "a milestone Budget: is not a number"},
	{"roadmap.delivery-link", 1, "person", "a brief the roadmap's Delivery: line links does not resolve to a file"},
	{"roadmap.delivery-unlinked", 1, "person", "development/briefs/ holds briefs but no Delivery: line links one"},
	{"brief.bad-name", 1, "person", "a file under development/briefs/ is not YYYY-MM-DD-<slug>.md (a real date)"},
	{"brief.uncommitted", 1, "person", "a brief is not committed: briefs are committed with the plan"},
	{"brief.edited", 1, "person", "a brief differs from the text it was first committed with: restore it, write a new dated brief"},
	{"decision.bad-name", 1, "person", "an ADR file is not NNNN-<slug>.md"},
	{"decision.bad-title", 1, "person", "an ADR's heading is not # ADR-NNNN: <title>"},
	{"decision.no-status", 1, "person", "an ADR has no valid Status:"},
	{"decision.no-date", 1, "person", "an ADR has no Date:"},
	{"decision.no-enforced-by", 1, "person", "an ADR has no Enforced by:"},
	{"decision.missing-section", 1, "person", "an ADR lacks Context/Decision/Consequences"},
	{"decision.accepted-edited", 1, "person", "an accepted ADR's text changed after acceptance: restore it, supersede instead"},
	{"decision.bad-supersedes", 1, "person", "a Supersedes: line names no ADR"},
	{"spec.bad-capability-name", 1, "person", "a capability folder is not kebab-case"},
	{"spec.missing", 1, "person", "a capability folder has no spec.md"},
	{"spec.too-big", 1, "person", "a spec over 20 KB: split the capability"},
	{"spec.no-title", 1, "person", "a spec has no # title"},
	{"spec.no-purpose", 1, "person", "a spec has no Purpose: line"},
	{"spec.duplicate-requirement", 1, "person", "a requirement name appears twice"},
	{"spec.bad-heading", 1, "person", "a level-3 heading that is not ### Requirement:"},
	{"spec.unnamed-requirement", 1, "person", "a requirement has no name"},
	{"spec.no-scenario", 1, "person", "a requirement has no scenario"},
	{"spec.no-check", 1, "person", "a scenario has no check: line"},
	{"spec.multiple-checks", 1, "person", "a scenario has more than one check:"},
	{"spec.empty-check", 1, "person", "a scenario's check: is empty"},
	{"spec.stray-check", 1, "person", "a check: line outside any scenario"},
	{"change.too-big", 1, "replan", "change.md over 8 KB: split the change"},
	{"change.design-too-big", 1, "replan", "design.md over 8 KB"},
	{"change.frontmatter", 1, "replan", "change.md frontmatter is missing, unparseable, or its id/milestone/touches are wrong"},
	{"change.unknown-key", 1, "replan", "change.md frontmatter has a key v1 does not define (own keys: prefix x-)"},
	{"change.bad-id", 1, "replan", "the change folder name is not a valid id"},
	{"change.unknown-milestone", 1, "replan", "the change's milestone is not in roadmap.md"},
	{"change.bad-touches", 1, "replan", "a touches entry is not kebab-case"},
	{"change.bad-decision-ref", 1, "replan", "a decisions entry is not ADR-NNNN"},
	{"change.unknown-decision", 1, "replan", "a referenced ADR has no file"},
	{"change.bad-path-glob", 1, "replan", "a paths entry is not a valid glob"},
	{"change.no-title", 1, "replan", "change.md has no # title"},
	{"change.missing-section", 1, "replan", "change.md lacks a required ## section"},
	{"change.empty-section", 1, "replan", "## Why or ## Out of scope is empty"},
	{"change.empty-delta", 1, "replan", "## Delta is empty"},
	{"change.bad-delta", 1, "replan", "## Delta holds something other than delta blocks or `none — <reason>`"},
	{"change.bad-delta-heading", 1, "replan", "a delta heading is malformed"},
	{"change.bad-delta-capability", 1, "replan", "a delta capability is not kebab-case"},
	{"change.delta-not-touched", 1, "replan", "a delta capability is not in touches"},
	{"change.unnamed-delta", 1, "replan", "a delta block has no requirement name"},
	{"change.duplicate-delta", 1, "replan", "a requirement appears twice in the delta"},
	{"change.added-exists", 1, "replan", "ADDED names an existing requirement"},
	{"change.added-near-duplicate", 1, "replan", "ADDED names a requirement that nearly matches an existing one (case, spacing, a typo)"},
	{"change.dropped-text", 1, "replan", "delta text that archive would not carry into any spec"},
	{"change.capability-emptied", 1, "replan", "the delta removes a capability's last requirement without retire:"},
	{"change.bad-retire", 1, "replan", "retire: lists a capability the delta does not empty"},
	{"change.purpose-existing", 1, "replan", "an ADDED block's Purpose: names a capability that already exists"},
	{"change.purpose-empty", 1, "replan", "an ADDED block's Purpose: is empty"},
	{"change.scenario-loss", 1, "replan", "MODIFIED drops a scenario or changes its check"},
	{"change.modified-missing", 1, "replan", "MODIFIED names no existing requirement"},
	{"change.removed-missing", 1, "replan", "REMOVED names no existing requirement"},
	{"change.removed-no-reason", 1, "replan", "REMOVED has no Reason:"},
	{"change.no-tasks", 1, "replan", "## Tasks has no tasks"},
	{"change.duplicate-task", 1, "replan", "a task id appears twice"},
	{"change.task-checkbox", 1, "replan", "a task has a checkbox"},
	{"change.bad-task-line", 1, "replan", "a ## Tasks list item is not - T<n> <what> — check: …"},
	{"change.task-no-check", 1, "replan", "a task has no check:"},
	{"hooks.missing", 1, "person", "an ape hook is not registered in .claude/settings.json: `ape project init` merges it back"},
	{"hooks.altered", 1, "person", "an ape hook in .claude/settings.json has a changed matcher, type, async or timeout: `ape project init` restores it"},
	{"plugins.config", 1, "person", "_apex/config.yaml marketplaces/plugins are malformed (unknown source, missing repo/url/path, a plugin listed twice)"},
	{"plugins.drift", 1, "person", "settings.json's extraKnownMarketplaces/enabledPlugins differ from _apex/config.yaml, or a listed plugin is not installed or not at its ref: `ape doctor --fix`"},
	{UnclassifiedFinding, 1, "stop", "a finding no rule classifies (a bug: report it)"},
}
