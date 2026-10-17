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
	// CodeHookFailed: a project git hook refused the archive's squash
	// commit (H15).
	CodeHookFailed = "archive.hook-failed"

	CodeChangeNotFound    = "change.not-found"
	CodeStateUnreadable   = "change.state-unreadable"
	CodeUnsupportedLayout = "lift.unsupported-layout"
	CodeTagNotPushed      = "lift.tag-not-pushed"
	CodeLiftWriteFailed   = "lift.write-failed"
	CodeClaudeIgnored     = "lift.claude-ignored"

	CodeNoSuchTask            = "usage.no-such-task"
	CodeNoChangeMD            = "change.no-change-md"
	CodeDelegateInvalidConfig = "delegate.invalid-config"
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
	{"usage.evidence", 2, "person", "accept: an --evidence value is neither a URL (scheme://…) nor a regular file in the repository"},
	{"usage.findings", 2, "stop", "review: --findings is required without --run"},
	{"usage.findings-missing", 2, "stop", "review: the --findings file does not exist"},
	{"usage.gate", 2, "stop", "milestone new: --gate is required"},
	{"usage.milestone", 2, "stop", "change new: --milestone is required"},
	{CodeNoSuchTask, 2, "stop", "accept: the change has no such task"},
	{"usage.one-line", 2, "stop", "milestone new: title and gate must be one line each"},
	{"usage.paths", 2, "stop", "change new: a --paths entry is not a valid glob"},
	{"usage.reason", 2, "stop", "block/abandon/accept --verdict: --reason is required; close --accept-red: its reason is empty or more than one line"},
	{"usage.review-run", 2, "stop", "review: --run takes no --verdict/--findings"},
	{"usage.task", 2, "stop", "accept: the task (T<n>, or --task) is required; change tool: the entry's args use {task} and no --task was given"},
	{"usage.text", 2, "person", "answer: --text is required (the answer); next add: the text is empty or more than one line"},
	{"usage.title", 2, "stop", "milestone new: a title is required"},
	{"usage.touches", 2, "stop", "change new: --touches is empty or holds a non-kebab-case slug"},
	{"usage.verdict", 2, "stop", "review: --verdict is not pass or block; accept --verdict: not pass or fail"},
	{"usage.wait", 2, "stop", "--wait is outside 0–9m30s: one call must stay under Claude Code's 10-minute Bash cap; run the same command again to wait longer (D58 2.3)"},
	{"review.nested", 2, "stop", "review --run inside a review session (depth 1)"},
	{"delegate.nested", 2, "stop", "accept --delegate inside a delegate session"},
	{"delegate.not-in-session", 2, "stop", "accept --verdict outside the delegate session ape started for that change and task (APE_DELEGATE=<id>/<T> and its run): only the delegate records a verdict"},
	{"usage.delegate", 2, "stop", "accept: --delegate, --verdict and --by are three different acts; one at a time (--reason only with --verdict)"},
	{"project.not-initialized", 2, "person", "no development folder: run `ape project init`"},
	{CodeSessionDepth, 2, "stop", "ape task, prompt, chat or change review --run from a model session that may not start one (D62): depth 1 that is not ape's own apex-orchestrator run, or depth 2 (a session an orchestrator started); depth comes from what ape recorded at launch (APE_DEPTH and the process ancestry), never from the model — the session finishes its own work; a person or the orchestrator starts the next session. ape script refuses at any depth >= 1"},
	{CodeSessionNotAllowed, 2, "stop", "an apex-orchestrator run starting a session that is not `ape task <skill>` / `ape change review --run` for a skill on its allow-list (the core skills apex-plan, apex-change, apex-apply, apex-review, apex-release, plus tools: skills) (D62)"},
	{"session.person-only", 2, "person", "a person-only flag or command from a model session (APE_SESSION or CLAUDECODE set): --allow-pending-human, --accept-red (D45), ape milestone approve, ape change approve, ape change answer (D47 Q6) — a person runs it from a terminal outside Claude Code (a `!` line inside Claude Code is a model session)"},
	{"usage.lift-exclude", 2, "stop", "lift: an --exclude glob does not compile"},
	{"usage.unclassified", 2, "stop", "a usage error without a code (a bug: report it)"},

	// failures (exit 1)
	{"accept.not-human-review", 1, "stop", "accept: the task has a command check; verify runs it"},
	{"archive.pending-human", 1, "person", "archive: human-review tasks are not accepted; the orchestrator leaves the change reviewed and moves on (D45) — a person accepts them, or archives past them with --allow-pending-human (a person only)"},
	{"branch.change-branch", 1, "stop", "change new, milestone new/approve/gate/close, decision accept: HEAD is on a change branch; their records belong on the base branch (git switch <base>)"},
	{"branch.detached", 1, "stop", "start: HEAD is detached; a change branch is created from a branch, recorded as its base"},
	{"change.no-branch", 1, "person", "archive: the change has no change/<id> branch with a recorded base_branch (started before D38)"},
	{"change.wrong-branch", 1, "stop", "the command records a started change's state, and HEAD is not on its branch change/<id> (git switch change/<id>), or is on another change's branch"},
	{CodeBaseCheckedOut, 1, "stop", "the branch ape must switch to or commit a record on (the base branch, or a change's) is checked out in another worktree (the message names its path): archive, abandon, a switch, or a record written from another branch; an environment problem — stop, do not re-plan; run the command there, or switch that worktree off the branch (D45)"},
	{CodeRefMoved, 1, "retry", "the branch moved while ape wrote its record there without a checkout; nothing was written"},
	{"git.branch-failed", 1, "stop", "creating, switching to, rebasing or deleting a branch failed, or the base branch is missing"},
	{CodeBehindUpstream, 1, "person", "start (a new start, or a re-entry that catches up) and archive: the base branch is behind its upstream (the local remote-tracking ref; ape never fetches) — a person pulls (D45: one operator per checkout, local trunk)"},
	{CodeArchiveInterrupted, 1, "retry", "a writing command found the journal of an archive that crashed (_apex/state/archive-journal.json) and rolled the checkout back to it — the base at its pre-archive sha, change/<id> at its recorded tip, checked out; the message says what was undone; archive again, then the command (H28)"},
	{CodeRunInterrupted, 1, "retry", "a start, verify, archive, gate, close or lift (its regression run at apex-v0-final) was interrupted (SIGTERM/SIGINT mid-check, or its run lock's process is gone without a result — killed, or the machine restarted): its checks were stopped and NOTHING was recorded — no attempt, no state commit, no gate run (D58 2.2); run the same command again"},
	{"run.start-failed", 1, "retry", "start, verify, archive, gate, close: the detached worker could not be started (D58 2.3); nothing ran"},
	{CodeHookFailed, 1, "apply", "archive: a project git hook (pre-commit, commit-msg; commit.hooks: run, the default) refused the squash commit; archive rolled back (the base unchanged, change/<id> checked out again) and the message carries the hook output's tail — fix it on the branch, verify, archive again"},
	{"archive.spec-invalid", 1, "replan", "archive: the merged spec would fail project check (e.g. over its caps.spec cap)"},
	{"change.archived", 1, "stop", "the change is already archived"},
	{CodeAttemptsTotalExceeded, 1, "person", "verify: the red verify that reaches limits.attempts_total (default 15) across the change's starts since a person last answered it; the change is now blocked — re-planning did not converge: a person answers, abandons, or raises the limit (H19)"},
	{CodeReplansExceeded, 1, "person", "start: the re-plan past limits.replans (default 3) since a person last answered the change; the change is now blocked — a person answers, abandons, or raises the limit (H19)"},
	{"change.attempts-exceeded", 1, "replan", "verify: the red verify that reaches limits.attempts since the change started; the change is now blocked"},
	{"change.approve-milestone", 1, "stop", "change approve: the change belongs to a milestone, which approves it (ape milestone approve); only a fix (milestone: none) is approved on its own"},
	{CodeCheckFailed, 1, "replan", "change.md fails `ape change check` (findings follow)"},
	{"change.exists", 1, "replan", "change new: a change with that id exists"},
	{"change.id-archived", 1, "replan", "change new: the id was used by an archived change"},
	{CodeNoChangeMD, 1, "replan", "the change folder has no change.md"},
	{"change.no-state", 1, "replan", "the change folder has no state.json (not made by `ape change new`)"},
	{CodeChangeNotFound, 1, "stop", "no such change"},
	{CodeNotApproved, 1, "person", "start of a fix (milestone: none) no person approved, or whose plan changed since the approval: ape change approve <id> --by <who>"},
	{CodeVacuousCheck, 1, "replan", "start: the change ADDs or MODIFIES behaviour and a task check already passes before any of its code (backlog 43); rewrite it to fail until the change is done, or mark a check meant to stay green `— regression: <command>`; nothing written"},
	{"change.nothing-to-rebaseline", 1, "stop", "start on a started change whose plan, modified spec and regression command did not change"},
	{"change.plan-drift", 1, "apply", "## Tasks or ## Delta changed since start: re-start to re-baseline, or restore"},
	{CodeStateUnreadable, 1, "person", "state.json does not parse"},
	{CodeWrongStatus, 1, "stop", "the command does not apply to the change's status (answer: only a blocked change)"},
	{"decision.slug-taken", 1, "replan", "decision new: an ADR already uses the slug"},
	{CodeDecisionNotFound, 1, "replan", "no such ADR"},
	{"decision.not-supersedable", 1, "replan", "decision new --supersedes: the ADR is already superseded"},
	{"decision.not-proposed", 1, "person", "decision accept: the ADR is not proposed"},
	{"decision.malformed", 1, "person", "decision accept: the ADR fails its shape (findings follow)"},
	{"decision.write-failed", 1, "stop", "decision new: the ADR file could not be created"},
	{"gate.red", 1, "replan", "the milestone gate (command, a spec scenario, or a newly red regression command) is red"},
	{CodeCommitFailed, 1, "stop", "ape wrote its record but could not commit it (identity, lock)"},
	{"git.not-a-repo", 1, "stop", "archive and lift need a git repository"},
	{"lift.not-v0", 1, "person", "lift: no APEX v0 install here (no _apex/framework.yaml recording a v0 version), or the project is APEX v1 already (_apex/state/, development/project.md, or an `apex: lift` commit)"},
	{CodeUnsupportedLayout, 1, "person", "lift: the v0 framework version is outside v0.15.0–v0.31.x, unknown, or its _apex/config.yaml does not resolve; the message lists what was found"},
	{"lift.dirty", 1, "person", "lift: uncommitted changes (inside ape framework update: outside the paths the install writes)"},
	{"lift.no-tag", 1, "person", "lift: no tag apex-v0-final; a person tags the commit to lift and pushes the tag"},
	{"lift.tag-not-head", 1, "person", "lift: apex-v0-final does not point at HEAD"},
	{CodeTagNotPushed, 1, "person", "lift: the current branch has no upstream remote, or apex-v0-final is not on it, or points elsewhere there (git ls-remote)"},
	{"lift.remote-unreachable", 1, "retry", "lift: git ls-remote against the upstream remote failed (network, credentials)"},
	{CodeClaudeIgnored, 1, "person", "lift: git ignores a path the lift commits — .claude/settings.json (the v1 hooks) as a rule, under an ignored .claude/ — so git add would refuse it mid-lift; a person un-ignores it (.claude/* and !.claude/settings.json), commits, moves apex-v0-final to that commit and pushes it"},
	{CodeLiftWriteFailed, 1, "stop", "lift: removing the v0 families or writing a file failed mid-lift; git reset --hard restores the tree"},
	{CodeGitStatusFailed, 1, "stop", "git status could not be read"},
	{"ape.below-floor", 1, "person", "ape task: the installed framework's _apex/ape-commands.yaml declares a min_ape_version above this ape (H1); no session starts — a person updates ape (the message carries the command)"},
	{CodeBlocksExceeded, 1, "stop", "the milestone's changes blocked in a row reached limits.consecutive_blocks (default 2): change new and change start of its changes and ape task apex-orchestrator for it refuse, and ape status --json reports it under milestone.diagnostics; the run stops — a person deals with the blocked changes and re-approves the milestone (ape milestone approve), which resets the count (H19, D49)"},
	{"milestone.budget-exceeded", 1, "person", "ape task: the current milestone's spend is at its enforced budget (limits.budget: enforce); no session starts"},
	{"milestone.changes-in-flight", 1, "apply", "close: changes of the milestone are in-progress, verified or reviewed; the action is person instead when one of them has a non-empty awaiting_human (reviewed, human-review tasks not accepted: D49)"},
	{CodeStatusUnrecorded, 1, "person", "a milestone's Status says approved (or done) and ape holds no record of it in milestones.json — approved by hand, or before ape recorded approvals (H2); change start, gate and close refuse it: a person runs `ape milestone approve <M> --by <who>` (a done one: restore its Status line)"},
	{CodeGateChanged, 1, "person", "the milestone's Gate: command, or a script it runs (a recorded gate file), differs from what a person approved (H2, D51); gate and close refuse it: a person reviews the gate and runs `ape milestone approve <M> --by <who>` again"},
	{"milestone.done", 1, "stop", "approve: the milestone is already done"},
	{"milestone.malformed", 1, "person", "approve: the milestone lacks a Gate or Goal (findings follow)"},
	{"milestone.no-gate", 1, "person", "the milestone has no Gate: line"},
	{CodeMilestoneNotApproved, 1, "person", "the milestone is not approved"},
	{CodeMilestoneNotFound, 1, "person", "no such milestone in roadmap.md"},
	{"project.check-failed", 1, "person", "`ape project check` found problems (findings follow)"},
	{"review.block", 1, "apply", "review --run: the review session's verdict is block"},
	{"review.no-verdict", 1, "retry", "review --run: the session ended without recording a verdict"},
	{"review.stale", 1, "review", "archive: code changed since the review; `ape change review <id> --run` verifies it again first, then reviews it (D51 Q16)"},
	{"review.start-failed", 1, "retry", "review --run: the review session could not be started"},
	{"review.voided", 1, "retry", "review --run: the session changed more than review.md; verdict voided"},
	{"delegate.off", 1, "person", "accept --delegate: delegation is off — _apex/config.yaml has no human_review: block (project init writes none), so only a person accepts the task (D75, D78)"},
	{"delegate.already-failed", 1, "person", "accept --delegate: the task's last delegate verdict is a fail at the change's current tip (change/<id>'s tip; the archive folder once archived) — no automatic retry on the same artefact: a person reviews it, or the change moves first (D78)"},
	{"delegate.unaccepted-config", 1, "person", "accept --delegate: the human_review block was changed by a commit no person accepted (config.changed), or differs uncommitted from HEAD's — a model may not delegate a person's acceptance; a person runs `ape project accept-config --by <who>` or reverts it (D75)"},
	{CodeDelegateInvalidConfig, 1, "person", "human_review in _apex/config.yaml: args holds a placeholder other than {id}, {task}, {assets}, {change} or does not fill (a value that is not one shell-safe token), or effort is not a level; also a project check finding (D77)"},
	{"delegate.not-awaiting", 1, "stop", "accept --delegate: the task is not awaiting a person — the change is not reviewed (or archived) yet, or the task is accepted already"},
	{"delegate.voided", 1, "person", "accept --delegate: the delegate session committed, switched branch or left a file other than changes/<id>/assets/accept-*; its verdict is voided (recorded in its verdict file, shown by ape status) and nothing is accepted"},
	{"delegate.fail", 1, "person", "accept --delegate: the delegate's verdict is fail — or it ended without one (reason \"no verdict\", D78); the task stays awaiting_human and ape status shows the verdict (changes[].delegate.<T>) — a person reviews it"},
	{"delegate.start-failed", 1, "retry", "accept --delegate: the delegate session could not be started"},
	{"tool.unknown", 1, "replan", "change tool: no tools.design or tools.review entry in _apex/config.yaml has that name"},
	{"tool.args", 1, "person", "change tool: the entry's args template does not fill (a value that is not one shell-safe token)"},
	{"tool.start-failed", 1, "retry", "change tool: the tool session could not be started"},
	{"tool.failed", 1, "retry", "change tool: the tool session ended with a non-zero exit, or was lost (task_exit, log in the result)"},
	{"roadmap.missing", 1, "person", "no roadmap.md"},
	{"settings.invalid-json", 1, "person", "init: .claude/settings.json is not valid JSON"},
	{"plugins.missing", 1, "stop", "a plugin _apex/config.yaml lists is not installed at project scope, or not at its ref, and installing or updating it failed (the message carries the command to run)"},
	{"tree.dirty", 1, "apply", "uncommitted changes where the command needs a clean tree: code outside the development folder (verify), or anything before a branch switch or a rebase onto a moved base"},
	{"verify.missing", 1, "apply", "no green verify to review"},
	{"verify.red", 1, "apply", "a task check, an effective-spec scenario check, or a newly red regression command is red"},
	{"verify.stale", 1, "apply", "code changed since the last verify"},
	{CodeUnresolvedConflict, 1, "apply", "verify: a conflicted catch-up (change.conflicted) is not resolved yet — unmerged paths in the index, or conflict markers left in a file the rebase conflicted in; apex-apply resolves, `git add`s and commits (H8)"},
	{"error.unclassified", 1, "stop", "a failure without a code (a bug: report it)"},

	// conflicts (exit 3)
	{CodeConflicted, 3, "apply", "start, verify, archive: the base branch moved and change/<id> does not rebase onto it (D40, H8); ape aborted the rebase, kept the old tip under refs/apex/pre-rebase/<id>/<n>, recreated change/<id> at the base tip and cherry-picked the branch's net diff --no-commit: conflict markers and unmerged paths are in the tree, HEAD on the branch, the change in-progress (verify stale, review to run again); apex-apply resolves and commits — when it cannot, it blocks the change and the orchestrator re-plans (D43)"},
	{"archive.base-changed", 3, "replan", "a requirement this change modifies or removes changed in the spec since start (another branch); nothing written"},
	{CodeDeltaConflict, 3, "replan", "the delta no longer applies to the current specs; nothing written"},
	{CodeFolderExists, 3, "stop", "the archive folder already exists; nothing written"},
	{CodeGateGreen, 1, "replan", "milestone approve (and a re-approval): the gate, run once against the current tree (D70 B), exits 0 — a gate must be red until the milestone's work is done; apex-plan re-plans the gate"},
	{CodeGateBroken, 1, "replan", "milestone approve (and a re-approval): the gate cannot run (D70 B) — a script it names is missing or fails `bash -n`, it exits 126 or 127, a requires: entry is missing, or it times out (timeouts.gate); apex-plan re-plans the gate"},
	{"change.requirement-conflict", 3, "replan", "another active change claims the same requirement"},
	{CodeChildRunning, 3, "retry", "ape task / change review --run from an apex-orchestrator run that already has a child session running (D62: one at a time) — wait for it (`ape run wait <id> --wait 9m`), then start the next"},
	{CodeOrchestratorRunning, 3, "retry", "ape task apex-orchestrator in a project that already has a live apex-orchestrator run (D62: one per project) — wait for it to end, or stop it, then start again"},
	{CodeRunBusy, 3, "retry", "start, verify, archive, gate, close (and review --run's re-verify): another of these verbs is running on the same change or milestone (its run lock, _apex/state/runs/<id|M>.json); the message names it and since when — wait for it (running that command attaches), then run this one again (D58 2.1)"},
	{"conflict.unclassified", 3, "stop", "a conflict without a code (a bug: report it)"},

	// findings (exit 1, from project check / change check)
	{"file.missing", 1, "person", "a required file is missing"},
	{"layout.old-archive", 1, "person", "development/changes/archive/ exists (an older ape's archive): move its folders to development/archive/"},
	{"lift.disposition-pending", 1, "person", "development/lift-disposition.md exists after a lift: apex-plan fills it, a fresh read-only session then the operator review it; the operator deletes it and lift-disposition.json"},
	{"docs.missing", 1, "person", "a docs.changelog or docs.user_docs path in _apex/config.yaml does not exist (H21): create it, or fix the path"},
	{"layout.old-state", 1, "person", "development/.apex/ exists (an older ape's state): move milestones.json and decisions.json to _apex/state/, delete it"},
	{"project.too-big", 1, "person", "project.md over its caps.project_md cap (default 16 KB, ceiling 48 KB; no line cap, D69)"},
	{"project.no-title", 1, "person", "project.md has no `# <name>`"},
	{"project.missing-section", 1, "person", "project.md lacks a required ## section"},
	{"project.empty-section", 1, "person", "a required project.md section is empty"},
	{"agents.too-big", 1, "person", "AGENTS.md over its caps.agents_md cap (default 16 KB, ceiling 48 KB; its lines are not measured, D69)"},
	{CodeCapsOverCeiling, 1, "person", "_apex/config.yaml caps: holds a value above its hard ceiling (project_md, agents_md, change_md, design_md 48 KB; spec 64 KB; evidence_log 16 MiB): a project check finding, and change check, start, archive and abandon refuse until a person fixes it (D69)"},
	{"claude-md.no-import", 1, "person", "CLAUDE.md lacks the line @AGENTS.md"},
	{"roadmap.title", 1, "person", "roadmap.md does not start with # Roadmap"},
	{"roadmap.no-milestones", 1, "person", "roadmap.md has no milestone"},
	{"roadmap.bad-heading", 1, "person", "a roadmap section is not ## M<n> — <title> (or ## Next)"},
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
	{CodeDecisionUnrecorded, 1, "person", "an ADR says accepted and decisions.json holds no record of it (accepted by hand; the lift's records, by \"lift\", count): a person runs `ape decision accept ADR-NNNN --by <who>`, which records it as it is"},
	{"decision.bad-supersedes", 1, "person", "a Supersedes: line names no ADR"},
	{"spec.bad-capability-name", 1, "person", "a capability folder is not kebab-case"},
	{"spec.missing", 1, "person", "a capability folder has no spec.md"},
	{"spec.too-big", 1, "person", "a spec over its caps.spec cap (default 24 KB, ceiling 64 KB): split the capability"},
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
	{"change.too-big", 1, "replan", "change.md over its caps.change_md cap (default 16 KB, ceiling 48 KB): split the change; at archive and abandon (D69), trim it under the cap"},
	{"change.design-too-big", 1, "replan", "design.md over its caps.design_md cap (default 16 KB, ceiling 48 KB)"},
	{"change.frontmatter", 1, "replan", "change.md frontmatter is missing, unparseable, or its id/milestone/touches are wrong"},
	{"change.unknown-key", 1, "replan", "change.md frontmatter has a key v1 does not define (own keys: prefix x-)"},
	{"change.spec-too-big", 1, "replan", "change check / start: merging the delta would take a spec over its caps.spec cap — split the capability first (H18)"},
	{"change.no-type", 1, "replan", "change.md has no type: and _apex/config.yaml commit.subject uses {type} (D47)"},
	{"change.bad-type", 1, "replan", "change.md type: is not one of feat, fix, perf, refactor, docs, test, build, ci, chore (D47)"},
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
	{"change.bad-task-line", 1, "replan", "a ## Tasks list item is not - T<n> <what> — check: <command> | human-review, or - T<n> <what> — regression: <command>"},
	{"change.task-no-check", 1, "replan", "a task has no check:"},
	{"hooks.missing", 1, "person", "an ape hook is not registered in .claude/settings.json: `ape project init` merges it back"},
	{"hooks.altered", 1, "person", "an ape hook in .claude/settings.json has a changed matcher, type, async or timeout: `ape project init` restores it"},
	{"plugins.config", 1, "person", "_apex/config.yaml marketplaces/plugins are malformed (unknown source, missing repo/url/path, a plugin listed twice)"},
	{"plugins.drift", 1, "person", "settings.json's extraKnownMarketplaces/enabledPlugins differ from _apex/config.yaml, or a listed plugin is not installed or not at its ref: `ape doctor --fix`"},
	{"plugin.hooks-or-mcp", 1, "person", "a plugin _apex/config.yaml lists carries hooks (hooks/hooks.json, plugin.json hooks) or MCP servers (.mcp.json, plugin.json mcpServers) its entry does not allow (D59 Q5): a person reviews them and adds allow: [hooks, mcp] to the entry, or drops the plugin"},
	{"tools.invalid", 1, "person", "_apex/config.yaml tools: is malformed (D59 Q4): an unknown slot (the slots are design and review), an entry that is not {name, skill, when} or misses one, a skill that is not <skill> or <plugin>:<skill>, a name used twice"},
	{"tools.not-installed", 1, "person", "a skill tools: names is not installed: a plugin skill whose plugin is not listed in plugins: or not installed or has no such skill, or no project or user skill of that name"},
	{"tools.unknown", 1, "replan", "a change.md design-tool task (`— tool: <name>`) or review_tools: entry names no tools.design / tools.review entry of _apex/config.yaml (D74): re-plan with a declared tool"},
	{"tools.not-invocable", 1, "person", "a skill tools: names has user-invocable: false, so ape task cannot type it as a slash command: drop the flag, or name another skill"},
	{UnclassifiedFinding, 1, "stop", "a finding no rule classifies (a bug: report it)"},
}
